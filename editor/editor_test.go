package editor

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dracory/entitystore"
	_ "modernc.org/sqlite"

	"github.com/dracory/nodeadmin/shared"
	"github.com/dracory/nodestore"
)

// newTestStore builds an in-memory nodestore.
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

// TestEditorPageChrome guards the composition invariants the editor
// relies on: the server-rendered navbar sits ABOVE #app (Vue
// re-renders #app on mount, so the navbar inside it would lose the
// listeners bootlegance wires up), the retired app-navigation
// component and its script are gone, and editor_app.js is served via
// the ?action=appjs rewrite.
func TestEditorPageChrome(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?controller=editor", nil)
	rec := httptest.NewRecorder()
	Editor(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	page := rec.Body.String()

	navIdx := strings.Index(page, "<nav ")
	appIdx := strings.Index(page, `<div id="app">`)
	if navIdx < 0 || appIdx < 0 {
		t.Fatalf("navbar or #app missing: nav=%d app=%d", navIdx, appIdx)
	}
	if navIdx > appIdx {
		t.Fatalf("navbar must sit above #app (nav=%d app=%d) — Vue drops it on mount otherwise", navIdx, appIdx)
	}
	if strings.Contains(page, "<navbar") {
		t.Fatal("emitted <navbar> — must be a real <nav>")
	}
	if strings.Contains(page, "app-navigation") {
		t.Fatal("retired app-navigation component left in page")
	}
	if strings.Contains(page, "/js/components/navigation.js") {
		t.Fatal("retired navigation.js script left in page")
	}
	if !strings.Contains(page, `src="?controller=editor&action=appjs"`) {
		t.Fatal("editor_app.js script URL not rewritten")
	}
	if !strings.Contains(page, "nav nav-pills") {
		t.Fatal("section nav pills missing")
	}
}

// TestEditorChromeDisabled proves chrome=false emits the standalone
// page bare — no nodeadmin <nav>, no section pills — for hosts that
// own the outer chrome (Options.DisableChrome).
func TestEditorChromeDisabled(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?controller=editor", nil)
	rec := httptest.NewRecorder()
	New(nil, false).Editor(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	page := rec.Body.String()

	if strings.Contains(page, "<nav ") {
		t.Fatal("chrome=false must not inject nodeadmin <nav>")
	}
	if strings.Contains(page, "nav nav-pills") {
		t.Fatal("chrome=false must not inject section nav pills")
	}
	if strings.Contains(page, "app-navigation") {
		t.Fatal("retired app-navigation component left in page")
	}
	if !strings.Contains(page, `<div id="app">`) {
		t.Fatal("#app missing — editor page broken")
	}
	if !strings.Contains(page, `src="?controller=editor&action=appjs"`) {
		t.Fatal("editor_app.js script URL not rewritten")
	}
}

// TestEditorBadgesWithStore proves the injected chrome renders
// store-backed count badges (previously the editor passed a nil
// store, so Workflows/Messages never showed counts).
func TestEditorBadgesWithStore(t *testing.T) {
	store := newTestStore(t)
	if err := store.WorkflowCreate(context.Background(), nodestore.NewWorkflow("flow-a")); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/?controller=editor", nil)
	rec := httptest.NewRecorder()
	New(store, true).Editor(rec, req)

	page := rec.Body.String()
	if !strings.Contains(page, "badge bg-secondary") {
		t.Fatal("store-backed count badges missing from editor chrome")
	}
}

// TestEditorAppJS proves the app script is served as JavaScript with
// its /api/ fetches rewritten onto the nodeadmin API mount.
func TestEditorAppJS(t *testing.T) {
	for _, tc := range []struct {
		name        string
		ctxURL      string
		wantAPIBase string
	}{
		{"default mount", "", "/nodes/api/"},
		{"custom mount", "/admin/nodes/", "/admin/nodes/api/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/?controller=editor&action=appjs", nil)
			if tc.ctxURL != "" {
				req = req.WithContext(context.WithValue(req.Context(), shared.KeyNodeAdminURL, tc.ctxURL))
			}
			rec := httptest.NewRecorder()
			Editor(rec, req)

			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
				t.Fatalf("content-type = %q", ct)
			}
			js := rec.Body.String()
			if !strings.Contains(js, "'"+tc.wantAPIBase) && !strings.Contains(js, `"`+tc.wantAPIBase) {
				t.Fatalf("api base %q not rewritten in script", tc.wantAPIBase)
			}
		})
	}
}
