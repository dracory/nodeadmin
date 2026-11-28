package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dracory/entitystore"
	_ "modernc.org/sqlite"

	"github.com/dracory/nodebase"
	"github.com/dracory/nodestore"
)

// Endpoint-level tests for the nodeadmin API surface: every route is
// exercised through httptest against a real in-memory nodestore and a
// fake EngineInterface — the same narrow contract
// noderunner.WorkflowEngine satisfies in production (task
// admin_layer_test_coverage).

// newTestStore builds an in-memory nodestore (single connection so
// the :memory: database is shared).
func newTestStore(t *testing.T) nodestore.StoreInterface {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	entityStore, err := entitystore.NewStore(entitystore.NewStoreOptions{
		DB:                 db,
		EntityTableName:    "entity",
		AttributeTableName: "attribute",
		AutomigrateEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := nodestore.NewStore(nodestore.NewStoreOptions{EntityStore: entityStore})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return store
}

// postAPI posts a JSON body to an API endpoint and returns the decoded
// dracory/api envelope plus the recorder.
func postAPI(t *testing.T, handler http.Handler, path string, body map[string]interface{}) (map[string]interface{}, *httptest.ResponseRecorder) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBuffer(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var env map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("envelope decode failed for %s: %v (body: %s)", path, err, rec.Body.String())
	}
	return env, rec
}

// apiData returns the envelope's data map, failing on error envelopes.
func apiData(t *testing.T, env map[string]interface{}, path string) map[string]interface{} {
	t.Helper()
	if env["status"] != "success" {
		t.Fatalf("%s failed: %v", path, env["message"])
	}
	data, _ := env["data"].(map[string]interface{})
	return data
}

// apiError asserts the envelope is an error and returns its message.
func apiError(t *testing.T, env map[string]interface{}, path string) string {
	t.Helper()
	if env["status"] == "success" {
		t.Fatalf("%s unexpectedly succeeded: %v", path, env)
	}
	msg, _ := env["message"].(string)
	return msg
}

/* ---- fake engine ---- */

// fakeEngine is a minimal EngineInterface recording calls and serving
// scripted queue/journal/snapshot data — the fake pattern the API
// contract is written against.
type fakeEngine struct {
	deployed   map[string]bool
	deployErr  error
	stopErr    error
	queues     map[string]*fakeQueue // key workflowID + "/" + nodeID
	journal    map[string][]nodebase.RunJournalEntry
	fired      []string // "workflowID/nodeID"
	injected   []string
	discarded  []string
	events     []nodebase.NodeEvent
	lastSeq    int64
	runnerID   string
	partialHit bool
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		deployed: map[string]bool{},
		queues:   map[string]*fakeQueue{},
		journal:  map[string][]nodebase.RunJournalEntry{},
	}
}

func (f *fakeEngine) DeployWorkflow(ctx context.Context, workflow nodestore.WorkflowInterface, input nodestore.MessageInterface) (string, error) {
	if f.deployErr != nil {
		return "", f.deployErr
	}
	f.deployed[workflow.GetID()] = true
	return "exec-1", nil
}

func (f *fakeEngine) DeployWorkflowPartial(ctx context.Context, workflow nodestore.WorkflowInterface, input nodestore.MessageInterface) (string, error) {
	f.partialHit = true
	return f.DeployWorkflow(ctx, workflow, input)
}

func (f *fakeEngine) StopWorkflow(workflowID string) error {
	delete(f.deployed, workflowID)
	return f.stopErr
}

func (f *fakeEngine) GetWorkflowStatus(workflowID string) (string, error) {
	if f.deployed[workflowID] {
		return "running", nil
	}
	return "stopped", nil
}

func (f *fakeEngine) PauseWorkflow(workflowID string) error {
	if !f.deployed[workflowID] {
		return errors.New("workflow is not running")
	}
	return nil
}

func (f *fakeEngine) ResumeWorkflow(workflowID string) error {
	if !f.deployed[workflowID] {
		return errors.New("workflow is not running")
	}
	return nil
}

func (f *fakeEngine) FireInjectNode(workflowID, nodeID string) error {
	f.fired = append(f.fired, workflowID+"/"+nodeID)
	return nil
}

func (f *fakeEngine) InjectMessage(workflowID, fromNodeID string, payload nodestore.MessageInterface) (string, chan nodestore.MessageInterface, error) {
	f.injected = append(f.injected, workflowID+"/"+fromNodeID)
	return "msg-1", make(chan nodestore.MessageInterface, 1), nil
}

