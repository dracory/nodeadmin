package nodeadmin

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/dracory/entitystore"
	_ "modernc.org/sqlite"

	"github.com/dracory/nodestore"
)

// newTestStore builds an in-memory nodestore for auth tests.
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

// writeAuthFile writes a Node-RED adminAuth-shaped credentials file.
func writeAuthFile(t *testing.T, password string) string {
	t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]interface{}{
		"users": []map[string]string{{"username": "admin", "password": hash}},
	})
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuthFileGate(t *testing.T) {
	store := newTestStore(t)
	admin, err := New(Options{Store: store, AuthFile: writeAuthFile(t, "s3cret")})
	if err != nil {
		t.Fatal(err)
	}

	// No credentials → 401 challenge
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("expected 401 challenge, got %d", rec.Code)
	}

	// Wrong password → 401
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("admin", "wrong")
	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password must be rejected, got %d", rec.Code)
	}

	// Correct credentials → the admin surface renders
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("admin", "s3cret")
	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid credentials must pass, got %d", rec.Code)
	}
}

func TestAuthConfigStoreInterim(t *testing.T) {
	store := newTestStore(t)
	hash, err := HashPassword("cfgs3cret")
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := json.Marshal(map[string]interface{}{
		"users": []map[string]string{{"username": "admin", "password": hash}},
	})
	if err := store.ConfigSet(context.Background(), configKeyAuthUsers, string(doc)); err != nil {
		t.Fatal(err)
	}

	admin, err := New(Options{Store: store})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("config-store credentials must gate the admin, got %d", rec.Code)
	}
	req.SetBasicAuth("admin", "cfgs3cret")
	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("config-store credentials must authenticate, got %d", rec.Code)
	}
}

func TestAuthUserIDOverridesCredentials(t *testing.T) {
	store := newTestStore(t)
	admin, err := New(Options{
		Store:      store,
		AuthFile:   writeAuthFile(t, "s3cret"),
		AuthUserID: func(r *http.Request) string { return "host-user" },
	})
	if err != nil {
		t.Fatal(err)
	}
	// With AuthUserID wired the host owns auth — no basic challenge.
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("AuthUserID path must skip basic auth, got %d", rec.Code)
	}
}
