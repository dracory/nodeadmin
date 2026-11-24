package editor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dracory/nodeadmin/shared"
)

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
