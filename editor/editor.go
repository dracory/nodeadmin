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
	"github.com/dracory/nodestore"
)

//go:embed index.html
var indexHTML string

//go:embed editor_app.js
var editorAppJS string

// Vendored CodeMirror 5 assets (core + javascript mode + stylesheet)
// embedded into the binary — the code-format field editor has no CDN
// or runtime network dependency. If the script fails to parse or load
// the dialog falls back to a plain textarea.
//
//go:embed vendor/codemirror.min.js
var codeMirrorJS string

//go:embed vendor/javascript.min.js
var codeMirrorJSMode string

//go:embed vendor/codemirror.min.css
var codeMirrorCSS string

// Section serves the workflow editor. Unlike the other sections it
// writes a standalone page (the embedded flowadmin index document)
// rather than going through the UiConfig layout, so it carries its
// own dependencies.
type Section struct {
	// store feeds the navbar/section-nav count badges; nil renders
	// the links badge-free.
	store nodestore.StoreInterface
	// chrome controls whether nodeadmin injects its own navbar and
	// section pills (true) or emits the page bare for a host that
	// owns the outer chrome (false) — Options.DisableChrome.
	chrome bool
}

// New builds the editor section. chrome=false emits the editor page
// without nodeadmin's navbar or section pills so an embedding host
// can supply its own chrome.
func New(store nodestore.StoreInterface, chrome bool) *Section {
	return &Section{store: store, chrome: chrome}
}

// Editor serves the editor page with nodeadmin chrome and no store —
// the standalone handler kept for direct mounts. Mounts through
// nodeadmin.New dispatch to the Section built in buildRoutes, which
// carries the real store and the Options.DisableChrome flag.
func Editor(w http.ResponseWriter, r *http.Request) {
	New(nil, true).Editor(w, r)
}

// Editor serves the editor page or one of its assets. action=appjs
// serves the editor application script; action=codeeditorjs /
// action=codeeditorcss serve the vendored code-editor bundle;
// anything else serves the index document. The NodeAdminURL context value supplies the API
// base so every fetch targets <endpoint>/api/*.
func (s *Section) Editor(w http.ResponseWriter, r *http.Request) {
	endpoint, _ := r.Context().Value(shared.KeyNodeAdminURL).(string)
	if endpoint == "" {
		endpoint = "/nodes"
	}
	apiBase := strings.TrimSuffix(endpoint, "/") + "/api/"

	switch r.URL.Query().Get("action") {
	case "appjs":
		js := strings.ReplaceAll(editorAppJS, "'/api/", "'"+apiBase)
		js = strings.ReplaceAll(js, `"/api/`, `"`+apiBase)
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		_, _ = w.Write([]byte(js))
		return
	case "codeeditorjs":
		// One response carries core + the javascript mode — the page
		// loads a single script tag.
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		_, _ = w.Write([]byte(codeMirrorJS + "\n" + codeMirrorJSMode))
		return
	case "codeeditorcss":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write([]byte(codeMirrorCSS))
		return
	}

	page := strings.ReplaceAll(indexHTML,
		`src="/editor/editor_app.js"`,
		`src="?controller=editor&action=appjs"`)
	page = strings.ReplaceAll(page,
		`src="/editor/codeeditor.js"`,
		`src="?controller=editor&action=codeeditorjs"`)
	page = strings.ReplaceAll(page,
		`href="/editor/codeeditor.css"`,
		`href="?controller=editor&action=codeeditorcss"`)
	// The flowadmin app-navigation component is gone. With chrome on
	// (the default) inject the same server-side top navbar (brand +
	// section links + theme switcher) the shared layout emits, so the
	// editor keeps the app chrome. It goes ABOVE #app, not inside it:
	// Vue re-renders #app on mount, which would drop the listeners
	// bootlegance wires onto the theme-switcher controls at
	// DOMContentLoaded. With chrome off (Options.DisableChrome) the
	// host owns the outer chrome, so the page ships bare.
	if s.chrome {
		page = strings.Replace(page,
			`<app-navigation active="editor"></app-navigation>`,
			shared.SectionNav(r, s.store).ToHTML(), 1)
		page = strings.Replace(page,
			`<div id="app">`,
			shared.Navbar(r, s.store).ToHTML()+`<div id="app">`, 1)
	} else {
		page = strings.Replace(page,
			`<app-navigation active="editor"></app-navigation>`, ``, 1)
	}
	page = strings.Replace(page,
		`<script src="/js/components/navigation.js"></script>`, ``, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(page))
}
