package home

import (
	_ "embed"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dracory/cdn"

	"github.com/dracory/nodeadmin/shared"
	"github.com/dracory/nodestore"
)

//go:embed home.html
var homeHTML string

// Section is the home dashboard section
type Section struct {
	uiConfig shared.UiConfig
}

// UI builds the home section bound to the shared config
func UI(uiConfig shared.UiConfig) *Section {
	return &Section{uiConfig: uiConfig}
}

// Home renders the dashboard — stat cards, a per-day message
// activity chart, a typed activity feed and the deployed-workflows
// strip, all populated from the store.
func (s *Section) Home(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	workflows, werr := s.uiConfig.Store.WorkflowList(ctx, nodestore.WorkflowQuery())
	workflowCount, _ := s.uiConfig.Store.WorkflowCount(ctx, nodestore.WorkflowQuery())
	messageCount, _ := s.uiConfig.Store.MessageCount(ctx, nodestore.MessageQuery())
	messages, _ := s.uiConfig.Store.MessageList(ctx, nodestore.MessageQuery().WithLimit(500))

	deployed, _ := s.uiConfig.Store.WorkflowCount(ctx, nodestore.WorkflowQuery().WithDeployed(true))

	nodeCount := 0
	rows := strings.Builder{}
	activity := strings.Builder{}

	if werr != nil {
		rows.WriteString(`<tr><td colspan="5"><em>` + html.EscapeString(werr.Error()) + `</em></td></tr>`)
	} else if len(workflows) == 0 {
		rows.WriteString(`<tr><td colspan="5" class="text-muted">No workflows yet</td></tr>`)
		activity.WriteString(activityItem("", "No activity", "Stored workflows will appear here", "just now"))
	}

	for _, wf := range workflows {
		nodeCount += len(wf.GetNodes())

		status := `<span class="pill pill-draft">draft</span>`
		kind := "store"
		title := "Workflow stored"
		if wf.GetDeployed() {
			status = `<span class="pill pill-deployed">deployed</span>`
			kind = "deploy"
			title = "Workflow deployed"
		}
		rows.WriteString(`<tr><td><div class="d-flex align-items-center gap-3">` +
			minimap(wf) +
			`<div><div class="fw-semibold">` + html.EscapeString(wf.GetName()) + `</div>` +
			`<div class="text-secondary small na-mono">` + html.EscapeString(shortID(wf.GetID())) + `</div></div>` +
			`</div></td>` +
			`<td>` + status + `</td>` +
			`<td class="na-mono small">` + strconv.Itoa(len(wf.GetNodes())) + ` nodes · ` + strconv.Itoa(len(wf.GetEdges())) + ` edges</td>` +
			`<td class="text-secondary small">` + html.EscapeString(wf.GetUpdatedAt().Format("2006-01-02 15:04")) + `</td>` +
			`<td class="text-end"><a href="?controller=editor&workflow=` + url.QueryEscape(wf.GetID()) + `" class="btn btn-sm btn-outline-secondary">Edit</a></td></tr>`)

		activity.WriteString(activityItem(kind, title,
			wf.GetName()+" — "+strconv.Itoa(len(wf.GetNodes()))+" nodes, "+strconv.Itoa(len(wf.GetEdges()))+" edges",
			wf.GetUpdatedAt().Format("2006-01-02 15:04")))
	}

	// Errors recorded on stored messages surface in the feed.
	for _, msg := range messages {
		if msg.GetError() != nil {
			activity.WriteString(activityItem("error", "Message error",
				*msg.GetError(),
				msg.GetTimestamp().Format("2006-01-02 15:04")))
		}
	}

	page := homeHTML
	page = strings.ReplaceAll(page, "{{WORKFLOW_COUNT}}", strconv.FormatInt(workflowCount, 10))
	page = strings.ReplaceAll(page, "{{DEPLOYED_COUNT}}", strconv.FormatInt(deployed, 10))
	page = strings.ReplaceAll(page, "{{DRAFT_COUNT}}", strconv.FormatInt(workflowCount-deployed, 10))
	page = strings.ReplaceAll(page, "{{MESSAGE_COUNT}}", strconv.FormatInt(messageCount, 10))
	page = strings.ReplaceAll(page, "{{NODE_COUNT}}", strconv.Itoa(nodeCount))
	page = strings.ReplaceAll(page, "{{WORKFLOW_ROWS}}", rows.String())
	page = strings.ReplaceAll(page, "{{ACTIVITY_ITEMS}}", activity.String())
	page = strings.ReplaceAll(page, "{{ACTIVITY_CHART}}", activityChart(messages))
	page = strings.Replace(page, `<main class="container">`,
		`<main class="container">`+shared.SectionNav(r, s.uiConfig.Store).ToHTML(), 1)

	s.uiConfig.Layout(w, r, "Dashboard", page, shared.LayoutOptions{
		Store:     s.uiConfig.Store,
		StyleURLs: []string{cdn.BootstrapIconsCss_1_13_1()},
	})
}

func activityItem(kind, title, body, when string) string {
	class := ""
	if kind != "" {
		class = ` ` + kind
	}
	return `<div class="feed-item` + class + ` pb-3">
	<div class="d-flex justify-content-between">
		<strong>` + html.EscapeString(title) + `</strong>
		<span class="text-secondary small na-mono">` + html.EscapeString(when) + `</span>
	</div>
	<div class="text-secondary small">` + html.EscapeString(body) + `</div>
</div>`
}

// activityChart renders the CSS bar chart from the stored message
// timestamps: one bar per day over the last 14 days, height scaled
// to the busiest day. Days with no traffic render dim.
func activityChart(messages []nodestore.MessageInterface) string {
	const days = 14
	buckets := make([]int, days)
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	max := 0
	for _, msg := range messages {
		t := msg.GetTimestamp()
		d := int(today.Sub(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())).Hours() / 24)
		if d >= 0 && d < days {
			buckets[days-1-d]++
			if buckets[days-1-d] > max {
				max = buckets[days-1-d]
			}
		}
	}
	out := strings.Builder{}
	for _, n := range buckets {
		height := 2
		if n > 0 && max > 0 {
			height = 2 + (n * 96 / max)
		}
		class := "bar"
		if n == 0 {
			class = "bar dim"
		}
		out.WriteString(`<div class="` + class + `" style="height:` + strconv.Itoa(height) + `%" title="` +
			strconv.Itoa(n) + ` messages"></div>`)
	}
	return out.String()
}

func minimap(wf nodestore.WorkflowInterface) string {
	return shared.WorkflowMinimap(wf)
}

func shortID(id string) string {
	return shared.ShortID(id)
}
