package shared

import (
	"net/http"

	"github.com/dracory/hb"

	"github.com/dracory/nodestore"
)

// SectionNav renders the node admin section navigation as the mono
// "NODES ›" pill strip. The active tab is derived from the
// ?controller= query parameter. Workflows, Nodes and Messages tabs
// show record counts as badges when the store is available.
func SectionNav(r *http.Request, store nodestore.StoreInterface) hb.TagInterface {
	active := activeController(r)

	items := navItems(r, store)
	tabs := make([]hb.TagInterface, 0, len(items))
	for _, item := range items {
		tabs = append(tabs, hb.LI().Class("nav-item").Child(navLink(item, active)))
	}

	ulNav := hb.UL().
		Class("nav").
		Children(tabs)

	label := hb.Span().
		Class("qn-label").
		HTML(`NODES&nbsp;<i class="bi bi-chevron-right"></i>`)

	return hb.Nav().
		Class("na-quicknav d-flex align-items-center px-2 py-1 mb-4").
		Child(label).
		Child(ulNav)
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
