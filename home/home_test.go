package home

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

// testConfig returns a UiConfig whose layout writes the fragment to
// the response (mirroring nodeadmin's render wrapper) and records the
// LayoutOptions it was handed so tests can assert on emitted assets.
func testConfig(store nodestore.StoreInterface) (shared.UiConfig, *shared.LayoutOptions) {
	captured := &shared.LayoutOptions{}
	return shared.UiConfig{
		Store: store,
		Layout: func(w http.ResponseWriter, r *http.Request, title, html string, options shared.LayoutOptions) string {
			*captured = options
			_, _ = w.Write([]byte(html))
			return ""
		},
	}, captured
}

// TestHome renders the dashboard section: stat counts, the workflow
// rows with deployed/draft badges, and the shared section nav.
func TestHome(t *testing.T) {
	store := newTestStore(t)
	wf := nodestore.NewWorkflow("flow-a").SetNodes([]nodestore.NodeInterface{
		nodestore.NewNode("n1", "junction").SetData(map[string]interface{}{}),
	}).SetDeployed(true)
	if err := store.WorkflowCreate(context.Background(), wf); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkflowCreate(context.Background(), nodestore.NewWorkflow("flow-b")); err != nil {
		t.Fatal(err)
	}
	if err := store.MessageCreate(context.Background(), nodestore.NewMessage()); err != nil {
		t.Fatal(err)
	}

	cfg, _ := testConfig(store)
	section := UI(cfg)
	req := httptest.NewRequest(http.MethodGet, "/?controller=home", nil)
	rec := httptest.NewRecorder()
	section.Home(rec, req)

	page := rec.Body.String()
	for _, needle := range []string{
		"na-quicknav",      // section nav injected
		"flow-a", "flow-b", // workflow rows
		"deployed", "draft", // status pills
		`controller=editor`, // edit links
	} {
		if !strings.Contains(page, needle) {
			t.Fatalf("page missing %q\n%s", needle, page)
		}
	}
	// Template placeholders must all be substituted.
	if strings.Contains(page, "{{") {
		t.Fatalf("unsubstituted placeholder in page: %s", page)
	}
}

// TestHomeNoVueMountScript is a regression guard for the
// "Vue is not defined" console error: the dashboard never loads Vue,
// so it must not emit a Vue-dependent mount script either.
func TestHomeNoVueMountScript(t *testing.T) {
	cfg, opts := testConfig(newTestStore(t))
	section := UI(cfg)
	req := httptest.NewRequest(http.MethodGet, "/?controller=home", nil)
	rec := httptest.NewRecorder()
	section.Home(rec, req)

	for _, u := range opts.ScriptURLs {
		if strings.Contains(u, "vue") {
			t.Fatalf("home must not load Vue: %s", u)
		}
	}
	for _, s := range opts.Scripts {
		if strings.Contains(s, "Vue") || strings.Contains(s, "createApp") {
			t.Fatalf("home must not emit a Vue mount script: %s", s)
		}
	}
}

// TestHomeEmpty covers the empty-state rows.
func TestHomeEmpty(t *testing.T) {
	cfg, _ := testConfig(newTestStore(t))
	section := UI(cfg)
	req := httptest.NewRequest(http.MethodGet, "/?controller=home", nil)
	rec := httptest.NewRecorder()
	section.Home(rec, req)

	page := rec.Body.String()
	if !strings.Contains(page, "No workflows yet") {
		t.Fatalf("empty state missing: %s", page)
	}
	if strings.Contains(page, "{{") {
		t.Fatalf("unsubstituted placeholder in page: %s", page)
	}
}
