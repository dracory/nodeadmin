package nodeadmin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dracory/nodestore"
)

// triggerFlow builds httpTrigger(listener "main") -> httpResponse and
// deploys it on the engine — the request is answered by the flow, not
// the trigger.
func deployTriggerFlow(t *testing.T, admin Interface, store nodestore.StoreInterface, wfID, path string, basicAuth ...string) {
	t.Helper()
	wf := nodestore.NewWorkflow(wfID).SetID(wfID).SetNodes([]nodestore.NodeInterface{
		nodestore.NewNode("trig", "httpTrigger").SetData(map[string]interface{}{
			"method":   "GET",
			"path":     path,
			"listener": "main",
		}),
		nodestore.NewNode("resp", "httpResponse").SetData(map[string]interface{}{}),
	}).SetEdges([]nodestore.EdgeInterface{
		nodestore.NewEdge("e1", "trig", "resp"),
	})
	if err := store.WorkflowCreate(context.Background(), wf); err != nil {
		t.Fatalf("workflow create: %v", err)
	}
	raw, _ := json.Marshal(map[string]interface{}{"id": wfID})
	req := httptest.NewRequest(http.MethodPost, "/api/workflow-start", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	if len(basicAuth) == 2 {
		req.SetBasicAuth(basicAuth[0], basicAuth[1])
	}
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, req)
	var env map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env["status"] != "success" {
		t.Fatalf("workflow-start failed: %v body=%s", err, rec.Body.String())
	}
	t.Cleanup(func() {
		stopRaw, _ := json.Marshal(map[string]interface{}{"id": wfID})
		stopReq := httptest.NewRequest(http.MethodPost, "/api/workflow-stop", strings.NewReader(string(stopRaw)))
		stopReq.Header.Set("Content-Type", "application/json")
		if len(basicAuth) == 2 {
			stopReq.SetBasicAuth(basicAuth[0], basicAuth[1])
		}
		admin.ServeHTTP(httptest.NewRecorder(), stopReq)
	})
}

// TestTriggerRoundtrip proves the P2 seam end to end: nodeadmin wires
// its endpointRouter onto the concrete engine at mount, the
// httpTrigger "main" node mounts its route on it, and a request
// through the admin handler is answered by the flow's httpResponse.
func TestTriggerRoundtrip(t *testing.T) {
	admin, _, store := newEngineTestAdmin(t)
	deployTriggerFlow(t, admin, store, "wf-listener", "/hook/:id")

	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hook/42", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("trigger roundtrip failed: %d body=%s", rec.Code, rec.Body.String())
	}
	var data map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatalf("response is not JSON: %v body=%s", err, rec.Body.String())
	}
	// The trigger injects the request object; params proves manual
	// dispatch populated PathValue for the ":id" wildcard.
	params, _ := data["params"].(map[string]interface{})
	if params["id"] != "42" {
		t.Fatalf("wildcard param not delivered, body=%s", rec.Body.String())
	}
}

// TestTriggerBehindAuth proves listener "main" routes serve inside the
// admin's auth middleware — an auth-protected admin protects node
// endpoints (parity with flowadmin).
func TestTriggerBehindAuth(t *testing.T) {
	store := newTestStore(t)
	authFile := writeAuthFile(t, "s3cret")
	admin, err := New(Options{Store: store, AuthFile: authFile})
	if err != nil {
		t.Fatal(err)
	}
	deployTriggerFlow(t, admin, store, "wf-listener-auth", "/hook", "admin", "s3cret")

	// Unauthenticated → the 401 challenge, before route dispatch.
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hook", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("node route must sit behind admin auth, got %d", rec.Code)
	}

	// Authenticated → the flow answers.
	req := httptest.NewRequest(http.MethodGet, "/hook", nil)
	req.SetBasicAuth("admin", "s3cret")
	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated trigger request failed: %d", rec.Code)
	}
}
