package messages

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// TestMessages renders the message list: stored messages as table
// rows, the error badge, and the shared section nav.
func TestMessages(t *testing.T) {
	store := newTestStore(t)
	msg := nodestore.NewMessage().
		SetID("msg-1").
		SetPayload(map[string]interface{}{"data": "x"}).
		SetError("boom").
		SetTimestamp(time.Now())
	if err := store.MessageCreate(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	cfg := shared.UiConfig{
		Store: store,
		Layout: func(w http.ResponseWriter, r *http.Request, title, html string, options shared.LayoutOptions) string {
			_, _ = w.Write([]byte(html))
			return ""
		},
	}
	section := UI(cfg)
	req := httptest.NewRequest(http.MethodGet, "/?controller=messages", nil)
	rec := httptest.NewRecorder()
	section.Messages(rec, req)

	page := rec.Body.String()
	for _, needle := range []string{"nav nav-pills", "msg-1", "Yes"} {
		if !strings.Contains(page, needle) {
			t.Fatalf("page missing %q\n%s", needle, page)
		}
	}
}

// TestMessagesEmpty covers the empty-state row.
func TestMessagesEmpty(t *testing.T) {
	cfg := shared.UiConfig{
		Store: newTestStore(t),
		Layout: func(w http.ResponseWriter, r *http.Request, title, html string, options shared.LayoutOptions) string {
			_, _ = w.Write([]byte(html))
			return ""
		},
	}
	section := UI(cfg)
	req := httptest.NewRequest(http.MethodGet, "/?controller=messages", nil)
	rec := httptest.NewRecorder()
	section.Messages(rec, req)

	if !strings.Contains(rec.Body.String(), "No messages yet") {
		t.Fatalf("empty state missing: %s", rec.Body.String())
	}
}
