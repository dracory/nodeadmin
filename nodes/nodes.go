// Package nodes renders the all-node-instances list section — a
// flat inventory of every stored node entity across workflows, with
// its type, parent workflow, node group and wire counts, and an
// open-in-editor deep link.
package nodes

import (
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dracory/cdn"

	"github.com/dracory/nodeadmin/shared"
	"github.com/dracory/nodestore"
)

// Section is the nodes list section
type Section struct {
	uiConfig shared.UiConfig
}

// UI builds the nodes section bound to the shared config
func UI(uiConfig shared.UiConfig) *Section {
	return &Section{uiConfig: uiConfig}
}

// Nodes renders the stored node list inside the same chrome as the
// other sections. Wires are derived from the edge entities that
// reference each node as source or target.
func (s *Section) Nodes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	store := s.uiConfig.Store

	list, err := store.NodeList(ctx, nodestore.NodeQuery())
	workflows, _ := store.WorkflowList(ctx, nodestore.WorkflowQuery())
	groups, _ := store.NodeGroupList(ctx, nodestore.NodeGroupQuery())
	edges, _ := store.EdgeList(ctx, nodestore.EdgeQuery())

	workflowNames := map[string]string{}
	for _, wf := range workflows {
		workflowNames[wf.GetID()] = wf.GetName()
	}
	groupNames := map[string]string{}
	for _, g := range groups {
		groupNames[g.GetID()] = g.GetName()
	}
	wires := map[string]struct{ in, out int }{}
	for _, e := range edges {
		if c, ok := wires[e.GetSource()]; ok {
			c.out++
			wires[e.GetSource()] = c
		} else {
			wires[e.GetSource()] = struct{ in, out int }{out: 1}
		}
		if c, ok := wires[e.GetTarget()]; ok {
			c.in++
			wires[e.GetTarget()] = c
		} else {
			wires[e.GetTarget()] = struct{ in, out int }{in: 1}
		}
	}

	rows := strings.Builder{}
	if err != nil {
		rows.WriteString(`<tr><td colspan="6"><em>` + html.EscapeString(err.Error()) + `</em></td></tr>`)
	} else if len(list) == 0 {
		rows.WriteString(`<tr><td colspan="6" class="text-muted">No nodes yet</td></tr>`)
	}
	for _, n := range list {
		nodeType := n.GetType()
		label := nodeType
		if name, ok := n.GetData()["name"].(string); ok && name != "" {
			label = name
		}
		workflow := `<span class="text-secondary">—</span>`
		if name, ok := workflowNames[n.GetWorkflowID()]; ok {
			workflow = html.EscapeString(name)
		}
		group := `<span class="text-secondary">—</span>`
		if name, ok := groupNames[n.GetGroupID()]; ok {
			group = `<span class="node-chip"><i class="bi bi-collection"></i> ` + html.EscapeString(name) + `</span>`
		}
		wire := "—"
		if c, ok := wires[n.GetID()]; ok {
			parts := []string{}
			if c.in > 0 {
				parts = append(parts, strconv.Itoa(c.in)+" in")
			}
			if c.out > 0 {
				parts = append(parts, strconv.Itoa(c.out)+" out")
			}
			wire = strings.Join(parts, " · ")
		}
		rows.WriteString(`<tr>` +
			`<td><div class="d-flex align-items-center gap-2">` +
			`<span class="node-chip"><span class="dot"></span>` + html.EscapeString(nodeType) + `</span>` +
			`<div><div class="fw-semibold small">` + html.EscapeString(label) + `</div>` +
			`<div class="text-secondary small na-mono">` + html.EscapeString(shortID(n.GetID())) + `</div></div>` +
			`</div></td>` +
			`<td class="na-mono small">` + html.EscapeString(nodeType) + `</td>` +
			`<td class="small">` + workflow + `</td>` +
			`<td>` + group + `</td>` +
			`<td class="na-mono small">` + html.EscapeString(wire) + `</td>` +
			`<td class="text-end"><a class="btn btn-sm btn-outline-secondary" href="?controller=` +
			shared.CONTROLLER_EDITOR + `&workflow=` + url.QueryEscape(n.GetWorkflowID()) + `">Open in editor</a></td>` +
			`</tr>`)
	}

	htmlOut := `<main class="container">` + shared.SectionNav(r, store).ToHTML() + `
	<div class="page-head d-flex align-items-end justify-content-between mb-4">
		<div>
			<div class="crumb">Nodes</div>
			<h1 class="h3 mb-0">All node instances</h1>
		</div>
	</div>
	<div class="na-section mb-3">
		<div class="table-responsive">
			<table class="table na-table align-middle mb-0">
				<thead><tr><th>Node</th><th>Type</th><th>Workflow</th><th>Group</th><th>Wires</th><th class="text-end"></th></tr></thead>
				<tbody>` + rows.String() + `</tbody>
			</table>
		</div>
	</div>
</main>`

	s.uiConfig.Layout(w, r, "Nodes", htmlOut, shared.LayoutOptions{
		Store:     store,
		StyleURLs: []string{cdn.BootstrapIconsCss_1_13_1()},
	})
}

// shortID renders the entity ID in the compact form used in list
// columns (first 8 chars — enough to disambiguate visually).
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12] + "…"
	}
	return id
}
