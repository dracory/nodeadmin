package nodeadmin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dracory/nodestore"
)

// dashboardWidgetFlow builds a workflow carrying one uiText and one
// uiButton widget plus a non-widget node — the shape the dashboard
// hub collects into a manifest on activation.
func dashboardWidgetFlow(id string) nodestore.WorkflowInterface {
	return nodestore.NewWorkflow(id).SetID(id).SetNodes([]nodestore.NodeInterface{
		nodestore.NewNode("t1", "uiText").SetData(map[string]interface{}{
			"page": "main", "label": "Readout",
		}),
		nodestore.NewNode("b1", "uiButton").SetData(map[string]interface{}{
			"page": "main", "payload": "go",
		}),
		nodestore.NewNode("d1", "debug").SetData(map[string]interface{}{}),
	})
}

// TestDashboardPageServed: the client shell is served for the mount
// root and for deep links under it.
func TestDashboardPageServed(t *testing.T) {
	admin := initTestAdmin(t)
	for _, path := range []string{"/dashboard/", "/dashboard/anything/deep"} {
		rec := httptest.NewRecorder()
		admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Fatalf("%s: content type %q", path, ct)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "GoFlow Dashboard") || !strings.Contains(body, "goflowDashboardSend") {
			t.Fatalf("%s: dashboard shell missing", path)
		}
	}
}

// TestDashboardWsEndpointExists: the ws endpoint is routed to the
// hub (a plain GET fails the upgrade inside the hub rather than
// falling through to the page or the admin catch-all).
func TestDashboardWsEndpointExists(t *testing.T) {
	admin := initTestAdmin(t)
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/ws", nil))
	if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "GoFlow Dashboard") {
		t.Fatal("ws path served the page shell")
	}
	if strings.Contains(rec.Body.String(), "Dashboard") && rec.Code == http.StatusOK {
		t.Fatal("ws path fell through to the admin UI")
	}
}

// TestDashboardManifestEndpoint: activating a ui* workflow produces
// a JSON manifest through the mounted hub.
func TestDashboardManifestEndpoint(t *testing.T) {
	iface := initTestAdmin(t)
	a, ok := iface.(*admin)
	if !ok || a.dashboard == nil {
		t.Skip("dashboard hub unavailable on this engine")
	}
	a.dashboard.WorkflowActivated(dashboardWidgetFlow("wf1"))

	rec := httptest.NewRecorder()
	admin := iface
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/manifest?flow=wf1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest status %d: %s", rec.Code, rec.Body.String())
	}
	var m struct {
		Workflow string `json:"workflow"`
		Widgets  []struct {
			ID     string                 `json:"id"`
			Type   string                 `json:"type"`
			Label  string                 `json:"label"`
			Config map[string]interface{} `json:"config"`
		} `json:"widgets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest not JSON: %v", err)
	}
	if m.Workflow != "wf1" || len(m.Widgets) != 2 {
		t.Fatalf("manifest wrong: %+v", m)
	}
}
