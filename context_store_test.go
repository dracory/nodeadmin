package nodeadmin

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dracory/entitystore"
	"github.com/dracory/nodebase/function"
	"github.com/dracory/noderun"
	"github.com/dracory/nodestore"
	_ "modernc.org/sqlite"
)

type persistentContextTestStack struct {
	store  nodestore.StoreInterface
	engine *noderunner.WorkflowEngine
	admin  Interface
	db     *sql.DB
}

func newPersistentContextTestStack(t *testing.T, dbPath string) *persistentContextTestStack {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	// SQLite permits one writer; the engine, inbox store, entity store and
	// context store all share this handle in the standard DB-backed path.
	db.SetMaxOpenConns(1)
	entityStore, err := entitystore.NewStore(entitystore.NewStoreOptions{
		DB:                 db,
		EntityTableName:    "node_entity",
		AttributeTableName: "node_attribute",
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
	admin, err := New(Options{Store: store, Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	return &persistentContextTestStack{store: store, engine: engine, admin: admin, db: db}
}

func requirePersistentContextStore(t *testing.T, engine *noderunner.WorkflowEngine) {
	t.Helper()
	if engine.ContextStore == nil {
		t.Skip("persistent context store capability is not present in the tagged module dependencies yet")
	}
}

func runContextFunction(t *testing.T, engine *noderunner.WorkflowEngine, workflowID, nodeID, script string) interface{} {
	t.Helper()
	node := nodestore.NewNode(nodeID, "function").SetWorkflowID(workflowID)
	fn := function.NewFunctionNode(node, &function.FunctionConfig{Func: script}, engine)
	out, err := fn.Execute(context.Background(), nodestore.NewMessage().SetPayload(map[string]interface{}{}))
	if err != nil {
		t.Fatalf("function execute: %v", err)
	}
	return out.GetPayload()
}

func TestPersistentContextSurvivesReopenInStandardPath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "node.db")

	first := newPersistentContextTestStack(t, dbPath)
	requirePersistentContextStore(t, first.engine)
	runContextFunction(t, first.engine, "wf-restart", "n1",
		"context.set('nodeValue', 'node-survived');"+
			"flow.set('flowValue', 'flow-survived');"+
			"global.set('globalValue', 'global-survived'); return msg;")
	if err := first.db.Close(); err != nil {
		t.Fatalf("close first db: %v", err)
	}

	second := newPersistentContextTestStack(t, dbPath)
	defer func() { _ = second.db.Close() }()
	requirePersistentContextStore(t, second.engine)
	payload, ok := runContextFunction(t, second.engine, "wf-restart", "n1",
		"msg.payload = {nodeValue: context.get('nodeValue'), flowValue: flow.get('flowValue'), globalValue: global.get('globalValue')}; return msg;").(map[string]interface{})
	if !ok {
		t.Fatal("function did not return an object payload")
	}
	if payload["nodeValue"] != "node-survived" || payload["flowValue"] != "flow-survived" || payload["globalValue"] != "global-survived" {
		t.Fatalf("context did not survive reopen: %#v", payload)
	}
}

func TestPersistentContextScopeIsolationInStandardPath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "node.db")
	stack := newPersistentContextTestStack(t, dbPath)
	defer func() { _ = stack.db.Close() }()
	requirePersistentContextStore(t, stack.engine)

	runContextFunction(t, stack.engine, "wf-a", "n1",
		"flow.set('secret', 'flow-a'); context.set('mine', 'node-a'); global.set('shared', 'global-value'); return msg;")
	payload, ok := runContextFunction(t, stack.engine, "wf-b", "n2",
		"msg.payload = {flowLeak: typeof flow.get('secret'), nodeLeak: typeof context.get('mine'), globalValue: global.get('shared')}; return msg;").(map[string]interface{})
	if !ok {
		t.Fatal("function did not return an object payload")
	}
	if payload["flowLeak"] != "undefined" || payload["nodeLeak"] != "undefined" {
		t.Fatalf("flow/node context leaked across workflows: %#v", payload)
	}
	if payload["globalValue"] != "global-value" {
		t.Fatalf("global context not shared: %#v", payload)
	}
}

func TestWorkflowDeleteCleansPersistentContextInStandardPath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "node.db")
	stack := newPersistentContextTestStack(t, dbPath)
	defer func() { _ = stack.db.Close() }()
	requirePersistentContextStore(t, stack.engine)

	workflow := nodestore.NewWorkflow("delete context").SetID("wf-delete")
	if err := stack.store.WorkflowCreate(context.Background(), workflow); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	runContextFunction(t, stack.engine, "wf-delete", "n1",
		"flow.set('flowKey', 'flow-value'); context.set('nodeKey', 'node-value'); global.set('globalKey', 'global-value'); return msg;")

	for _, tc := range []struct{ scope, scopeID, key string }{
		{"flow", "wf-delete", "flowKey"},
		{"node", "wf-delete/n1", "nodeKey"},
		{"global", "", "globalKey"},
	} {
		if _, ok, err := stack.engine.ContextStore.GetContext(tc.scope, tc.scopeID, tc.key); err != nil || !ok {
			t.Fatalf("seeded %s context missing: ok=%v err=%v", tc.scope, ok, err)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/workflow-delete", strings.NewReader(`{"id":"wf-delete"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	stack.admin.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"success"`) {
		t.Fatalf("workflow delete failed: status=%d body=%s", rec.Code, rec.Body.String())
	}

	for _, tc := range []struct{ scope, scopeID, key string }{
		{"flow", "wf-delete", "flowKey"},
		{"node", "wf-delete/n1", "nodeKey"},
	} {
		if _, ok, err := stack.engine.ContextStore.GetContext(tc.scope, tc.scopeID, tc.key); err != nil || ok {
			t.Fatalf("%s context survived workflow delete: ok=%v err=%v", tc.scope, ok, err)
		}
	}
	value, ok, err := stack.engine.ContextStore.GetContext("global", "", "globalKey")
	if err != nil || !ok || value != "global-value" {
		t.Fatalf("global context was deleted or corrupted: value=%#v ok=%v err=%v", value, ok, err)
	}
}
