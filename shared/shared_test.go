package shared

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dracory/entitystore"
	_ "modernc.org/sqlite"

	"github.com/dracory/nodestore"
)

// Rendering-invariant tests for the shared chrome: Navbar/SectionNav
// active state, count badges and the bootlegance theme-switcher
// attributes (task admin_layer_test_coverage).

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

// TestNavbarEmitsNavElement guards the element-name regression: the
// component must emit a real <nav> tag, not a bogus <navbar>.
func TestNavbarEmitsNavElement(t *testing.T) {
	r := httptest.NewRequest("GET", "/nodes", nil)
	html := Navbar(r, nil).ToHTML()
	if strings.Contains(html, "<navbar") {
		t.Fatalf("emitted <navbar>: %s", html)
	}
	if !strings.Contains(html, "<nav ") {
		t.Fatalf("missing <nav> element: %s", html)
	}
	if !strings.Contains(html, "navbar-brand") {
		t.Fatalf("missing brand link: %s", html)
	}
}

// TestNavbarActiveState proves the active link follows the
// ?controller= query parameter, defaulting to home.
func TestNavbarActiveState(t *testing.T) {
	for _, tc := range []struct {
		name       string
		target     string
		wantActive string // href fragment whose link carries .active
	}{
		{"default is home", "/nodes", "?controller=home"},
		{"workflows", "/nodes?controller=workflows", "?controller=workflows"},
		{"editor", "/nodes?controller=editor", "?controller=editor"},
		{"settings", "/nodes?controller=settings", "?controller=settings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", tc.target, nil)
			html := Navbar(r, nil).ToHTML()
			// The active class is applied to the link pointing at the
			// active controller URL (hb emits class before href).
			needle := `class="nav-link active" href="` + "/nodes" + tc.wantActive + `"`
			if !strings.Contains(html, "nav-link active") {
				t.Fatalf("no active link in %s", html)
			}
			if !strings.Contains(html, needle) {
				t.Fatalf("active link not on %s: %s", tc.wantActive, html)
			}
		})
	}
}

// TestNavbarCountBadges proves Workflows and Messages links carry the
// store counts as badges, and that a nil store renders badge-free.
func TestNavbarCountBadges(t *testing.T) {
	store := newTestStore(t)
	if err := store.WorkflowCreate(context.Background(), nodestore.NewWorkflow("a")); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkflowCreate(context.Background(), nodestore.NewWorkflow("b")); err != nil {
		t.Fatal(err)
	}
	if err := store.MessageCreate(context.Background(), nodestore.NewMessage()); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/nodes", nil)
	html := Navbar(r, store).ToHTML()
	if !strings.Contains(html, `badge bg-secondary ms-2">2`) {
		t.Fatalf("workflow badge missing: %s", html)
	}
	if !strings.Contains(html, `badge bg-secondary ms-2">1`) {
		t.Fatalf("message badge missing: %s", html)
	}

	badgeFree := Navbar(r, nil).ToHTML()
	if strings.Contains(badgeFree, "badge bg-secondary") {
		t.Fatalf("nil store must render badge-free: %s", badgeFree)
	}
}

// TestNavbarBootleganceAttributes proves the theme switcher keeps the
// data-bootlegance-* hooks bootlegance.min.js wires at DOMContentLoaded.
func TestNavbarBootleganceAttributes(t *testing.T) {
	r := httptest.NewRequest("GET", "/nodes", nil)
	html := Navbar(r, nil).ToHTML()
	for _, attr := range []string{
		`data-bootlegance-mode="light"`,
		`data-bootlegance-mode="dark"`,
		`data-bootlegance-mode="auto"`,
		`data-bootlegance-picker`,
		`data-bootlegance-font-size="decrease"`,
		`data-bootlegance-font-size="increase"`,
		`data-bootlegance-font-size="reset"`,
		`data-bootlegance-font-size-value`,
	} {
		if !strings.Contains(html, attr) {
			t.Fatalf("missing %s in %s", attr, html)
		}
	}
}

// TestSectionNav proves the pill nav shares the nav-items model:
// active state from ?controller=, badges, and the section label.
func TestSectionNav(t *testing.T) {
	r := httptest.NewRequest("GET", "/nodes?controller=messages", nil)
	html := SectionNav(r, nil).ToHTML()
	if !strings.Contains(html, "nav nav-pills") {
		t.Fatalf("missing pills container: %s", html)
	}
	if !strings.Contains(html, `class="nav-link active" href="/nodes?controller=messages"`) {
		t.Fatalf("active tab not on messages: %s", html)
	}
	if !strings.Contains(html, "Nodes") {
		t.Fatalf("missing section label: %s", html)
	}

	store := newTestStore(t)
	_ = store.WorkflowCreate(context.Background(), nodestore.NewWorkflow("a"))
	html = SectionNav(r, store).ToHTML()
	if !strings.Contains(html, `badge bg-secondary ms-2">1`) {
		t.Fatalf("badge missing: %s", html)
	}
}

// TestNodeAdminURL covers the context-override fallback chain.
func TestNodeAdminURL(t *testing.T) {
	if got := NodeAdminURL(nil); got != "/nodes" {
		t.Fatalf("nil request = %q", got)
	}
	r := httptest.NewRequest("GET", "/x", nil)
	if got := NodeAdminURL(r); got != "/nodes" {
		t.Fatalf("no context = %q", got)
	}
	r = r.WithContext(context.WithValue(r.Context(), KeyNodeAdminURL, "/custom"))
	if got := NodeAdminURL(r); got != "/custom" {
		t.Fatalf("context override = %q", got)
	}
}

// TestLayout proves the default layout emits the bootlegance script,
// the navbar and the page body, in that order.
func TestLayout(t *testing.T) {
	r := httptest.NewRequest("GET", "/nodes", nil)
	page := Layout(nil, r, "Title", "<p>BODY</p>", LayoutOptions{
		Scripts: []string{"window.__x = 1;"},
	})
	for _, needle := range []string{
		"bootlegance.min.js",
		"data-bootlegance",
		"<nav ",
		"<p>BODY</p>",
		"window.__x = 1;",
	} {
		if !strings.Contains(page, needle) {
			t.Fatalf("layout missing %q", needle)
		}
	}
	navIdx := strings.Index(page, "<nav ")
	bodyIdx := strings.Index(page, "<p>BODY</p>")
	if !(navIdx >= 0 && navIdx < bodyIdx) {
		t.Fatalf("navbar must precede body: nav=%d body=%d", navIdx, bodyIdx)
	}
}
