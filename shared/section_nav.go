package shared

import (
	"net/http"

	"github.com/dracory/hb"

	"github.com/dracory/nodestore"
)

// SectionNav renders the node admin section navigation as pills,
// matching the nav-pills pattern used across the external admin
// packages (shopadmin, taskadmin). The active tab is derived from the
// ?controller= query parameter. Workflows and Messages tabs show
// record counts as badges when the store is available.
func SectionNav(r *http.Request, store nodestore.StoreInterface) hb.TagInterface {
	active := activeController(r)

	tabs := make([]hb.TagInterface, 0, 5)
	for _, item := range navItems(r, store) {
		tabs = append(tabs, hb.LI().Class("nav-item").Child(navLink(item, active)))
	}

	ulNav := hb.UL().
		Class("nav nav-pills p-1 justify-content-center flex-grow-1").
		Children(tabs)

	label := hb.Span().
		Class("fw-bolder text-uppercase ms-3 me-3").
		Text("Nodes").
		Child(hb.I().Class("bi bi-chevron-right ms-2"))

	return hb.Div().
		Class("card card-default mt-3 mb-4 bg-primary-subtle").
		Child(hb.Div().
			Class("card-body d-flex align-items-center").
			Style("padding: 2px;").
			Child(label).
			Child(ulNav))
}

// NodeAdminURL returns the nodeadmin base URL injected into the
// request context by Handle, defaulting to /nodes.
func NodeAdminURL(r *http.Request) string {
	if r == nil {
		return "/nodes"
	}
	if v, ok := r.Context().Value(KeyNodeAdminURL).(string); ok && v != "" {
		return v
	}
	return "/nodes"
}
