package messages

import (
	"html"
	"net/http"

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

// Messages renders the stored message list inside the same
// container/card chrome as the flowadmin pages
func (s *Section) Messages(w http.ResponseWriter, r *http.Request) {
	list, err := s.uiConfig.Store.MessageList(r.Context(), nodestore.MessageQuery())

	rows := ""
	if err != nil {
		rows = `<tr><td colspan="3"><em>` + html.EscapeString(err.Error()) + `</em></td></tr>`
	} else if len(list) == 0 {
		rows = `<tr><td colspan="3" class="text-muted">No messages yet</td></tr>`
	}
	for _, msg := range list {
		hasError := `<span class="badge bg-secondary">No</span>`
		if msg.GetError() != nil {
			hasError = `<span class="badge bg-danger">Yes</span>`
		}
		rows += `<tr><td>` + html.EscapeString(msg.GetID()) + `</td>` +
			`<td>` + hasError + `</td>` +
			`<td>` + html.EscapeString(msg.GetTimestamp().Format("2006-01-02 15:04:05")) + `</td></tr>`
	}

	htmlOut := `<main class="container">` + shared.SectionNav(r, s.uiConfig.Store).ToHTML() + `
	<div class="d-flex justify-content-between align-items-center mb-4">
		<h1>Messages</h1>
	</div>
	<div class="card">
		<div class="card-body p-0">
			<div class="table-responsive">
				<table class="table table-hover mb-0">
					<thead><tr><th>ID</th><th>Has Error</th><th>Timestamp</th></tr></thead>
					<tbody>` + rows + `</tbody>
				</table>
			</div>
		</div>
	</div>
</main>`

	s.uiConfig.Layout(w, r, "Messages", htmlOut, shared.LayoutOptions{
		Store:     s.uiConfig.Store,
		StyleURLs: []string{cdn.BootstrapIconsCss_1_13_1()},
	})
}
