package messages

import (
	"encoding/json"
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

// Section is the messages list section
type Section struct {
	uiConfig shared.UiConfig
}

// UI builds the messages section bound to the shared config
func UI(uiConfig shared.UiConfig) *Section {
	return &Section{uiConfig: uiConfig}
}

// Messages renders the stored message list beside a detail pane.
// Selecting a row (?id=) loads that message's payload, metadata and
// error verbatim in the inspector pane — the store carries no
// per-hop trace, so the pane shows what the entity persists.
func (s *Section) Messages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	store := s.uiConfig.Store

	list, err := store.MessageList(ctx, nodestore.MessageQuery().WithLimit(200))
	selectedID := strings.TrimSpace(r.URL.Query().Get("id"))

	var selected nodestore.MessageInterface
	if selectedID != "" {
		if found, ferr := store.MessageList(ctx, nodestore.MessageQuery().WithID(selectedID)); ferr == nil && len(found) > 0 {
			selected = found[0]
		}
	}

	rows := strings.Builder{}
	if err != nil {
		rows.WriteString(`<tr><td colspan="3"><em>` + html.EscapeString(err.Error()) + `</em></td></tr>`)
	} else if len(list) == 0 {
		rows.WriteString(`<tr><td colspan="3" class="text-muted">No messages yet</td></tr>`)
	}
	for _, msg := range list {
		hasError := `<span class="pill pill-deployed">stored</span>`
		if msg.GetError() != nil {
			hasError = `<span class="pill pill-error">error</span>`
		}
		active := ""
		if selected != nil && msg.GetID() == selected.GetID() {
			active = ` class="table-active"`
		}
		rows.WriteString(`<tr` + active + `>` +
			`<td class="trace-id"><a class="text-body text-decoration-none" href="?controller=` +
			shared.CONTROLLER_MESSAGES + `&id=` + url.QueryEscape(msg.GetID()) + `">` +
			html.EscapeString(shared.ShortID(msg.GetID())) + `</a></td>` +
			`<td>` + hasError + `</td>` +
			`<td class="text-secondary small">` + html.EscapeString(age(msg.GetTimestamp())) + `</td></tr>`)
	}

	detail := `<div class="p-3 text-secondary small">Select a message to inspect its payload.</div>`
	if selected != nil {
		payloadJSON, _ := json.MarshalIndent(selected.GetPayload(), "", "  ")
		metadataJSON, _ := json.MarshalIndent(selected.GetMetadata(), "", "  ")
		errorText := `<span class="pill pill-deployed">none</span>`
		if selected.GetError() != nil {
			errorText = `<span class="pill pill-error">error</span><div class="msg-json mt-2">` +
				html.EscapeString(*selected.GetError()) + `</div>`
		}
		detail = `<div class="p-3">` +
			`<div class="d-flex justify-content-between mb-2">` +
			`<span class="node-chip"><span class="dot"></span>message</span>` +
			`<span class="text-secondary small na-mono">` + html.EscapeString(selected.GetTimestamp().Format("2006-01-02 15:04:05")) + `</span>` +
			`</div>` +
			`<div class="text-secondary small na-mono mb-3">` + html.EscapeString(selected.GetID()) + `</div>` +
			`<div class="mb-3"><div class="small fw-semibold mb-1">Error</div>` + errorText + `</div>` +
			`<div class="mb-3"><div class="small fw-semibold mb-1">Payload</div>` +
			`<pre class="msg-json mb-0">` + html.EscapeString(string(payloadJSON)) + `</pre></div>` +
			`<div><div class="small fw-semibold mb-1">Metadata</div>` +
			`<pre class="msg-json mb-0">` + html.EscapeString(string(metadataJSON)) + `</pre></div>` +
			`</div>`
	}

	htmlOut := `<main class="container">` + shared.SectionNav(r, store).ToHTML() + `
	<div class="page-head d-flex align-items-end justify-content-between mb-4">
		<div>
			<div class="crumb">Messages</div>
			<h1 class="h3 mb-0">Message inspector</h1>
		</div>
	</div>
	<div class="row g-3">
		<div class="col-lg-7">
			<div class="na-section">
				<div class="head">Recent messages</div>
				<div class="table-responsive">
					<table class="table na-table align-middle mb-0">
						<thead><tr><th>ID</th><th>Status</th><th>Age</th></tr></thead>
						<tbody>` + rows.String() + `</tbody>
					</table>
				</div>
			</div>
		</div>
		<div class="col-lg-5">
			<div class="na-section">
				<div class="head">Message detail</div>
				` + detail + `
			</div>
		</div>
	</div>
</main>`

	s.uiConfig.Layout(w, r, "Messages", htmlOut, shared.LayoutOptions{
		Store:     store,
		StyleURLs: []string{cdn.BootstrapIconsCss_1_13_1()},
	})
}

// age renders a timestamp as a compact "time ago" string.
func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	default:
		return t.Format("2006-01-02 15:04")
	}
}