func (f *fakeEngine) DiscardPending(msgID string) {
	f.discarded = append(f.discarded, msgID)
}

func (f *fakeEngine) QueueHandle(workflowID, nodeID string) (nodebase.QueueInspector, bool) {
	q, ok := f.queues[workflowID+"/"+nodeID]
	if !ok {
		return nil, false
	}
	return q, true
}

func (f *fakeEngine) RunJournal(workflowID, nodeID string) ([]nodebase.RunJournalEntry, bool) {
	entries, ok := f.journal[workflowID+"/"+nodeID]
	return entries, ok
}

func (f *fakeEngine) GetWorkflowSnapshot(workflowID string, since int64) (string, map[string]string, []nodebase.NodeEvent, int64) {
	status, _ := f.GetWorkflowStatus(workflowID)
	var fresh []nodebase.NodeEvent
	for _, e := range f.events {
		if e.Seq > since {
			fresh = append(fresh, e)
		}
	}
	return status, map[string]string{"n1": "running"}, fresh, f.lastSeq
}

func (f *fakeEngine) LeaseRunner(workflowID string) string {
	return f.runnerID
}

/* ---- fake queue ---- */

// fakeQueue is a fakeQueue-backed nodebase.QueueInspector with an
// in-memory item list and a hold gate.
type fakeQueue struct {
	items    []nodebase.QueueItem
	held     bool
	released []int64
	routes   []string
	nextSeq  int64
}

func (q *fakeQueue) Items() []nodebase.QueueItem { return q.items }
func (q *fakeQueue) Count() int                  { return len(q.items) }
func (q *fakeQueue) Held() bool                  { return q.held }
func (q *fakeQueue) SetHold(hold bool)           { q.held = hold }

func (q *fakeQueue) Enqueue(p nodestore.MessageInterface) error {
	q.nextSeq++
	q.items = append(q.items, nodebase.QueueItem{Seq: q.nextSeq, EnqueuedAt: time.Now(), Payload: p})
	return nil
}

func (q *fakeQueue) DeleteItem(seq int64) error {
	for i, it := range q.items {
		if it.Seq == seq {
			q.items = append(q.items[:i], q.items[i+1:]...)
			return nil
		}
	}
	return errors.New("item not found")
}

func (q *fakeQueue) RequeueItem(seq int64) error { return q.ReleaseItem(seq, "", "") }

func (q *fakeQueue) ReleaseItem(seq int64, route string, approvedBy string) error {
	if err := q.DeleteItem(seq); err != nil {
		return err
	}
	q.released = append(q.released, seq)
	return nil
}

func (q *fakeQueue) RouteLabels() []string { return q.routes }

/* ---- helpers ---- */

// seedWorkflow persists a minimal workflow and returns it.
func seedWorkflow(t *testing.T, store nodestore.StoreInterface, id, name string) nodestore.WorkflowInterface {
	t.Helper()
	wf := nodestore.NewWorkflow(name).SetID(id).SetNodes([]nodestore.NodeInterface{
		nodestore.NewNode("n1", "junction").SetData(map[string]interface{}{}),
	}).SetEdges([]nodestore.EdgeInterface{
		nodestore.NewEdge("e1", "n1", "n2"),
	})
	if err := store.WorkflowCreate(context.Background(), wf); err != nil {
		t.Fatalf("workflow create: %v", err)
	}
	return wf
}

/* ---- transport gates ---- */

