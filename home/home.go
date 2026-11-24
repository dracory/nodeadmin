package home

import (
	_ "embed"
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/dracory/cdn"

	"github.com/dracory/nodeadmin/shared"
	"github.com/dracory/nodestore"
)

//go:embed home.html
var homeHTML string

// dashboardCSS mirrors flowadmin's dashboard styles (stat-card accent,
// full-height flex shell)
const dashboardCSS = `.dashboard-card {
            transition: transform 0.2s, box-shadow 0.2s;
            height: 100%;
        }
        .dashboard-card:hover {
            transform: translateY(-5px);
            box-shadow: 0 4px 8px rgba(0,0,0,0.1);
        }
        .stat-card {
            background: var(--bs-tertiary-bg);
            border-left: 4px solid #0d6efd;
        }
        .app-container {
            display: flex;
            flex-direction: column;
            min-height: 100vh;
        }
        main {
            flex: 1;
        }`

// Section is the home dashboard section
type Section struct {
	uiConfig shared.UiConfig
}

// UI builds the home section bound to the shared config
func UI(uiConfig shared.UiConfig) *Section {
	return &Section{uiConfig: uiConfig}
}

// Home renders the dashboard — same structure as flowadmin's home,
// populated from the nodestore
func (s *Section) Home(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	workflows, werr := s.uiConfig.Store.WorkflowList(ctx, nodestore.WorkflowQuery())
	workflowCount, _ := s.uiConfig.Store.WorkflowCount(ctx, nodestore.WorkflowQuery())
	messageCount, _ := s.uiConfig.Store.MessageCount(ctx, nodestore.MessageQuery())

	deployed, _ := s.uiConfig.Store.WorkflowCount(ctx, nodestore.WorkflowQuery().WithDeployed(true))

	nodeCount := 0
	rows := strings.Builder{}
	activity := strings.Builder{}

	if werr != nil {
		rows.WriteString(`<tr><td colspan="5"><em>` + html.EscapeString(werr.Error()) + `</em></td></tr>`)
	} else if len(workflows) == 0 {
		rows.WriteString(`<tr><td colspan="5" class="text-muted">No workflows yet</td></tr>`)
		activity.WriteString(activityItem("No activity", "Stored workflows will appear here", "just now"))
	}

	for _, wf := range workflows {
		nodeCount += len(wf.GetNodes())

		status := `<span class="badge bg-secondary">Draft</span>`
		if wf.GetDeployed() {
			status = `<span class="badge bg-success">Deployed</span>`
		}
		rows.WriteString(`<tr><td>` + html.EscapeString(wf.GetName()) + `</td>` +
			`<td>` + status + `</td>` +
			`<td>` + strconv.Itoa(len(wf.GetNodes())) + `</td>` +
			`<td>` + strconv.Itoa(len(wf.GetEdges())) + `</td>` +
			`<td><div class="btn-group btn-group-sm">` +
			`<a href="?controller=editor" class="btn btn-outline-primary">Edit</a>` +
			`</div></td></tr>`)

		activity.WriteString(activityItem(
			"Workflow "+map[bool]string{true: "deployed", false: "stored"}[wf.GetDeployed()],
			wf.GetName(),
			wf.GetUpdatedAt().Format("2006-01-02 15:04"),
		))
	}

	page := homeHTML
	page = strings.ReplaceAll(page, "{{WORKFLOW_COUNT}}", strconv.FormatInt(workflowCount, 10))
	page = strings.ReplaceAll(page, "{{DEPLOYED_COUNT}}", strconv.FormatInt(deployed, 10))
	page = strings.ReplaceAll(page, "{{MESSAGE_COUNT}}", strconv.FormatInt(messageCount, 10))
	page = strings.ReplaceAll(page, "{{NODE_COUNT}}", strconv.Itoa(nodeCount))
	page = strings.ReplaceAll(page, "{{WORKFLOW_ROWS}}", rows.String())
	page = strings.ReplaceAll(page, "{{ACTIVITY_ITEMS}}", activity.String())
	page = strings.Replace(page, `<main class="container">`,
		`<main class="container">`+shared.SectionNav(r, s.uiConfig.Store).ToHTML(), 1)

	s.uiConfig.Layout(w, r, "Dashboard", page, shared.LayoutOptions{
		Store:     s.uiConfig.Store,
		Styles:    []string{dashboardCSS},
		StyleURLs: []string{cdn.BootstrapIconsCss_1_13_1()},
	})
}

func activityItem(title, body, when string) string {
	return `<div class="list-group-item">
	<div class="d-flex w-100 justify-content-between">
		<h6 class="mb-1">` + html.EscapeString(title) + `</h6>
		<small class="text-muted">` + html.EscapeString(when) + `</small>
	</div>
	<p class="mb-1">` + html.EscapeString(body) + `</p>
</div>`
}
