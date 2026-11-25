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

// plexFontsURL loads the IBM Plex Sans/Mono pair the admin skin uses
// (CDN, same policy as the other StyleURLs below).
const plexFontsURL = "https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500;600&family=IBM+Plex+Sans:wght@400;500;600&display=swap"

// SkinCSS is the shared admin skin: signal-red accent on the neutral
// Bootstrap canvas, mono "quick nav" pill strip, stat cards, status
// pills, tables, feed, settings sections and the workflow minimap.
// All colors resolve against Bootstrap CSS variables so light/dark
// (data-bs-theme) keeps working everywhere. Exported because the
// editor — a standalone page that bypasses the layout — injects the
// same skin so the shared chrome renders consistently there.
const SkinCSS = `:root {
	--na-accent: #e0452a;
	--na-accent-soft: rgba(224, 69, 42, .12);
	--na-ok: #2ea043;
	--na-warn: #d4a017;
	--na-run: #1f7aec;
}
[data-bs-theme="dark"] {
	--na-accent-soft: rgba(224, 69, 42, .18);
}
.na-mono, .stat-number, code, .node-chip, .trace-id {
	font-family: "IBM Plex Mono", ui-monospace, monospace;
}
/* top bar */
.na-topbar { border-bottom: 1px solid var(--bs-border-color); }
.na-brand {
	font-family: "IBM Plex Mono", monospace;
	font-weight: 600;
	letter-spacing: .5px;
	text-decoration: none;
}
.na-brand .flow-dot {
	display: inline-block;
	width: .55em; height: .55em;
	border-radius: 50%;
	background: var(--na-accent);
	margin-right: .35em;
	box-shadow: 0 0 8px var(--na-accent);
}
/* quick nav pill strip */
.na-quicknav {
	font-family: "IBM Plex Mono", monospace;
	font-size: .8rem;
	border: 1px solid var(--bs-border-color);
	border-radius: .5rem;
	background: var(--bs-tertiary-bg);
}
.na-quicknav .nav-link {
	color: var(--bs-secondary-color);
	border-radius: .375rem;
	padding: .35rem .8rem;
}
.na-quicknav .nav-link.active {
	background: var(--na-accent);
	color: #fff;
}
.na-quicknav .nav-link.active .badge { background: rgba(255,255,255,.25); color: #fff; }
.na-quicknav .qn-label {
	color: var(--na-accent);
	font-weight: 600;
	padding: .35rem .6rem;
	border-right: 1px solid var(--bs-border-color);
	margin-right: .5rem;
}
/* stat cards */
.stat-card {
	border: 1px solid var(--bs-border-color);
	border-left: 3px solid var(--na-accent);
	border-radius: .5rem;
	background: var(--bs-body-bg);
	transition: transform .15s ease, box-shadow .15s ease;
}
.stat-card:hover { transform: translateY(-2px); box-shadow: 0 4px 14px rgba(0,0,0,.18); }
.stat-card.ok   { border-left-color: var(--na-ok); }
.stat-card.run  { border-left-color: var(--na-run); }
.stat-card.warn { border-left-color: var(--na-warn); }
.stat-number { font-size: 2rem; font-weight: 600; line-height: 1.1; }
/* status pills */
.pill {
	font-family: "IBM Plex Mono", monospace;
	font-size: .7rem;
	padding: .15em .55em;
	border-radius: 999px;
	text-transform: uppercase;
	letter-spacing: .04em;
}
.pill-deployed { background: rgba(46,160,67,.15); color: var(--na-ok); }
.pill-draft    { background: rgba(212,160,23,.15); color: var(--na-warn); }
.pill-stopped  { background: var(--bs-secondary-bg); color: var(--bs-secondary-color); }
.pill-running  { background: rgba(31,122,236,.15); color: var(--na-run); }
.pill-error    { background: rgba(224,69,42,.15); color: var(--na-accent); }
/* workflow minimap thumbnail */
.wf-minimap {
	width: 120px; height: 44px;
	border: 1px solid var(--bs-border-color);
	border-radius: .375rem;
	background:
		radial-gradient(circle at 1px 1px, var(--bs-border-color) 1px, transparent 0) 0 0/12px 12px,
		var(--bs-tertiary-bg);
	position: relative;
	overflow: hidden;
	flex: none;
}
.wf-minimap svg { position: absolute; inset: 0; width: 100%; height: 100%; }
/* tables */
.na-table { font-size: .875rem; }
.na-table thead th {
	font-family: "IBM Plex Mono", monospace;
	font-size: .7rem;
	text-transform: uppercase;
	letter-spacing: .06em;
	color: var(--bs-secondary-color);
	border-bottom-width: 1px;
}
.na-table tbody tr:hover { background: var(--na-accent-soft); }
/* CSS bar chart */
.activity-chart {
	display: flex; align-items: flex-end; gap: 3px;
	height: 140px; padding-top: 1rem;
}
.activity-chart .bar {
	flex: 1;
	background: linear-gradient(to top, var(--na-accent), rgba(224,69,42,.35));
	border-radius: 2px 2px 0 0;
	min-height: 2px;
}
.activity-chart .bar.dim { background: var(--bs-border-color); }
/* activity feed */
.feed-item { border-left: 2px solid var(--bs-border-color); padding-left: .9rem; position: relative; }
.feed-item::before {
	content: ""; position: absolute; left: -5px; top: .4rem;
	width: 8px; height: 8px; border-radius: 50%;
	background: var(--bs-secondary-color);
}
.feed-item.deploy::before { background: var(--na-ok); }
.feed-item.error::before  { background: var(--na-accent); }
.feed-item.store::before  { background: var(--na-run); }
/* node chips */
.node-chip {
	font-size: .72rem;
	border: 1px solid var(--bs-border-color);
	border-radius: .3rem;
	padding: .1em .5em;
	background: var(--bs-tertiary-bg);
	white-space: nowrap;
}
.node-chip .dot { display:inline-block; width:.5em; height:.5em; border-radius:2px; margin-right:.35em; background: var(--na-accent); }
/* page heading */
.page-head h1 { font-weight: 600; letter-spacing: -.01em; }
.page-head .crumb {
	font-family: "IBM Plex Mono", monospace;
	font-size: .75rem;
	color: var(--bs-secondary-color);
	text-transform: uppercase;
	letter-spacing: .1em;
}
/* message detail */
.msg-json {
	font-family: "IBM Plex Mono", monospace;
	font-size: .8rem;
	background: var(--bs-tertiary-bg);
	border: 1px solid var(--bs-border-color);
	border-radius: .5rem;
	padding: .75rem 1rem;
	overflow-x: auto;
}
.msg-hop { border-left: 3px solid var(--na-run); }
/* settings / content sections */
.na-section {
	border: 1px solid var(--bs-border-color);
	border-radius: .5rem;
}
.na-section > .head {
	font-family: "IBM Plex Mono", monospace;
	font-size: .8rem; font-weight: 600;
	border-bottom: 1px solid var(--bs-border-color);
	padding: .6rem 1rem;
	background: var(--bs-tertiary-bg);
	border-radius: .5rem .5rem 0 0;
}
.form-text { font-size: .78rem; }
/* accent action button */
.btn-na-accent { background: var(--na-accent); color: #fff; }
.btn-na-accent:hover, .btn-na-accent:focus { background: #c93a22; color: #fff; }
/* subtle page enter */
@keyframes na-in { from { opacity: 0; transform: translateY(6px); } to { opacity: 1; transform: none; } }
main > * { animation: na-in .25s ease both; }
main > *:nth-child(2) { animation-delay: .04s; }
main > *:nth-child(3) { animation-delay: .08s; }`

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
		plexFontsURL,
	})
	webpage.AddScriptURLs([]string{
		cdn.BootstrapJs_5_3_8(),
	})
	webpage.AddStyleURLs(options.StyleURLs)
	webpage.AddScriptURLs(options.ScriptURLs)
	webpage.AddStyles(options.Styles)
	webpage.AddStyle(`body {
		font-family: "IBM Plex Sans", system-ui, sans-serif;
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
	webpage.AddStyle(SkinCSS)
	webpage.AddChild(Navbar(r, options.Store))
	webpage.AddChild(hb.NewHTML(webpageHtml))
	webpage.AddScripts(options.Scripts)

	return webpage
}
