package nodeadmin

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dracory/entitystore"
	_ "modernc.org/sqlite"

	"github.com/dracory/nodestore"
)

func initTestAdmin(t *testing.T) Interface {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	// :memory: databases are per-connection — cap the pool at one so
	// every store/handler shares the same database.
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
	// AuthUserID stands in for host-owned auth: with no credentials
	// configured a bare admin is in first-run setup mode, which would
	// redirect every route these tests exercise.
	admin, err := New(Options{
		Store:      store,
		AuthUserID: func(r *http.Request) string { return "test-user" },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return admin
}

func TestNewRequiresStore(t *testing.T) {
	if _, err := New(Options{}); err != ErrStoreRequired {
		t.Fatal("expected ErrStoreRequired")
	}
}

func TestHomeRenders(t *testing.T) {
	admin := initTestAdmin(t)
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatal("status:", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Dashboard") {
		t.Fatal("home page missing dashboard")
	}
}

func TestWorkflowApiRoundTrip(t *testing.T) {
	admin := initTestAdmin(t)

	create := httptest.NewRequest(http.MethodPost, "/api/workflow-create",
		strings.NewReader(`{"name":"test flow","nodes":[{"id":"n1","type":"inject","position":{"x":1,"y":2},"data":{"a":1}}]}`))
	create.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, create)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "workflow created") {
		t.Fatal("create:", rec.Body.String())
	}

	list := httptest.NewRequest(http.MethodPost, "/api/workflow-fetch-all",
		strings.NewReader(`{}`))
	list.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, list)
	body := rec.Body.String()
	if !strings.Contains(body, "test flow") || !strings.Contains(body, "inject") {
		t.Fatal("list:", body)
	}
}

func TestApiRejectsNonJSONPost(t *testing.T) {
	admin := initTestAdmin(t)
	req := httptest.NewRequest(http.MethodPost, "/api/workflow-fetch-all",
		strings.NewReader("x"))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatal("status:", rec.Code)
	}
}

func TestAuthRedirect(t *testing.T) {
	store := initTestAdmin(t)
	_ = store
	db, _ := sql.Open("sqlite", ":memory:")
	db.SetMaxOpenConns(1) // :memory: databases are per-connection
	entityStore, _ := entitystore.NewStore(entitystore.NewStoreOptions{
		DB: db, EntityTableName: "e", AttributeTableName: "a", AutomigrateEnabled: true,
	})
	ns, _ := nodestore.NewStore(nodestore.NewStoreOptions{EntityStore: entityStore})
	admin, err := New(Options{
		Store:        ns,
		AuthUserID:   func(r *http.Request) string { return "" },
		AdminHomeURL: "/login",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatal("expected redirect to /login")
	}
}
