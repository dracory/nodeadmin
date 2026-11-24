package workflows

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

func testConfig(store nodestore.StoreInterface) shared.UiConfig {
	return shared.UiConfig{
		Store: store,
		Layout: func(w http.ResponseWriter, r *http.Request, title, html string, options shared.LayoutOptions) string {
			_, _ = w.Write([]byte(html))
			return ""
		},
	}
}

// TestWorkflows renders the workflow list: rows with node/edge counts
// and deployed/draft badges, plus the shared section nav.
func TestWorkflows(t *testing.T) {
	store := newTestStore(t)
	wf := nodestore.NewWorkflow("flow-a").SetID("wf-1").
		SetNodes([]nodestore.NodeInterface{
			nodestore.NewNode("n1", "junction").SetData(map[string]interface{}{}),
		}).
		SetDeployed(true)
	if err := store.WorkflowCreate(context.Background(), wf); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkflowCreate(context.Background(), nodestore.NewWorkflow("flow-b").SetID("wf-2")); err != nil {
		t.Fatal(err)
	}

	section := UI(testConfig(store))
	req := httptest.NewRequest(http.MethodGet, "/?controller=workflows", nil)
	rec := httptest.NewRecorder()
	section.Workflows(rec, req)

	page := rec.Body.String()
	for _, needle := range []string{
		"nav nav-pills",
		"wf-1", "flow-a", "Deployed",
		"wf-2", "flow-b", "Draft",
		`controller=editor`,
	} {
		if !strings.Contains(page, needle) {
			t.Fatalf("page missing %q\n%s", needle, page)
		}
	}
}

// TestWorkflowsEmpty covers the empty-state row.
func TestWorkflowsEmpty(t *testing.T) {
	section := UI(testConfig(newTestStore(t)))
	req := httptest.NewRequest(http.MethodGet, "/?controller=workflows", nil)
	rec := httptest.NewRecorder()
	section.Workflows(rec, req)

	if !strings.Contains(rec.Body.String(), "No workflows yet") {
		t.Fatalf("empty state missing: %s", rec.Body.String())
	}
}