func TestHealth(t *testing.T) {
	a := New(newTestStore(t), nil)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"status":"success"`) {
		t.Fatalf("GET health: %s", rec.Body.String())
	}

	env, _ := postAPI(t, a, "/api/health", map[string]interface{}{})
	if env["status"] != "success" {
		t.Fatalf("POST health: %v", env)
	}
}

// TestWriteGates covers the cross-site and media-type guards that
// stand in for CORS on a localhost admin surface.
func TestWriteGates(t *testing.T) {
	a := New(newTestStore(t), nil)

	for _, tc := range []struct {
		name   string
		setup  func(r *http.Request)
		status int
	}{
		{"cross-site POST", func(r *http.Request) {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Sec-Fetch-Site", "cross-site")
		}, http.StatusForbidden},
		{"non-JSON POST", func(r *http.Request) {
			r.Header.Set("Content-Type", "text/plain")
		}, http.StatusUnsupportedMediaType},
		{"form POST", func(r *http.Request) {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}, http.StatusUnsupportedMediaType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/workflow-fetch-all", strings.NewReader("{}"))
			tc.setup(req)
			rec := httptest.NewRecorder()
			a.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
		})
	}
}

/* ---- workflow CRUD ---- */

func TestWorkflowCRUD(t *testing.T) {
	store := newTestStore(t)
	a := New(store, nil)

	// Create
	env, _ := postAPI(t, a, "/api/workflow-create", map[string]interface{}{
		"name": "flow-a",
		"nodes": []map[string]interface{}{
			{"id": "n1", "type": "junction", "position": map[string]any{"x": 1, "y": 2}, "data": map[string]any{}},
		},
		"edges": []map[string]interface{}{
			{"id": "e1", "source": "n1", "target": "n2"},
		},
	})
	created := apiData(t, env, "/api/workflow-create")["workflow"].(map[string]interface{})
	id, _ := created["id"].(string)
	if id == "" || created["name"] != "flow-a" {
		t.Fatalf("created workflow: %v", created)
	}

	// List
	env, _ = postAPI(t, a, "/api/workflow-fetch-all", map[string]interface{}{})
	list, _ := apiData(t, env, "/api/workflow-fetch-all")["workflows"].([]interface{})
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}

	// Get
	env, _ = postAPI(t, a, "/api/workflow-fetch", map[string]interface{}{"id": id})
	got := apiData(t, env, "/api/workflow-fetch")["workflow"].(map[string]interface{})
	if nodes, _ := got["nodes"].([]interface{}); len(nodes) != 1 {
		t.Fatalf("nodes = %v", got["nodes"])
	}
	if edges, _ := got["edges"].([]interface{}); len(edges) != 1 {
		t.Fatalf("edges = %v", got["edges"])
	}

	// Update
	env, _ = postAPI(t, a, "/api/workflow-update", map[string]interface{}{
		"id": id, "name": "flow-a-renamed",
	})
	apiData(t, env, "/api/workflow-update")
	env, _ = postAPI(t, a, "/api/workflow-fetch", map[string]interface{}{"id": id})
	got = apiData(t, env, "/api/workflow-fetch")["workflow"].(map[string]interface{})
	if got["name"] != "flow-a-renamed" {
		t.Fatalf("name = %v", got["name"])
	}

	// Delete
	env, _ = postAPI(t, a, "/api/workflow-delete", map[string]interface{}{"id": id})
	if env["status"] != "success" {
		t.Fatalf("delete: %v", env)
	}
	env, _ = postAPI(t, a, "/api/workflow-fetch", map[string]interface{}{"id": id})
	apiError(t, env, "/api/workflow-fetch")
}

// TestWorkflowCreateValidation covers the failure envelopes.
func TestWorkflowCreateValidation(t *testing.T) {
	a := New(newTestStore(t), nil)

	env, _ := postAPI(t, a, "/api/workflow-create", map[string]interface{}{"name": ""})
	apiError(t, env, "create without name")

	env, _ = postAPI(t, a, "/api/workflow-update", map[string]interface{}{"id": "", "name": "x"})
	apiError(t, env, "update without id")

	env, _ = postAPI(t, a, "/api/workflow-update", map[string]interface{}{"id": "missing", "name": "x"})
	apiError(t, env, "update missing workflow")

	env, _ = postAPI(t, a, "/api/workflow-delete", map[string]interface{}{"id": ""})
	apiError(t, env, "delete without id")
}

// TestWorkflowUpdatePreservesDeployed is the api-package-level
// regression guard for the deployed-flag wipe: a plain editor save
// (no deployed key) must not clear the server-managed flag.
func TestWorkflowUpdatePreservesDeployed(t *testing.T) {
	store := newTestStore(t)
	engine := newFakeEngine()
	a := New(store, engine)
	seedWorkflow(t, store, "wf-save", "wf-save")

	env, _ := postAPI(t, a, "/api/workflow-start", map[string]interface{}{"id": "wf-save"})
	apiData(t, env, "/api/workflow-start")

	env, _ = postAPI(t, a, "/api/workflow-update", map[string]interface{}{
		"id":   "wf-save",
		"name": "wf-save",
		"nodes": []map[string]interface{}{
			{"id": "n1", "type": "junction", "data": map[string]any{}},
		},
	})
	apiData(t, env, "/api/workflow-update")

	wf, err := store.WorkflowFindByID(context.Background(), "wf-save")
	if err != nil || !wf.GetDeployed() {
		t.Fatalf("deployed flag wiped by editor save: %v", err)
	}
}

/* ---- lifecycle: start / stop / status ---- */

func TestWorkflowStartStopStatus(t *testing.T) {
	store := newTestStore(t)
	engine := newFakeEngine()
	engine.runnerID = "runner-1"
	engine.events = []nodebase.NodeEvent{
		{Seq: 1, Time: time.Now(), NodeID: "n1", Level: "info", Message: "started"},
		{Seq: 2, Time: time.Now(), NodeID: "n2", Level: "debug", Message: "fired"},
	}
	engine.lastSeq = 2
	a := New(store, engine)
	seedWorkflow(t, store, "wf-1", "wf-1")

	// start (mode=full → DeployWorkflow, not the partial path)
	env, _ := postAPI(t, a, "/api/workflow-start", map[string]interface{}{"id": "wf-1", "mode": "full"})
	data := apiData(t, env, "/api/workflow-start")
	if data["execution_id"] != "exec-1" {
		t.Fatalf("execution_id = %v", data)
	}
	if engine.partialHit {
		t.Fatal("mode=full must not hit DeployWorkflowPartial")
	}
	wf, _ := store.WorkflowFindByID(context.Background(), "wf-1")
	if !wf.GetDeployed() {
		t.Fatal("deployed flag not persisted")
	}

	// status with incremental event polling
	env, _ = postAPI(t, a, "/api/workflow-status", map[string]interface{}{"id": "wf-1", "since": 1})
	data = apiData(t, env, "/api/workflow-status")
	if data["status"] != "running" || data["deployed"] != true {
		t.Fatalf("status data: %v", data)
	}
	if data["runner_id"] != "runner-1" {
		t.Fatalf("runner_id = %v", data["runner_id"])
	}
	if events, _ := data["events"].([]interface{}); len(events) != 1 {
		t.Fatalf("since=1 events = %v, want 1", events)
	}
	if seq, _ := data["last_seq"].(float64); seq != 2 {
		t.Fatalf("last_seq = %v", data["last_seq"])
	}

	// default mode hits the partial deploy path
	env, _ = postAPI(t, a, "/api/workflow-start", map[string]interface{}{"id": "wf-1"})
	apiData(t, env, "/api/workflow-start")
	if !engine.partialHit {
		t.Fatal("default mode must hit DeployWorkflowPartial")
	}

	// stop
	env, _ = postAPI(t, a, "/api/workflow-stop", map[string]interface{}{"id": "wf-1"})
	apiData(t, env, "/api/workflow-stop")
	wf, _ = store.WorkflowFindByID(context.Background(), "wf-1")
	if wf.GetDeployed() {
		t.Fatal("deployed flag not cleared on stop")
	}
}

// TestEngineUnavailable proves engine-bound endpoints report a clean
// error (not a stub success) when the admin has no engine, and
// workflow-status falls back to the persisted flag.
func TestEngineUnavailable(t *testing.T) {
	store := newTestStore(t)
	a := New(store, nil)
	seedWorkflow(t, store, "wf-1", "wf-1")

	for _, path := range []string{
		"/api/workflow-start", "/api/workflow-stop",
		"/api/queue-items", "/api/queue-hold",
		"/api/queue-item-delete", "/api/queue-item-requeue",
		"/api/node-inject", "/api/node-run-journal",
	} {
		env, _ := postAPI(t, a, path, map[string]interface{}{
			"id": "wf-1", "workflow_id": "wf-1", "node_id": "n1",
		})
		if msg := apiError(t, env, path); !strings.Contains(msg, "engine not available") {
			t.Fatalf("%s message = %q", path, msg)
		}
	}

	// status degrades gracefully to the persisted flag
	env, _ := postAPI(t, a, "/api/workflow-status", map[string]interface{}{"id": "wf-1"})
	data := apiData(t, env, "/api/workflow-status")
	if data["status"] != "stopped" || data["deployed"] != false {
		t.Fatalf("status = %v", data)
	}
}

/* ---- node types + schema + inject ---- */

func TestNodeTypesAndSchema(t *testing.T) {
	a := New(newTestStore(t), nil)

	env, _ := postAPI(t, a, "/api/node-fetch-all", map[string]interface{}{})
	types, _ := apiData(t, env, "/api/node-fetch-all")["node_types"].([]interface{})
	if len(types) == 0 {
		t.Fatal("no built-in node types registered")
	}

	// A custom schema registered through the package surface is served
	// back verbatim by node-schema-fetch.
	RegisterNodeSchema("x-custom", map[string]interface{}{
		"type": "object",
		"properties": map[string]any{
			"label": map[string]any{"type": "string"},
		},
	})
	env, _ = postAPI(t, a, "/api/node-schema-fetch", map[string]interface{}{"type": "x-custom"})
	schema, _ := apiData(t, env, "/api/node-schema-fetch")["schema"].(map[string]interface{})
	if schema["type"] != "object" {
		t.Fatalf("schema = %v", schema)
	}

	env, _ = postAPI(t, a, "/api/node-schema-fetch", map[string]interface{}{"type": "nope"})
	apiError(t, env, "unknown schema")
}

func TestQueueSchemaIsServedForEditorForm(t *testing.T) {
	a := New(newTestStore(t), nil)

	env, _ := postAPI(t, a, "/api/node-schema-fetch", map[string]interface{}{"type": "queue"})
	schema, _ := apiData(t, env, "/api/node-schema-fetch")["schema"].(map[string]interface{})
	properties, _ := schema["properties"].(map[string]interface{})
	for _, key := range []string{"capacity", "policy", "hold", "backend", "dir", "password", "keyFile", "hostKey"} {
		if properties[key] == nil {
			t.Fatalf("queue schema missing %q property: %v", key, properties)
		}
	}
	backend, _ := properties["backend"].(map[string]interface{})
	if backend["enum"] == nil {
		t.Fatalf("queue backend should expose enum options for the schema-driven editor: %v", backend)
	}
}

func TestNodeInject(t *testing.T) {
	engine := newFakeEngine()
	a := New(newTestStore(t), engine)

	// Explicit payload → InjectMessage + DiscardPending.
	env, _ := postAPI(t, a, "/api/node-inject", map[string]interface{}{
		"workflow_id": "wf-1", "node_id": "inj",
		"payload": map[string]interface{}{"data": "x"},
	})
	data := apiData(t, env, "/api/node-inject")
	if data["msg_id"] != "msg-1" {
		t.Fatalf("msg_id = %v", data)
	}
	if len(engine.injected) != 1 || engine.injected[0] != "wf-1/inj" {
		t.Fatalf("injected = %v", engine.injected)
	}
	if len(engine.discarded) != 1 || engine.discarded[0] != "msg-1" {
		t.Fatalf("discarded = %v", engine.discarded)
	}

	// No payload → fires the node's own configured payload.
	env, _ = postAPI(t, a, "/api/node-inject", map[string]interface{}{
		"workflow_id": "wf-1", "node_id": "inj",
	})
	apiData(t, env, "/api/node-inject")
	if len(engine.fired) != 1 || engine.fired[0] != "wf-1/inj" {
		t.Fatalf("fired = %v", engine.fired)
	}

	// Missing ids → error envelope.
	env, _ = postAPI(t, a, "/api/node-inject", map[string]interface{}{"workflow_id": "wf-1"})
	apiError(t, env, "missing node_id")
}

/* ---- queues + journal ---- */

func TestQueueEndpoints(t *testing.T) {
	engine := newFakeEngine()
	q := &fakeQueue{}
	_ = q.Enqueue(nodestore.NewMessage().SetPayload(map[string]any{"data": "one"}))
	_ = q.Enqueue(nodestore.NewMessage().SetPayload(map[string]any{"data": "two"}))
	engine.queues["wf-1/q1"] = q
	a := New(newTestStore(t), engine)

	env, _ := postAPI(t, a, "/api/queue-items", map[string]interface{}{
		"workflow_id": "wf-1", "node_id": "q1",
	})
	data := apiData(t, env, "/api/queue-items")
	if data["count"] != float64(2) || data["hold"] != false {
		t.Fatalf("queue data: %v", data)
	}
	items, _ := data["items"].([]interface{})
	if len(items) != 2 {
		t.Fatalf("items = %v", items)
	}
	first, _ := items[0].(map[string]interface{})
	if !strings.Contains(first["preview"].(string), "one") {
		t.Fatalf("preview = %v", first["preview"])
	}

	env, _ = postAPI(t, a, "/api/queue-hold", map[string]interface{}{
		"workflow_id": "wf-1", "node_id": "q1", "hold": true,
	})
	if apiData(t, env, "/api/queue-hold")["hold"] != true {
		t.Fatal("hold not set")
	}
	if !q.Held() {
		t.Fatal("queue gate not toggled")
	}

	env, _ = postAPI(t, a, "/api/queue-item-delete", map[string]interface{}{
		"workflow_id": "wf-1", "node_id": "q1", "seq": 1,
	})
	if apiData(t, env, "/api/queue-item-delete")["count"] != float64(1) {
		t.Fatal("delete did not drop the item")
	}

	env, _ = postAPI(t, a, "/api/queue-item-requeue", map[string]interface{}{
		"workflow_id": "wf-1", "node_id": "q1", "seq": 2,
	})
	apiData(t, env, "/api/queue-item-requeue")
	if len(q.released) != 1 || q.released[0] != 2 {
		t.Fatalf("released = %v", q.released)
	}
}

func TestQueueNotFound(t *testing.T) {
	a := New(newTestStore(t), newFakeEngine())

	env, _ := postAPI(t, a, "/api/queue-items", map[string]interface{}{
		"workflow_id": "wf-x", "node_id": "q",
	})
	apiError(t, env, "queue not found")

	env, _ = postAPI(t, a, "/api/queue-item-delete", map[string]interface{}{
		"workflow_id": "wf-x", "node_id": "q", "seq": 1,
	})
	apiError(t, env, "queue not found for delete")
}

func TestNodeRunJournal(t *testing.T) {
	engine := newFakeEngine()
	engine.journal["wf-1/n1"] = []nodebase.RunJournalEntry{
		{Seq: 1, Time: time.Now(), DurationMs: 1.5, Input: "in", Output: "out"},
	}
	a := New(newTestStore(t), engine)

	env, _ := postAPI(t, a, "/api/node-run-journal", map[string]interface{}{
		"workflow_id": "wf-1", "node_id": "n1",
	})
	data := apiData(t, env, "/api/node-run-journal")
	if data["size"] != float64(nodebase.RunJournalSize) {
		t.Fatalf("size = %v", data["size"])
	}
	if entries, _ := data["entries"].([]interface{}); len(entries) != 1 {
		t.Fatalf("entries = %v", entries)
	}

	env, _ = postAPI(t, a, "/api/node-run-journal", map[string]interface{}{
		"workflow_id": "wf-x", "node_id": "n1",
	})
	apiError(t, env, "journal for non-running workflow")
}

/* ---- messages CRUD ---- */

func TestMessageCRUD(t *testing.T) {
	a := New(newTestStore(t), nil)

	env, _ := postAPI(t, a, "/api/message-create", map[string]interface{}{
		"payload":   map[string]interface{}{"data": "hello"},
		"metadata":  map[string]interface{}{"origin": "test"},
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
	})
	created := apiData(t, env, "/api/message-create")["message"].(map[string]interface{})
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("created message: %v", created)
	}

	env, _ = postAPI(t, a, "/api/message-fetch-all", map[string]interface{}{})
	list, _ := apiData(t, env, "/api/message-fetch-all")["messages"].([]interface{})
	if len(list) != 1 {
		t.Fatalf("messages = %v", list)
	}

	env, _ = postAPI(t, a, "/api/message-fetch", map[string]interface{}{"id": id})
	apiData(t, env, "/api/message-fetch")

	env, _ = postAPI(t, a, "/api/message-update", map[string]interface{}{
		"id": id, "payload": map[string]interface{}{"data": "updated"},
	})
	apiData(t, env, "/api/message-update")

	env, _ = postAPI(t, a, "/api/message-delete", map[string]interface{}{"id": id})
	if env["status"] != "success" {
		t.Fatalf("delete: %v", env)
	}
	env, _ = postAPI(t, a, "/api/message-fetch", map[string]interface{}{"id": id})
	apiError(t, env, "deleted message fetch")
}

/* ---- global env + workflow groups ---- */

func TestGlobalEnv(t *testing.T) {
	a := New(newTestStore(t), nil)

	env, _ := postAPI(t, a, "/api/global-env-fetch", map[string]interface{}{})
	if envList, _ := apiData(t, env, "/api/global-env-fetch")["env"].([]interface{}); len(envList) != 0 {
		t.Fatalf("env = %v", envList)
	}

	env, _ = postAPI(t, a, "/api/global-env-update", map[string]interface{}{
		"env": []map[string]interface{}{
			{"name": "API_KEY", "type": "cred", "value": "s3cr3t"},
			{"name": "MODE", "type": "str", "value": "dev"},
		},
	})
	apiData(t, env, "/api/global-env-update")

	env, _ = postAPI(t, a, "/api/global-env-fetch", map[string]interface{}{})
	envList, _ := apiData(t, env, "/api/global-env-fetch")["env"].([]interface{})
	if len(envList) != 2 {
		t.Fatalf("env = %v", envList)
	}

	env, _ = postAPI(t, a, "/api/global-env-update", map[string]interface{}{
		"env": "not-an-array",
	})
	apiError(t, env, "non-array env")
}

func TestWorkflowGroups(t *testing.T) {
	a := New(newTestStore(t), nil)

	env, _ := postAPI(t, a, "/api/workflow-groups-fetch", map[string]interface{}{})
	groups, _ := apiData(t, env, "/api/workflow-groups-fetch")["workflow_groups"].([]interface{})
	if len(groups) != 0 {
		t.Fatalf("groups = %v", groups)
	}

	env, _ = postAPI(t, a, "/api/workflow-groups-update", map[string]interface{}{
		"workflow_groups": []map[string]interface{}{
			{"id": "g1", "name": "Prod", "parentId": "", "workflows": []string{"wf-1"}},
			{"id": "g2", "name": "Dev", "parentId": "", "workflows": []string{}},
		},
	})
	apiData(t, env, "/api/workflow-groups-update")

	env, _ = postAPI(t, a, "/api/workflow-groups-fetch", map[string]interface{}{})
	groups, _ = apiData(t, env, "/api/workflow-groups-fetch")["workflow_groups"].([]interface{})
	if len(groups) != 2 {
		t.Fatalf("groups = %v", groups)
	}

	// Dropping g2 from the update list deletes the entity.
	env, _ = postAPI(t, a, "/api/workflow-groups-update", map[string]interface{}{
		"workflow_groups": []map[string]interface{}{
			{"id": "g1", "name": "Prod", "parentId": "", "workflows": []string{"wf-1"}},
		},
	})
	apiData(t, env, "/api/workflow-groups-update")
	env, _ = postAPI(t, a, "/api/workflow-groups-fetch", map[string]interface{}{})
	groups, _ = apiData(t, env, "/api/workflow-groups-fetch")["workflow_groups"].([]interface{})
	if len(groups) != 1 {
		t.Fatalf("groups after delete = %v", groups)
	}

	env, _ = postAPI(t, a, "/api/workflow-groups-update", map[string]interface{}{
		"workflow_groups": "not-an-array",
	})
	apiError(t, env, "non-array groups")
}

/* ---- Node-RED + child flow import/export ---- */

func TestNodeREDImportExport(t *testing.T) {
	a := New(newTestStore(t), nil)

	flow := []map[string]interface{}{
		{"id": "tab1", "type": "tab", "label": "Main"},
		{"id": "n1", "type": "inject", "z": "tab1", "name": "tick", "wires": []any{[]any{"n2"}}},
		{"id": "n2", "type": "debug", "z": "tab1", "wires": []any{}},
	}
	raw, _ := json.Marshal(flow)
	env, _ := postAPI(t, a, "/api/workflow-import-nodered", map[string]interface{}{
		"name": "imported",
		"flow": json.RawMessage(raw),
	})
	created := apiData(t, env, "/api/workflow-import-nodered")["workflow"].(map[string]interface{})
	id, _ := created["id"].(string)
	if id == "" || created["name"] != "imported" {
		t.Fatalf("imported workflow: %v", created)
	}

	env, _ = postAPI(t, a, "/api/workflow-export-nodered", map[string]interface{}{"id": id})
	exported, _ := apiData(t, env, "/api/workflow-export-nodered")["flow"].([]interface{})
	if len(exported) == 0 {
		t.Fatal("export returned empty flow")
	}

	env, _ = postAPI(t, a, "/api/workflow-import-nodered", map[string]interface{}{})
	apiError(t, env, "import without flow")

	env, _ = postAPI(t, a, "/api/workflow-export-nodered", map[string]interface{}{"id": "missing"})
	apiError(t, env, "export missing workflow")
}

func TestChildFlowExportImportErrors(t *testing.T) {
	store := newTestStore(t)
	a := New(store, nil)
	seedWorkflow(t, store, "wf-1", "wf-1")

	// Workflow exists but holds no child flow named "m".
	env, _ := postAPI(t, a, "/api/child-flow-export", map[string]interface{}{
		"id": "wf-1", "flow": "m",
	})
	apiError(t, env, "child flow export")

	env, _ = postAPI(t, a, "/api/child-flow-export", map[string]interface{}{
		"id": "missing", "flow": "m",
	})
	apiError(t, env, "child flow export missing workflow")

	env, _ = postAPI(t, a, "/api/child-flow-import", map[string]interface{}{"id": "wf-1"})
	apiError(t, env, "child flow import without payload")
}

func TestChildFlowExportImportRoundTrip(t *testing.T) {
	store := newTestStore(t)
	a := New(store, nil)
	wf := seedWorkflow(t, store, "wf-1", "wf-1")
	wf.GetNodes()[0].SetData(map[string]interface{}{"childFlow": "flow-1"})
	wf.SetView(map[string]interface{}{
		"childFlows": []interface{}{
			map[string]interface{}{"id": "flow-1", "label": "Main", "role": "flow", "enabled": true},
		},
	})
	if err := store.WorkflowUpdate(context.Background(), wf); err != nil {
		t.Fatalf("workflow update: %v", err)
	}
	wf2 := nodestore.NewWorkflow("wf-2").SetID("wf-2").SetNodes([]nodestore.NodeInterface{
		nodestore.NewNode("target-n1", "junction").SetData(map[string]interface{}{}),
	})
	if err := store.WorkflowCreate(context.Background(), wf2); err != nil {
		t.Fatalf("target workflow create: %v", err)
	}

	env, _ := postAPI(t, a, "/api/child-flow-export", map[string]interface{}{
		"id": "wf-1", "childFlow": "flow-1",
	})
	exported, ok := apiData(t, env, "/api/child-flow-export")["child_flow_export"].(map[string]interface{})
	if !ok {
		t.Fatalf("canonical child_flow_export missing: %v", env)
	}

	env, _ = postAPI(t, a, "/api/child-flow-import", map[string]interface{}{
		"id": "wf-2", "child_flow_export": exported,
	})
	updated, ok := apiData(t, env, "/api/child-flow-import")["workflow"].(map[string]interface{})
	if !ok {
		t.Fatalf("updated workflow missing: %v", env)
	}
	view, _ := updated["view"].(map[string]interface{})
	flows, _ := view["childFlows"].([]interface{})
	if len(flows) != 1 {
		t.Fatalf("imported child flow entities = %v", flows)
	}

	// The legacy flow and module_export fields remain accepted as input aliases.
	env, _ = postAPI(t, a, "/api/child-flow-export", map[string]interface{}{
		"id": "wf-1", "flow": "flow-1",
	})
	apiData(t, env, "/api/child-flow-export")
	env, _ = postAPI(t, a, "/api/child-flow-import", map[string]interface{}{
		"id": "wf-2", "module_export": exported,
	})
	apiData(t, env, "/api/child-flow-import")
}

func TestStructuredConfigSchemasAreServed(t *testing.T) {
	a := New(newTestStore(t), nil)

	fetchProperties := func(nodeType string) map[string]interface{} {
		env, _ := postAPI(t, a, "/api/node-schema-fetch", map[string]interface{}{"type": nodeType})
		schema, _ := apiData(t, env, "/api/node-schema-fetch")["schema"].(map[string]interface{})
		properties, _ := schema["properties"].(map[string]interface{})
		if properties == nil {
			t.Fatalf("%s schema has no properties: %v", nodeType, schema)
		}
		return properties
	}

	for _, nodeType := range []string{"httpRequest", "httpResponse"} {
		headers, _ := fetchProperties(nodeType)["headers"].(map[string]interface{})
		if headers["format"] != "keyvalue" {
			t.Fatalf("%s headers should use the key/value editor: %v", nodeType, headers)
		}
	}

	checkpointRoutes, _ := fetchProperties("checkpoint")["routes"].(map[string]interface{})
	if checkpointRoutes["type"] != "string" || checkpointRoutes["default"] != "approve, decline" {
		t.Fatalf("checkpoint routes should edit as comma-separated text: %v", checkpointRoutes)
	}

	formFields, _ := fetchProperties("uiForm")["fields"].(map[string]interface{})
	items, _ := formFields["items"].(map[string]interface{})
	itemProperties, _ := items["properties"].(map[string]interface{})
	for _, key := range []string{"name", "label", "type", "options", "value"} {
		if itemProperties[key] == nil {
			t.Fatalf("uiForm fields row missing %q: %v", key, itemProperties)
		}
	}
}
