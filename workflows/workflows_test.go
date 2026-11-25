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

	cfg, _ := testConfig(store)
	section := UI(cfg)
	req := httptest.NewRequest(http.MethodGet, "/?controller=workflows", nil)
	rec := httptest.NewRecorder()
	section.Workflows(rec, req)

	page := rec.Body.String()
	for _, needle := range []string{
		"na-quicknav",
		"wf-1", "flow-a", "deployed",
		"wf-2", "flow-b", "draft",
		`controller=editor`,
	} {
		if !strings.Contains(page, needle) {
			t.Fatalf("page missing %q\n%s", needle, page)
		}
	}
}

// TestWorkflowsNoVueMountScript is a regression guard for the
// "Vue is not defined" console error: the list page never loads Vue,
// so it must not emit a Vue-dependent mount script either.
func TestWorkflowsNoVueMountScript(t *testing.T) {
	cfg, opts := testConfig(newTestStore(t))
	section := UI(cfg)
	req := httptest.NewRequest(http.MethodGet, "/?controller=workflows", nil)
	rec := httptest.NewRecorder()
	section.Workflows(rec, req)

	for _, u := range opts.ScriptURLs {
		if strings.Contains(u, "vue") {
			t.Fatalf("workflows must not load Vue: %s", u)
		}
	}
	for _, s := range opts.Scripts {
		if strings.Contains(s, "Vue") || strings.Contains(s, "createApp") {
			t.Fatalf("workflows must not emit a Vue mount script: %s", s)
		}
	}
}

// TestWorkflowsEmpty covers the empty-state row.
func TestWorkflowsEmpty(t *testing.T) {
	cfg, _ := testConfig(newTestStore(t))
	section := UI(cfg)
	req := httptest.NewRequest(http.MethodGet, "/?controller=workflows", nil)
	rec := httptest.NewRecorder()
	section.Workflows(rec, req)

	if !strings.Contains(rec.Body.String(), "No workflows yet") {
		t.Fatalf("empty state missing: %s", rec.Body.String())
	}
}
