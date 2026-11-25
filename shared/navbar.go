package shared

import (
	"context"
	"net/http"
	"strconv"

	"github.com/dracory/hb"

	"github.com/dracory/nodestore"
)

// navItem is one top-level navigation destination.
type navItem struct {
	label string
	key   string
	url   string
	count int // -1 = no badge
}

// navItems builds the section destination list; the active item is
// derived from the ?controller= query parameter. Workflows and
// Messages carry record counts when the store is available.
func navItems(r *http.Request, store nodestore.StoreInterface) []navItem {
	base := NodeAdminURL(r)
	link := func(controller string) string {
		return base + "?controller=" + controller
	}

	count := func(query func(ctx context.Context) (int64, error)) int {
		if store == nil || r == nil {
			return -1
		}
		ctx := context.Background()
		if r != nil {
			ctx = r.Context()
		}
		n, err := query(ctx)
		if err != nil {
			return -1
		}
		return int(n)
	}

	return []navItem{
		{label: "Dashboard", key: CONTROLLER_HOME, url: link(CONTROLLER_HOME), count: -1},
		{label: "Workflows", key: CONTROLLER_WORKFLOWS, url: link(CONTROLLER_WORKFLOWS),
			count: count(func(ctx context.Context) (int64, error) {
				return store.WorkflowCount(ctx, nodestore.WorkflowQuery())
			})},
		{label: "Nodes", key: CONTROLLER_NODES, url: link(CONTROLLER_NODES),
			count: count(func(ctx context.Context) (int64, error) {
				return store.NodeCount(ctx, nodestore.NodeQuery())
			})},
		{label: "Messages", key: CONTROLLER_MESSAGES, url: link(CONTROLLER_MESSAGES),
			count: count(func(ctx context.Context) (int64, error) {
				return store.MessageCount(ctx, nodestore.MessageQuery())
			})},
		{label: "Editor", key: CONTROLLER_EDITOR, url: link(CONTROLLER_EDITOR), count: -1},
		{label: "Settings", key: CONTROLLER_SETTINGS, url: link(CONTROLLER_SETTINGS), count: -1},
	}
}

func activeController(r *http.Request) string {
	if r != nil && r.URL != nil {
		if c := r.URL.Query().Get("controller"); c != "" {
			return c
		}
	}
	return CONTROLLER_HOME
}

func navLink(item navItem, active string) *hb.Tag {
	l := hb.Hyperlink().
		HTML(item.label).
		Href(item.url).
		Class("nav-link").
		ClassIf(item.key == active, "active")
	if item.count >= 0 {
		l.Child(hb.Span().
			Class("badge text-bg-secondary ms-2").
			HTML(strconv.Itoa(item.count)))
	}
	return l
}

// Navbar renders the application top bar: the mono brand mark with
// the signal-red flow dot and, on the right, the Bootlegance theme
// switcher dropdown — mode (light/dark/auto), theme picker and
// font-size controls. Section navigation lives in SectionNav (the
// "NODES ›" pill strip emitted inside each page's <main>). The page
// must include bootlegance.min.js (shared.Layout emits it in the
// head) for the switcher controls to wire up.
func Navbar(r *http.Request, store nodestore.StoreInterface) hb.TagInterface {
	modeBtn := func(label, mode string) hb.TagInterface {
		return hb.Button().
			Class("btn btn-sm btn-outline-secondary").
			Attr("type", "button").
			Attr("data-bootlegance-mode", mode).
			Text(label)
	}

	themeMenu := hb.Div().
		Class("dropdown-menu dropdown-menu-end theme-settings-menu").
		Children([]hb.TagInterface{
			hb.Div().Class("theme-section-label").Text("Mode"),
			hb.Div().Class("d-flex gap-1").Children([]hb.TagInterface{
				modeBtn("Light", "light"),
				modeBtn("Dark", "dark"),
				modeBtn("Auto", "auto"),
			}),
			hb.Hr().Class("dropdown-divider"),
			hb.Div().Class("theme-section-label").Text("Theme"),
			hb.Div().Attr("data-bootlegance-picker", "true"),
			hb.Hr().Class("dropdown-divider"),
			hb.Div().Class("theme-section-label").Text("Font size"),
			hb.Div().Class("d-flex align-items-center gap-1").Children([]hb.TagInterface{
				hb.Button().Class("btn btn-sm btn-outline-secondary").
					Attr("type", "button").
					Attr("data-bootlegance-font-size", "decrease").Text("A-"),
				hb.Span().Class("font-size-badge text-center").
					Attr("data-bootlegance-font-size-value", "true").Text("100%"),
				hb.Button().Class("btn btn-sm btn-outline-secondary").
					Attr("type", "button").
					Attr("data-bootlegance-font-size", "increase").Text("A+"),
				hb.Button().Class("btn btn-sm btn-outline-secondary").
					Attr("type", "button").
					Attr("data-bootlegance-font-size", "reset").Text("Reset"),
			}),
		})

	themeDropdown := hb.Div().Class("dropdown").Children([]hb.TagInterface{
		hb.Button().
			Class("btn btn-sm btn-outline-secondary dropdown-toggle").
			Attr("type", "button").
			Attr("data-bs-toggle", "dropdown").
			Attr("aria-expanded", "false").
			Attr("title", "Theme settings").
			HTML(`<i class="bi bi-circle-half"></i>`),
		themeMenu,
	})

	return hb.Nav().
		Class("na-topbar navbar navbar-expand px-3 py-2").
		Child(hb.Div().Class("container-fluid px-0 d-flex align-items-center").Children([]hb.TagInterface{
			hb.Hyperlink().
				Class("na-brand text-body").
				Href(NodeAdminURL(r)).
				HTML(`<span class="flow-dot"></span>nodeadmin`),
			hb.Div().
				Class("ms-auto d-flex align-items-center gap-2").
				Child(themeDropdown),
		}))
}
