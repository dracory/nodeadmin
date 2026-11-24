package shared

import (
	"net/http"

	"github.com/dracory/cdn"
	"github.com/dracory/hb"

	"github.com/dracory/nodestore"
)

// bootleganceScriptURL is the Bootlegance theme/mode switcher CDN —
// loaded in <head> so the stored mode (localStorage "cf-theme")
// applies data-bs-theme before first paint. Themes resolve against
// the script's own CDN endpoint; the default "bootstrap" theme is a
// no-op over stock Bootstrap CSS.
const bootleganceScriptURL = "https://cdn.jsdelivr.net/gh/lesichkovm/bootlegance@v0.15.0/bootlegance.min.js"

// LayoutOptions are the assets a page can inject into the layout
type LayoutOptions struct {
	Styles     []string
	StyleURLs  []string
	Scripts    []string
	ScriptURLs []string
	// Store enables the nav badges (Workflows/Messages counts) in the
	// top navbar; nil renders the links badge-free.
	Store nodestore.StoreInterface
}

// FuncLayout renders the admin interface inside a host-provided layout
// (branding, menus). It receives the request and response writer so the
// host project can access request context when rendering. Returning ""
// falls back to the default Layout.
type FuncLayout func(w http.ResponseWriter, r *http.Request, webpageTitle, webpageHtml string, options LayoutOptions) string

// UiConfig holds the dependencies passed to section UI factories —
// the shopadmin/taskadmin pattern, with nodestore as the store
type UiConfig struct {
	Store nodestore.StoreInterface
	// Engine is the deployed-workflow engine the settings section
	// inspects/tunes — carried as any so shared stays runner-agnostic;
	// sections type-assert (stock engine: *noderunner.WorkflowEngine).
	Engine any
	Layout func(w http.ResponseWriter, r *http.Request, webpageTitle, webpageHtml string, options LayoutOptions) string
}

// Layout is the default layout renderer. It builds a complete HTML page
// using hb.NewWebpage() with Bootstrap CDN, matching the shopadmin
// pattern. If a FuncLayout is provided in Options, it takes precedence
// over this default.
func Layout(w http.ResponseWriter, r *http.Request, webpageTitle, webpageHtml string, options LayoutOptions) string {
	_ = w
	return webpageComplete(r, webpageTitle, webpageHtml, options).ToHTML()
}

// webpageComplete builds the default webpage template
func webpageComplete(r *http.Request, webpageTitle, webpageHtml string, options LayoutOptions) hb.TagInterface {
	webpage := hb.NewWebpage()
	webpage.SetTitle(webpageTitle)

	// Bootlegance goes in the head, before other assets, so the saved
	// mode/theme applies before first paint (no light→dark flash).
	webpage.Head().AddChild(hb.NewHTML(
		`<script src="` + bootleganceScriptURL + `" data-bootlegance data-default-theme="bootstrap"></script>`))

	webpage.AddStyleURLs([]string{
		cdn.BootstrapCss_5_3_8(),
		cdn.BootstrapIconsCss_1_13_1(),
	})
	webpage.AddScriptURLs([]string{
		cdn.BootstrapJs_5_3_8(),
	})
	webpage.AddStyleURLs(options.StyleURLs)
	webpage.AddScriptURLs(options.ScriptURLs)
	webpage.AddStyles(options.Styles)
	webpage.AddStyle(`body {
		font-family: "Nunito", sans-serif;
		font-size: 0.9rem;
		font-weight: 400;
		line-height: 1.6;
		color: var(--bs-body-color);
		text-align: left;
		background-color: var(--bs-body-bg);
	}
	/* Bootlegance "Theme Settings" dropdown */
	.theme-settings-menu { width: 300px; padding: 1rem; }
	.theme-settings-menu .theme-section-label {
		font-size: 0.7rem;
		font-weight: 700;
		text-transform: uppercase;
		opacity: 0.6;
		margin-bottom: 0.35rem;
	}
	.theme-settings-menu .font-size-badge { min-width: 3rem; }`)
	webpage.AddChild(Navbar(r, options.Store))
	webpage.AddChild(hb.NewHTML(webpageHtml))
	webpage.AddScripts(options.Scripts)

	return webpage
}
