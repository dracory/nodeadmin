package settings

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/dracory/cdn"

	"github.com/dracory/nodeadmin/shared"
)

var (
	//go:embed settings.html
	settingsHTML string

	//go:embed settings.js
	settingsJS string
)

// renderPage composes the settings page body — the embedded content
// fragment — and injects the window.settingsManagerUrls + settings.js
// scripts via LayoutOptions.Scripts so they run after Vue loads. It
// hands the result to the UiConfig layout, which owns the outer chrome
// and writes the response itself.
func (u *ui) renderPage(w http.ResponseWriter, r *http.Request) {
	// The section's own controller URL is this request's path —
	// injected into the context by nodeadmin's Handle; AJAX actions
	// ride the same URL via ?controller=&action=.
	endpoint, _ := r.Context().Value(shared.KeyEndpoint).(string)
	if endpoint == "" {
		endpoint = r.URL.Path
	}
	controllerURL := endpoint + "?controller=" + shared.CONTROLLER_SETTINGS
	urls := map[string]string{
		"loadSettings":   controllerURL + "&action=" + actionLoadSettings,
		"saveSetting":    controllerURL + "&action=" + actionSaveSetting,
		"globalEnvFetch": "/api/global-env-fetch",
	}
	urlsJSON, _ := json.Marshal(urls)

	page := strings.Replace(settingsHTML, `<main class="container py-4">`,
		`<main class="container py-4">`+shared.SectionNav(r, u.config.Store).ToHTML(), 1)

	// The Vue app mounts #app — the fragment has no such element, so
	// wrap it here (without it app.mount throws and the page renders
	// raw mustaches).
	page = `<div id="app">` + page + `</div>`

	// The urls script and settings.js must run AFTER Vue loads, so they
	// go into LayoutOptions.Scripts — the layouts emit ScriptURLs (Vue)
	// first and Scripts last, after the body fragment. Inlining them in
	// the fragment would execute `const { createApp } = Vue` while Vue
	// is still undefined (ReferenceError → the app never mounts and the
	// page renders raw mustaches). settings.js mounts #app itself —
	// MountAppJS must NOT run here (it would mount a competing empty
	// app over the section).
	u.config.Layout(w, r, "Settings", page, shared.LayoutOptions{
		Store:      u.config.Store,
		StyleURLs:  []string{cdn.BootstrapIconsCss_1_13_1(), cdn.Notiflix_3_2_8_CSS()},
		ScriptURLs: []string{cdn.VueJs_3_5_42(), cdn.Notiflix_3_2_8()},
		Scripts: []string{
			fmt.Sprintf("window.settingsManagerUrls = %s;", string(urlsJSON)),
			settingsJS,
		},
	})
}
