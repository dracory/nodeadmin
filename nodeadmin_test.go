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
	// IsAccessAllowed stands in for host-owned auth — the library
	// holds no users; the host callback answers the one question.
	admin, err := New(Options{
		Store:           store,
		IsAccessAllowed: func(r *http.Request) bool { return true },
		GetAuthUserID:   func(r *http.Request) string { return "test-user" },
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

// newTestStore builds an in-memory nodestore for gate tests.
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

// TestIsAccessAllowedGate: denied requests get OnUnauthorized when
// wired (host redirect), the default 401 when it is not.
func TestIsAccessAllowedGate(t *testing.T) {
	store := newTestStore(t)
	admin, err := New(Options{
		Store:           store,
		IsAccessAllowed: func(r *http.Request) bool { return false },
		OnUnauthorized: func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatal("expected redirect to /login")
	}

	admin2, err := New(Options{
		Store:           newTestStore(t),
		IsAccessAllowed: func(r *http.Request) bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	admin2.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("default denial must be a plain 401, got %d", rec.Code)
	}
}

// TestOpenWhenNoAuth: nil IsAccessAllowed means open — the standalone
// host simply wires nothing.
func TestOpenWhenNoAuth(t *testing.T) {
	admin, err := New(Options{Store: newTestStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("no IsAccessAllowed must be open, got %d", rec.Code)
	}
}
