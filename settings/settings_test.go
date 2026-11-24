package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dracory/entitystore"
	_ "modernc.org/sqlite"

	"github.com/dracory/nodeadmin/shared"
	"github.com/dracory/noderun"
	"github.com/dracory/nodestore"
)

// testSection builds a settings section on an in-memory nodestore and
// returns the HTTP handler the nodeadmin routes map would invoke.
func testSection(t *testing.T, engine *noderunner.WorkflowEngine) (http.HandlerFunc, nodestore.StoreInterface) {
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

	section := New(engine, store)
	cfg := shared.UiConfig{
		Store:  store,
		Engine: engine,
		Layout: func(w http.ResponseWriter, r *http.Request, title, html string, options shared.LayoutOptions) string {
			return html
		},
	}
	return func(w http.ResponseWriter, r *http.Request) {
		section.UI(cfg).SettingsManager(w, r)
	}, store
}

func post(t *testing.T, h http.HandlerFunc, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// TestLoadSaveSettings covers the load/save cycle: an override
// persists into the config store's global_config blob and applies to
// the live engine.
func TestLoadSaveSettings(t *testing.T) {
	engine := noderunner.NewWorkflowEngine()
	h, store := testSection(t, engine)

	rec := post(t, h, "/?controller=settings&action=load-settings", nil)
	if !strings.Contains(rec.Body.String(), `"status":"success"`) {
		t.Fatalf("load: %s", rec.Body.String())
	}

	rec = post(t, h, "/?controller=settings&action=save-setting", map[string]any{
		"maxSteps": 42, "allowExec": true,
	})
	if !strings.Contains(rec.Body.String(), `"status":"success"`) {
		t.Fatalf("save: %s", rec.Body.String())
	}
	if engine.MaxSteps != 42 || !engine.AllowExec {
		t.Fatalf("overrides not applied: %v %v", engine.MaxSteps, engine.AllowExec)
	}
	raw, _ := store.ConfigGet(context.Background(), "global_config")
	var cfg map[string]any
	_ = json.Unmarshal([]byte(raw), &cfg)
	eng, _ := cfg["engine"].(map[string]any)
	if eng["maxSteps"] != 42.0 || eng["allowExec"] != true {
		t.Fatalf("persisted overrides: %v", cfg["engine"])
	}

	// Clearing restores the startup snapshot and removes the section.
	rec = post(t, h, "/?controller=settings&action=save-setting", map[string]any{})
	if !strings.Contains(rec.Body.String(), `"status":"success"`) {
		t.Fatalf("clear: %s", rec.Body.String())
	}
	if engine.MaxSteps != 0 || engine.AllowExec {
		t.Fatalf("startup not restored: %v %v", engine.MaxSteps, engine.AllowExec)
	}
}

// TestSettingsPageScriptsAfterVue is a regression guard for the
// unmounted-Vue bug: the settings scripts must be emitted AFTER the
// Vue script URL in the rendered page, otherwise `const { createApp }
// = Vue` runs while Vue is still undefined and the page renders raw
// mustaches.
func TestSettingsPageScriptsAfterVue(t *testing.T) {
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

	section := New(noderunner.NewWorkflowEngine(), store)
	cfg := shared.UiConfig{
		Store: store,
		// Mirror nodeadmin's admin.render: the default layout's
		// return value is written to the response by the wrapper.
		Layout: func(w http.ResponseWriter, r *http.Request, title, html string, options shared.LayoutOptions) string {
			_, _ = w.Write([]byte(shared.Layout(w, r, title, html, options)))
			return ""
		},
	}
	handler := section.UI(cfg).SettingsManager

	req := httptest.NewRequest(http.MethodGet, "/?controller=settings", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	page := rec.Body.String()
	vueIdx := strings.Index(page, "vue.global")
	urlsIdx := strings.Index(page, "window.settingsManagerUrls = {")
	appIdx := strings.Index(page, "const { createApp } = Vue")
	if vueIdx < 0 || urlsIdx < 0 || appIdx < 0 {
		t.Fatalf("expected vue script, urls script and settings.js in page")
	}
	if !(vueIdx < urlsIdx && urlsIdx < appIdx) {
		t.Fatalf("script order wrong: vue=%d urls=%d app=%d", vueIdx, urlsIdx, appIdx)
	}
}

// TestSettingsPageHasAppMount is a regression guard for the
// missing-mount bug: the settings fragment carries no #app element,
// so the page composer must wrap it — without the wrapper
// app.mount('#app') throws and the page renders raw mustaches.
func TestSettingsPageHasAppMount(t *testing.T) {
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

	section := New(noderunner.NewWorkflowEngine(), store)
	cfg := shared.UiConfig{
		Store: store,
		Layout: func(w http.ResponseWriter, r *http.Request, title, html string, options shared.LayoutOptions) string {
			_, _ = w.Write([]byte(shared.Layout(w, r, title, html, options)))
			return ""
		},
	}
	handler := section.UI(cfg).SettingsManager

	req := httptest.NewRequest(http.MethodGet, "/?controller=settings", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	page := rec.Body.String()
	if !strings.Contains(page, `id="app"`) {
		t.Fatal("settings page has no #app mount point — Vue cannot mount")
	}
	// The mount wrapper must enclose the fragment, not sit inside the
	// section nav it injects.
	if !strings.Contains(page, `<div id="app">`) {
		t.Fatal(`expected a <div id="app"> wrapper around the fragment`)
	}
}

// TestSettingsRejectsNonJSONPost covers the media-type gate.
func TestSettingsRejectsNonJSONPost(t *testing.T) {
	h, _ := testSection(t, nil)
	req := httptest.NewRequest(http.MethodPost, "/?controller=settings&action=save-setting", strings.NewReader("x=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d", rec.Code)
	}
}
