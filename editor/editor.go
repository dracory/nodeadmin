// Package editor serves the workflow editor — the same Vue app as
// flowadmin's /editor — as a nodeadmin section selected through
// ?controller=editor. The index page and its app script are the
// flowadmin embedded copies, rewritten at serve time so the editor's
// API calls land on this package's own /api mount.
package editor

import (
	_ "embed"
	"net/http"
	"strings"

	"github.com/dracory/nodeadmin/shared"
)

//go:embed index.html
var indexHTML string

//go:embed editor_app.js
var editorAppJS string

// Editor serves the editor page or one of its assets. action=appjs
// serves the editor application script; anything else serves the
// index document. The NodeAdminURL context value supplies the API
// base so every fetch targets <endpoint>/api/*.
func Editor(w http.ResponseWriter, r *http.Request) {
	endpoint, _ := r.Context().Value(shared.KeyNodeAdminURL).(string)
	if endpoint == "" {
		endpoint = "/nodes"
	}
	apiBase := strings.TrimSuffix(endpoint, "/") + "/api/"

	if r.URL.Query().Get("action") == "appjs" {
		js := strings.ReplaceAll(editorAppJS, "'/api/", "'"+apiBase)
		js = strings.ReplaceAll(js, `"/api/`, `"`+apiBase)
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		_, _ = w.Write([]byte(js))
		return
	}

	page := strings.ReplaceAll(indexHTML,
		`src="/editor/editor_app.js"`,
		`src="?controller=editor&action=appjs"`)
	// The flowadmin app-navigation component is gone; inject the same
	// server-side top navbar (brand + section links + theme switcher)
	// the shared layout emits, so the editor keeps the app chrome.
	// It goes ABOVE #app, not inside it: Vue re-renders #app on mount,
	// which would drop the listeners bootlegance wires onto the
	// theme-switcher controls at DOMContentLoaded.
	page = strings.Replace(page,
		`<app-navigation active="editor"></app-navigation>`,
		shared.SectionNav(r, nil).ToHTML(), 1)
	page = strings.Replace(page,
		`<div id="app">`,
		shared.Navbar(r, nil).ToHTML()+`<div id="app">`, 1)
	page = strings.Replace(page,
		`<script src="/js/components/navigation.js"></script>`, ``, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(page))
}
