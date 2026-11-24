package workflows

import (
	"html"
	"net/http"
	"strconv"

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

// Workflows renders the stored workflow list inside the same
// container/card chrome as the flowadmin pages
func (s *Section) Workflows(w http.ResponseWriter, r *http.Request) {
	list, err := s.uiConfig.Store.WorkflowList(r.Context(), nodestore.WorkflowQuery())

	rows := ""
	if err != nil {
		rows = `<tr><td colspan="7"><em>` + html.EscapeString(err.Error()) + `</em></td></tr>`
	} else if len(list) == 0 {
		rows = `<tr><td colspan="7" class="text-muted">No workflows yet</td></tr>`
	}
	for _, wf := range list {
		deployed := `<span class="badge bg-secondary">Draft</span>`
		if wf.GetDeployed() {
			deployed = `<span class="badge bg-success">Deployed</span>`
		}
		rows += `<tr><td>` + html.EscapeString(wf.GetID()) + `</td>` +
			`<td>` + html.EscapeString(wf.GetName()) + `</td>` +
			`<td>` + deployed + `</td>` +
			`<td>` + strconv.Itoa(len(wf.GetNodes())) + `</td>` +
			`<td>` + strconv.Itoa(len(wf.GetEdges())) + `</td>` +
			`<td>` + html.EscapeString(wf.GetUpdatedAt().Format("2006-01-02 15:04")) + `</td>` +
			`<td><a href="?controller=editor" class="btn btn-sm btn-outline-primary">Edit</a></td></tr>`
	}

	htmlOut := `<main class="container">` + shared.SectionNav(r, s.uiConfig.Store).ToHTML() + `
	<div class="d-flex justify-content-between align-items-center mb-4">
		<h1>Workflows</h1>
	</div>
	<div class="card">
		<div class="card-body p-0">
			<div class="table-responsive">
				<table class="table table-hover mb-0">
					<thead><tr><th>ID</th><th>Name</th><th>Status</th><th>Nodes</th><th>Edges</th><th>Updated</th><th></th></tr></thead>
					<tbody>` + rows + `</tbody>
				</table>
			</div>
		</div>
	</div>
</main>`

	s.uiConfig.Layout(w, r, "Workflows", htmlOut, shared.LayoutOptions{
		Store:     s.uiConfig.Store,
		StyleURLs: []string{cdn.BootstrapIconsCss_1_13_1()},
	})
}
