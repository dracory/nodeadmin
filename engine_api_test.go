package nodeadmin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dracory/entitystore"
	_ "modernc.org/sqlite"

	"github.com/dracory/noderun"
	"github.com/dracory/nodestore"
)

// Endpoint-level tests proving the engine-bound API endpoints are wired
// to a real noderunner.WorkflowEngine through the narrow
// api.EngineInterface contract — start/stop deploy for real, inject
// fires, the queue endpoints reach the live queue, and the run journal
// returns recorded executions (task cutover_p1_engine_wiring).

// newEngineTestAdmin builds an admin backed by an in-memory sqlite
// store and an explicit noderunner engine.
func newEngineTestAdmin(t *testing.T) (Interface, *noderunner.WorkflowEngine, nodestore.StoreInterface) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	// :memory: databases are per-connection — cap the pool at one so
	// the engine, the durable inbox store and request handlers all
	// share the same database.
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
	engine := noderunner.NewWorkflowEngine()
	// Host-owned auth seam: the admin holds no users of its own.
	admin, err := New(Options{
		Store:           store,
		Engine:          engine,
		IsAccessAllowed: func(r *http.Request) bool { return true },
		GetAuthUserID:   func(r *http.Request) string { return "test-user" },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return admin, engine, store
}

// postAPI posts a JSON body to an API endpoint and returns the decoded
// dracory/api envelope.
func postAPI(t *testing.T, handler http.Handler, path string, body map[string]interface{}) map[string]interface{} {
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
	return env
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

// queueFlow builds inj(junction) -> f(queue) -> dbg(debug) and persists
// it in the store.
func queueFlow(t *testing.T, store nodestore.StoreInterface, id string, queueData map[string]interface{}) {
	t.Helper()
	if queueData == nil {
		queueData = map[string]interface{}{}
	}
	wf := nodestore.NewWorkflow(id).SetID(id).SetNodes([]nodestore.NodeInterface{
		nodestore.NewNode("inj", "junction").SetData(map[string]interface{}{}),
		nodestore.NewNode("f", "queue").SetData(queueData),
		nodestore.NewNode("dbg", "debug").SetData(map[string]interface{}{}),
	}).SetEdges([]nodestore.EdgeInterface{
		nodestore.NewEdge("e1", "inj", "f"),
		nodestore.NewEdge("e2", "f", "dbg"),
	})
	if err := store.WorkflowCreate(context.Background(), wf); err != nil {
		t.Fatalf("workflow create: %v", err)
	}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestWorkflowStartDeploysForReal proves /api/workflow-start drives the
// engine's deploy path: the run registers on the engine AND the
// persisted deployed flag is set.
func TestWorkflowStartDeploysForReal(t *testing.T) {
	admin, engine, store := newEngineTestAdmin(t)
	queueFlow(t, store, "wf-start", map[string]interface{}{"hold": true})

	env := postAPI(t, admin, "/api/workflow-start", map[string]interface{}{"id": "wf-start"})
	data := apiData(t, env, "/api/workflow-start")
	if data["execution_id"] == nil || data["execution_id"] == "" {
		t.Fatalf("expected execution_id from real deploy, got %v", data)
	}

	status, err := engine.GetWorkflowStatus("wf-start")
	if err != nil || status != "running" {
		t.Fatalf("engine status = %q, %v — deploy did not run", status, err)
	}

	wf, err := store.WorkflowFindByID(context.Background(), "wf-start")
	if err != nil || !wf.GetDeployed() {
		t.Fatalf("deployed flag not persisted: %v", err)
	}

	env = postAPI(t, admin, "/api/workflow-status", map[string]interface{}{"id": "wf-start"})
	data = apiData(t, env, "/api/workflow-status")
	if data["status"] != "running" {
		t.Fatalf("status = %v, want running", data["status"])
	}
}

// TestWorkflowStopStopsEngine proves /api/workflow-stop tears down the
// run for real — not just the persisted flag.
func TestWorkflowStopStopsEngine(t *testing.T) {
	admin, engine, store := newEngineTestAdmin(t)
	queueFlow(t, store, "wf-stop", map[string]interface{}{"hold": true})

	env := postAPI(t, admin, "/api/workflow-start", map[string]interface{}{"id": "wf-stop"})
	apiData(t, env, "/api/workflow-start")

	env = postAPI(t, admin, "/api/workflow-stop", map[string]interface{}{"id": "wf-stop"})
	apiData(t, env, "/api/workflow-stop")

	status, err := engine.GetWorkflowStatus("wf-stop")
	if err != nil || status != "stopped" {
		t.Fatalf("engine status = %q, %v — stop did not run", status, err)
	}

	wf, err := store.WorkflowFindByID(context.Background(), "wf-stop")
	if err != nil || wf.GetDeployed() {
		t.Fatalf("deployed flag not cleared: %v", err)
	}
}

// queueItemCount polls /api/queue-items and returns the decoded payload.
func queueItemCount(t *testing.T, admin Interface, workflowID string) map[string]interface{} {
	env := postAPI(t, admin, "/api/queue-items", map[string]interface{}{
		"workflow_id": workflowID, "node_id": "f",
	})
	return apiData(t, env, "/api/queue-items")
}

// TestNodeInjectStagesIntoQueue proves /api/node-inject fires a real
// traversal: the injected payload lands in the held queue, inspectable
// through /api/queue-items, and the queue node's execution is
// journaled.
func TestNodeInjectStagesIntoQueue(t *testing.T) {
	admin, engine, store := newEngineTestAdmin(t)
	queueFlow(t, store, "wf-inj", map[string]interface{}{"hold": true})

	env := postAPI(t, admin, "/api/workflow-start", map[string]interface{}{"id": "wf-inj"})
	apiData(t, env, "/api/workflow-start")
	t.Cleanup(func() { _ = engine.StopWorkflow("wf-inj") })

	env = postAPI(t, admin, "/api/node-inject", map[string]interface{}{
		"workflow_id": "wf-inj", "node_id": "inj",
		"payload": map[string]interface{}{"data": "job-1"},
	})
	apiData(t, env, "/api/node-inject")

	var data map[string]interface{}
	waitFor(t, "queue item", 3*time.Second, func() bool {
		data = queueItemCount(t, admin, "wf-inj")
		return data["count"] == float64(1)
	})
	if data["hold"] != true {
		t.Fatalf("held queue reports hold=%v", data["hold"])
	}
	items, _ := data["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	item, _ := items[0].(map[string]interface{})
	if item["preview"] == "" {
		t.Fatalf("item missing payload preview: %v", item)
	}

	// The queue node's own execution must be journaled — the run
	// journal returns real engine data, not an empty stub.
	env = postAPI(t, admin, "/api/node-run-journal", map[string]interface{}{
		"workflow_id": "wf-inj", "node_id": "f",
	})
	jdata := apiData(t, env, "/api/node-run-journal")
	entries, _ := jdata["entries"].([]interface{})
	if len(entries) == 0 {
		t.Fatal("run journal returned no entries after a real execution")
	}
}

// TestNodeInjectFireButton proves the payload-less inject path reaches
// FireInjectNode on a real inject node: the node's configured payload
// lands downstream.
func TestNodeInjectFireButton(t *testing.T) {
	admin, engine, store := newEngineTestAdmin(t)

	wf := nodestore.NewWorkflow("wf-fire").SetID("wf-fire").SetNodes([]nodestore.NodeInterface{
		nodestore.NewNode("inj", "inject").SetData(map[string]interface{}{
			"payload":     `"button-fired"`,
			"payloadType": "json",
			"once":        false,
		}),
		nodestore.NewNode("f", "queue").SetData(map[string]interface{}{"hold": true}),
	}).SetEdges([]nodestore.EdgeInterface{
		nodestore.NewEdge("e1", "inj", "f"),
	})
	if err := store.WorkflowCreate(context.Background(), wf); err != nil {
		t.Fatal(err)
	}

	env := postAPI(t, admin, "/api/workflow-start", map[string]interface{}{"id": "wf-fire"})
	apiData(t, env, "/api/workflow-start")
	t.Cleanup(func() { _ = engine.StopWorkflow("wf-fire") })

	env = postAPI(t, admin, "/api/node-inject", map[string]interface{}{
		"workflow_id": "wf-fire", "node_id": "inj",
	})
	apiData(t, env, "/api/node-inject")

	waitFor(t, "fired payload in queue", 3*time.Second, func() bool {
		return queueItemCount(t, admin, "wf-fire")["count"] == float64(1)
	})
}

// TestWorkflowStatusSnapshot proves /api/workflow-status serves the
// editor's polling contract (flowadmin parity): deploy an inject→debug
// flow, fire the inject, and the incremental {nodes, events, last_seq,
// runner_id} snapshot reports the debug node's activity.
func TestWorkflowStatusSnapshot(t *testing.T) {
	admin, engine, store := newEngineTestAdmin(t)

	wf := nodestore.NewWorkflow("wf-snap").SetID("wf-snap").SetNodes([]nodestore.NodeInterface{
		nodestore.NewNode("inj", "inject").SetData(map[string]interface{}{
			"payload":     `"hello-debug"`,
			"payloadType": "json",
			"once":        false,
		}),
		nodestore.NewNode("dbg", "debug").SetData(map[string]interface{}{}),
	}).SetEdges([]nodestore.EdgeInterface{
		nodestore.NewEdge("e1", "inj", "dbg"),
	})
	if err := store.WorkflowCreate(context.Background(), wf); err != nil {
		t.Fatal(err)
	}

	env := postAPI(t, admin, "/api/workflow-start", map[string]interface{}{"id": "wf-snap"})
	apiData(t, env, "/api/workflow-start")
	t.Cleanup(func() { _ = engine.StopWorkflow("wf-snap") })

	env = postAPI(t, admin, "/api/node-inject", map[string]interface{}{
		"workflow_id": "wf-snap", "node_id": "inj",
	})
	apiData(t, env, "/api/node-inject")

	// Poll until the snapshot reports the debug node's activity —
	// traversal is asynchronous.
	var data map[string]interface{}
	waitFor(t, "debug event in workflow-status", 3*time.Second, func() bool {
		env := postAPI(t, admin, "/api/workflow-status", map[string]interface{}{
			"id": "wf-snap", "since": 0,
		})
		data = apiData(t, env, "/api/workflow-status")
		if data == nil {
			return false
		}
		events, _ := data["events"].([]interface{})
		for _, raw := range events {
			if ev, _ := raw.(map[string]interface{}); ev["node_id"] == "dbg" {
				return true
			}
		}
		return false
	})

	if data["status"] != "running" {
		t.Fatalf("status = %v, want running", data["status"])
	}
	if data["deployed"] != true {
		t.Fatalf("deployed = %v, want true", data["deployed"])
	}
	lastSeq, _ := data["last_seq"].(float64)
	if lastSeq <= 0 {
		t.Fatalf("last_seq = %v, want > 0", data["last_seq"])
	}
	if _, ok := data["runner_id"].(string); !ok {
		t.Fatalf("runner_id missing or wrong type: %v", data)
	}
	if nodesMap, _ := data["nodes"].(map[string]interface{}); len(nodesMap) == 0 {
		t.Fatalf("nodes map empty: %v", data)
	}

	// Incremental polling: re-issuing with since=last_seq returns no
	// new events — the contract the editor's pollStatus relies on.
	env = postAPI(t, admin, "/api/workflow-status", map[string]interface{}{
		"id": "wf-snap", "since": int64(lastSeq),
	})
	fresh := apiData(t, env, "/api/workflow-status")
	if events, _ := fresh["events"].([]interface{}); len(events) != 0 {
		t.Fatalf("since=last_seq returned %d events, want 0", len(events))
	}
}

// TestQueueHoldDeleteRequeue exercises all four queue endpoints against
// the live queue: list, hold toggle, per-item delete and requeue.
func TestQueueHoldDeleteRequeue(t *testing.T) {
	admin, engine, store := newEngineTestAdmin(t)
	queueFlow(t, store, "wf-q", map[string]interface{}{"hold": true})

	env := postAPI(t, admin, "/api/workflow-start", map[string]interface{}{"id": "wf-q"})
	apiData(t, env, "/api/workflow-start")
	t.Cleanup(func() { _ = engine.StopWorkflow("wf-q") })

	inject := func(data string) {
		env := postAPI(t, admin, "/api/node-inject", map[string]interface{}{
			"workflow_id": "wf-q", "node_id": "inj",
			"payload": map[string]interface{}{"data": data},
		})
		apiData(t, env, "/api/node-inject")
	}

	// Hold is already set from node data; toggling it through the API
	// flips the live gate. Do this while the queue is still empty —
	// releasing the gate with items staged would drain them before
	// the hold is re-applied.
	env = postAPI(t, admin, "/api/queue-hold", map[string]interface{}{
		"workflow_id": "wf-q", "node_id": "f", "hold": false,
	})
	if apiData(t, env, "/api/queue-hold")["hold"] != false {
		t.Fatal("queue-hold did not release the gate")
	}
	env = postAPI(t, admin, "/api/queue-hold", map[string]interface{}{
		"workflow_id": "wf-q", "node_id": "f", "hold": true,
	})
	if apiData(t, env, "/api/queue-hold")["hold"] != true {
		t.Fatal("queue-hold did not set the gate")
	}

	// Inject sequentially: each traversal runs on its own goroutine,
	// so wait for each payload to stage before sending the next —
	// deterministic arrival order, same discipline as the engine's
	// own queue tests.
	var data map[string]interface{}
	inject("one")
	waitFor(t, "first queued item", 3*time.Second, func() bool {
		data = queueItemCount(t, admin, "wf-q")
		return data["count"] == float64(1)
	})
	inject("two")
	waitFor(t, "two queued items", 3*time.Second, func() bool {
		data = queueItemCount(t, admin, "wf-q")
		return data["count"] == float64(2)
	})
	items, _ := data["items"].([]interface{})
	seq0 := int64(items[0].(map[string]interface{})["seq"].(float64))
	seq1 := int64(items[1].(map[string]interface{})["seq"].(float64))

	// Delete drops one item for real.
	env = postAPI(t, admin, "/api/queue-item-delete", map[string]interface{}{
		"workflow_id": "wf-q", "node_id": "f", "seq": seq0,
	})
	if apiData(t, env, "/api/queue-item-delete")["count"] != float64(1) {
		t.Fatalf("queue-item-delete count = %v", data)
	}

	// Requeue releases the remaining item downstream — the debug
	// node's journal entry proves real delivery.
	env = postAPI(t, admin, "/api/queue-item-requeue", map[string]interface{}{
		"workflow_id": "wf-q", "node_id": "f", "seq": seq1,
	})
	apiData(t, env, "/api/queue-item-requeue")

	waitFor(t, "dbg journal entry", 3*time.Second, func() bool {
		env := postAPI(t, admin, "/api/node-run-journal", map[string]interface{}{
			"workflow_id": "wf-q", "node_id": "dbg",
		})
		jdata := apiData(t, env, "/api/node-run-journal")
		entries, _ := jdata["entries"].([]interface{})
		return len(entries) > 0
	})
}

// TestQueueEndpointsNotDeployed proves the queue endpoints report a
// clean error (not a stub) when the workflow is not running.
func TestQueueEndpointsNotDeployed(t *testing.T) {
	admin, _, store := newEngineTestAdmin(t)
	queueFlow(t, store, "wf-cold", map[string]interface{}{"hold": true})

	for _, path := range []string{
		"/api/queue-items", "/api/queue-hold",
		"/api/queue-item-delete", "/api/queue-item-requeue",
		"/api/node-run-journal",
	} {
		env := postAPI(t, admin, path, map[string]interface{}{
			"workflow_id": "wf-cold", "node_id": "f",
		})
		if env["status"] == "success" {
			t.Fatalf("%s succeeded for a non-running workflow", path)
		}
	}
}

// TestWorkflowUpdatePreservesDeployed proves a plain editor save
// (which carries no deployed key) cannot silently clear the persisted
// deployed flag — the flag is server-managed.
func TestWorkflowUpdatePreservesDeployed(t *testing.T) {
	admin, _, store := newEngineTestAdmin(t)
	queueFlow(t, store, "wf-save", map[string]interface{}{"hold": true})

	postAPI(t, admin, "/api/workflow-start", map[string]interface{}{"id": "wf-save"})

	env := postAPI(t, admin, "/api/workflow-update", map[string]interface{}{
		"id":   "wf-save",
		"name": "wf-save",
		"nodes": []map[string]interface{}{
			{"id": "n1", "type": "queue", "data": map[string]interface{}{"hold": true}},
		},
	})
	apiData(t, env, "/api/workflow-update")

	wf, err := store.WorkflowFindByID(context.Background(), "wf-save")
	if err != nil || !wf.GetDeployed() {
		t.Fatalf("deployed flag wiped by editor save: %v", err)
	}
}
