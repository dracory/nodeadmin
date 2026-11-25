//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDeployRestartResume covers scenario (c) — the deployed-flag
// regression: a deployed workflow must come back running after the
// process restarts on the same database.
//
// The flow is httpTrigger (listener "main") -> httpResponse: the
// trigger is a long-lived stopper, so "running" is only true while
// its route is actually mounted — both before and after the restart.
func TestDeployRestartResume(t *testing.T) {
	dbPath := t.TempDir() + "/e2e-deploy.db"

	srv := newTestServer(t, dbPath)
	wfID := createWorkflow(t, srv.srv.URL, "E2E Deploy", []map[string]any{
		{
			"id":       "trig",
			"type":     "httpTrigger",
			"position": map[string]any{"x": 100, "y": 100},
			"data":     map[string]any{"method": "GET", "path": "/e2e-hook", "listener": "main"},
		},
		{
			"id":       "resp",
			"type":     "httpResponse",
			"position": map[string]any{"x": 300, "y": 100},
			"data":     map[string]any{},
		},
	}, []map[string]any{
		{"id": "e1", "source": "trig", "target": "resp"},
	})

	// Deploy through the same endpoint the editor's rocket button hits.
	apiPost(t, srv.srv.URL, "/api/workflow-start", map[string]any{"id": wfID, "mode": "full"})
	require.Equal(t, "running", workflowStatus(t, srv.srv.URL, wfID))

	// The shared listener serves on the admin handler.
	resp, err := http.Get(srv.url("/e2e-hook"))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Simulate a restart: listener + DB handle down, a fresh nodeadmin
	// boots on the same database file. nodeadmin.New replays
	// ResumeDeployed in the background, so poll for it.
	srv.close()
	srv = newTestServer(t, dbPath)
	defer srv.close()

	eventually(t, 15*time.Second, "workflow to resume running after restart", func() bool {
		return workflowStatus(t, srv.srv.URL, wfID) == "running"
	})

	// And the listener came back with it — the deployed flag is a
	// semantic, the route actually serves again.
	resp, err = http.Get(srv.url("/e2e-hook"))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// The restarted flow is also visible through the browser surface:
	// the editor's explorer lists the persisted workflow.
	browser := newBrowser(t)
	page, _ := mustPage(t, browser, srv.browserURL("/?controller=editor"))
	waitVueApp(t, page)
	// The workspace stays hidden until a workflow is opened — the
	// welcome screen is the "editor mounted" signal.
	waitElement(t, page, ".editor-welcome")
	waitElementText(t, page, ".flow-item", "E2E Deploy")
}
