//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"
)

// TestEditorCreateSavePersist covers scenario (b): the editor loads,
// the Add Node palette lists node types, a created workflow accepts a
// node, saves, and the node survives a reload (persistence roundtrip).
func TestEditorCreateSavePersist(t *testing.T) {
	srv := newTestServer(t, t.TempDir()+"/e2e-editor.db")
	defer srv.close()
	browser := newBrowser(t)

	page, w := mustPage(t, browser, srv.browserURL("/?controller=editor"))
	waitVueApp(t, page)

	// Vue mounted: the explorer tree and toolbar rendered.
	waitElement(t, page, ".editor-toolbar")

	// New workflow via the explorer "+" dropdown (JS clicks —
	// chromedp's pointer click waits for scroll-into-view stability
	// the small icon buttons can flap on under headless, and the
	// Bootstrap dropdown visibility is not needed for Vue's @click
	// handler).
	click(t, page, `button[title="New workflow or workflow group"]`)
	clickText(t, page, ".dropdown-item", "New workflow")
	setInput(t, page, "#newWorkflowModal input", "E2E Flow")
	click(t, page, "#newWorkflowModal .btn-primary")

	// The workflow is selected: the Add Node button becomes enabled.
	click(t, page, `button[title="Add node"]:not(:disabled)`)

	// Palette lists registered node types.
	cctx, cancel := context.WithTimeout(page, 45*time.Second)
	if err := chromedp.Run(cctx,
		chromedp.WaitVisible("#addNodeModal.show .node-type-item", chromedp.ByQuery)); err != nil {
		cancel()
		var state string
		_ = eval(page, `JSON.stringify({
			bootstrap: typeof bootstrap,
			modalClass: document.querySelector('#addNodeModal')?.className,
			items: document.querySelectorAll('#addNodeModal .node-type-item').length,
			listText: document.querySelector('#addNodeModal .modal-body')?.innerText?.slice(0, 300),
		})`, &state)
		t.Logf("palette wait failed; page state: %v; console errors: %v", state, w.errors())
		t.Fatalf("palette did not open: %v", err)
	}
	cancel()
	require.Positive(t, elementsLen(t, page, "#addNodeModal .node-type-item"),
		"palette should list node types")

	// Add the first node type to the canvas (fresh querySelector —
	// Vue re-renders the list when the filter model updates).
	click(t, page, "#addNodeModal .node-type-item")
	waitElement(t, page, ".drawflow-node")

	// Save — the badge confirms the roundtrip completed.
	click(t, page, `button[title="Save workflow"]:not(:disabled)`)
	waitElementText(t, page, ".editor-toolbar .badge.bg-success", "Saved")

	// Persisted server-side: fetch the workflow list and require a
	// stored node on the workflow the editor created.
	data := apiPost(t, srv.srv.URL, "/api/workflow-fetch-all", map[string]any{})
	workflows, ok := data["workflows"].([]any)
	require.True(t, ok)
	require.Len(t, workflows, 1)
	nodes, _ := workflows[0].(map[string]any)["nodes"].([]any)
	require.NotEmpty(t, nodes, "saved workflow should persist its node")

	// And a fresh editor load still shows the workflow in the tree.
	require.NoError(t, chromedp.Run(page,
		chromedp.Navigate(srv.browserURL("/?controller=editor"))))
	waitVueApp(t, page)
	waitElementText(t, page, ".module-item", "E2E Flow")

	if errs := w.errors(); len(errs) > 0 {
		t.Fatalf("console errors on editor: %v", errs)
	}
}
