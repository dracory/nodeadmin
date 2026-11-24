package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// getAPI issues a GET against the API and returns the decoded
// gouniverse/api envelope plus the recorder — the read-side twin of
// postAPI for the pending-count endpoint.
func getAPI(t *testing.T, handler http.Handler, path string) (map[string]interface{}, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var env map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("envelope decode failed for %s: %v (body: %s)", path, err, rec.Body.String())
	}
	return env, rec
}

// TestWorkflowPending covers GET /api/workflows/{id}/pending: the
// {nodeId: count} map built from node_inbox_messages rows in the
// pending/claimed lifecycle (NULL counts as pending, matching the
// drain's claimable predicate).
func TestWorkflowPending(t *testing.T) {
	store := newTestStore(t)
	a := New(store, newFakeEngine())
	seedWorkflow(t, store, "wf-1", "flow")

	// Before any durable inbox exists the endpoint is a cheap no-op:
	// an empty map, not an error.
	env, _ := getAPI(t, a, "/api/workflows/wf-1/pending")
	if data := apiData(t, env, "pending (no table)"); len(data) != 0 {
		t.Fatalf("expected empty counts, got %v", data)
	}

	db := store.GetEntityStore().GetDB()
	if db == nil {
		t.Fatal("test store has no sql.DB")
	}
	_, err := db.Exec(`CREATE TABLE node_inbox_messages (
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		workflow_id TEXT, node_id TEXT, payload TEXT, status TEXT,
		claimed_by TEXT, claimed_at TEXT, claim_expires_at TEXT, enqueued_at TEXT)`)
	if err != nil {
		t.Fatalf("create inbox table: %v", err)
	}
	rows := []struct{ wf, node, status string }{
		{"wf-1", "n1", "pending"},
		{"wf-1", "n1", "pending"},
		{"wf-1", "n2", "claimed"},
		{"wf-1", "n3", ""},        // NULL status counts as pending
		{"wf-2", "n1", "pending"}, // other workflow — excluded
	}
	for _, row := range rows {
		var status interface{}
		if row.status != "" {
			status = row.status
		}
		if _, err := db.Exec(
			"INSERT INTO node_inbox_messages (workflow_id, node_id, payload, status) VALUES (?, ?, '{}', ?)",
			row.wf, row.node, status); err != nil {
			t.Fatalf("insert inbox row: %v", err)
		}
	}

	env, _ = getAPI(t, a, "/api/workflows/wf-1/pending")
	data := apiData(t, env, "pending")
	want := map[string]float64{"n1": 2, "n2": 1, "n3": 1}
	if len(data) != len(want) {
		t.Fatalf("counts = %v, want %v", data, want)
	}
	for nodeID, n := range want {
		if data[nodeID] != n {
			t.Fatalf("counts[%s] = %v, want %v (all: %v)", nodeID, data[nodeID], n, data)
		}
	}
}

// TestWorkflowPendingUnknown covers the not-found envelope.
func TestWorkflowPendingUnknown(t *testing.T) {
	a := New(newTestStore(t), newFakeEngine())
	env, _ := getAPI(t, a, "/api/workflows/wf-missing/pending")
	if msg := apiError(t, env, "pending unknown"); msg == "" {
		t.Fatal("expected a not-found message")
	}
}
