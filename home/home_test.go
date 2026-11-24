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
// the response (mirroring nodeadmin's render wrapper).
func testConfig(store nodestore.StoreInterface) shared.UiConfig {
	return shared.UiConfig{
		Store: store,
		Layout: func(w http.ResponseWriter, r *http.Request, title, html string, options shared.LayoutOptions) string {
			_, _ = w.Write([]byte(html))
			return ""
		},
	}
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

	section := UI(testConfig(store))
	req := httptest.NewRequest(http.MethodGet, "/?controller=home", nil)
	rec := httptest.NewRecorder()
	section.Home(rec, req)

	page := rec.Body.String()
	for _, needle := range []string{
		"nav nav-pills",    // section nav injected
		"flow-a", "flow-b", // workflow rows
		"Deployed", "Draft", // status badges
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

// TestHomeEmpty covers the empty-state rows.
func TestHomeEmpty(t *testing.T) {
	section := UI(testConfig(newTestStore(t)))
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
