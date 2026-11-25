package workflows

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

// Section is the workflows list section
type Section struct {
	uiConfig shared.UiConfig
}

// UI builds the workflows section bound to the shared config
func UI(uiConfig shared.UiConfig) *Section {
	return &Section{uiConfig: uiConfig}
}

// Workflows renders the stored workflows as a single tree-list:
// workflow-group rows (aggregate status counts) contain indented
// workflow rows; ungrouped workflows render at the top level.
func (s *Section) Workflows(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	store := s.uiConfig.Store

	list, err := store.WorkflowList(ctx, nodestore.WorkflowQuery())
	groups, _ := store.WorkflowGroupList(ctx, nodestore.WorkflowGroupQuery())

	byID := map[string]nodestore.WorkflowInterface{}
	grouped := map[string]bool{}
	for _, wf := range list {
		byID[wf.GetID()] = wf
	}

	rows := strings.Builder{}
	if err != nil {
		rows.WriteString(`<tr><td colspan="5"><em>` + html.EscapeString(err.Error()) + `</em></td></tr>`)
	} else if len(list) == 0 {
		rows.WriteString(`<tr><td colspan="5" class="text-muted">No workflows yet</td></tr>`)
	}

	// Nested groups render under their parent; membership is tracked
	// so workflows in several groups don't duplicate at top level.
	var renderGroup func(g nodestore.WorkflowGroupInterface, depth int)
	renderGroup = func(g nodestore.WorkflowGroupInterface, depth int) {
		memberIDs := g.GetWorkflowIDs()
		deployed, draft := 0, 0
		for _, id := range memberIDs {
			if wf, ok := byID[id]; ok {
				grouped[id] = true
				if wf.GetDeployed() {
					deployed++
				} else {
					draft++
				}
			}
		}
		pills := ""
		if deployed > 0 {
			pills += `<span class="pill pill-deployed">` + strconv.Itoa(deployed) + ` deployed</span> `
		}
		if draft > 0 {
			pills += `<span class="pill pill-draft">` + strconv.Itoa(draft) + ` draft</span>`
		}
		indent := strconv.Itoa(depth * 24)
		rows.WriteString(`<tr style="background:var(--bs-tertiary-bg)">` +
			`<td><div class="d-flex align-items-center gap-2" style="padding-left:` + indent + `px">` +
			`<i class="bi bi-chevron-down small text-secondary"></i>` +
			`<i class="bi bi-folder2" style="color:var(--na-accent)"></i>` +
			`<span class="fw-semibold">` + html.EscapeString(g.GetName()) + `</span>` +
			`<span class="text-secondary small na-mono">group · ` + strconv.Itoa(len(memberIDs)) + ` flows</span>` +
			`</div></td>` +
			`<td>` + pills + `</td>` +
			`<td class="na-mono small">—</td>` +
			`<td class="text-secondary small">—</td>` +
			`<td></td></tr>`)
		for _, id := range memberIDs {
			if wf, ok := byID[id]; ok {
				rows.WriteString(workflowRow(wf, depth+1))
			}
		}
		for _, child := range groups {
			if child.GetParentID() == g.GetID() {
				renderGroup(child, depth+1)
			}
		}
	}
	for _, g := range groups {
		if g.GetParentID() == "" {
			renderGroup(g, 0)
		}
	}
	for _, wf := range list {
		if !grouped[wf.GetID()] {
			rows.WriteString(workflowRow(wf, 0))
		}
	}

	htmlOut := `<main class="container">` + shared.SectionNav(r, store).ToHTML() + `
	<div class="page-head d-flex align-items-end justify-content-between mb-4">
		<div>
			<div class="crumb">Workflows</div>
			<h1 class="h3 mb-0">All flows</h1>
		</div>
		<a href="?controller=` + shared.CONTROLLER_EDITOR + `" class="btn btn-sm btn-na-accent"><i class="bi bi-plus-lg"></i> New flow</a>
	</div>
	<div class="na-section">
		<div class="table-responsive">
			<table class="table na-table align-middle mb-0">
				<thead><tr><th style="width:42%">Name</th><th>Status</th><th>Topology</th><th>Updated</th><th class="text-end"></th></tr></thead>
				<tbody>` + rows.String() + `</tbody>
			</table>
		</div>
	</div>
</main>`

	s.uiConfig.Layout(w, r, "Workflows", htmlOut, shared.LayoutOptions{
		Store:     store,
		StyleURLs: []string{cdn.BootstrapIconsCss_1_13_1()},
	})
}

// workflowRow renders one workflow row, indented under its group.
func workflowRow(wf nodestore.WorkflowInterface, depth int) string {
	status := `<span class="pill pill-draft">draft</span>`
	if wf.GetDeployed() {
		status = `<span class="pill pill-deployed">deployed</span>`
	}
	indent := 24 * depth
	return `<tr>` +
		`<td><div class="d-flex align-items-center gap-3" style="padding-left:` + strconv.Itoa(indent) + `px">` +
		shared.WorkflowMinimap(wf) +
		`<div><div class="fw-semibold">` + html.EscapeString(wf.GetName()) + `</div>` +
		`<div class="text-secondary small na-mono">` + html.EscapeString(shared.ShortID(wf.GetID())) + `</div></div>` +
		`</div></td>` +
		`<td>` + status + `</td>` +
		`<td class="na-mono small">` + strconv.Itoa(len(wf.GetNodes())) + ` nodes · ` + strconv.Itoa(len(wf.GetEdges())) + ` edges</td>` +
		`<td class="text-secondary small">` + html.EscapeString(wf.GetUpdatedAt().Format("2006-01-02 15:04")) + `</td>` +
		`<td class="text-end"><a href="?controller=` + shared.CONTROLLER_EDITOR + `&workflow=` +
		url.QueryEscape(wf.GetID()) + `" class="btn btn-sm btn-outline-secondary">Edit</a></td></tr>`
}
