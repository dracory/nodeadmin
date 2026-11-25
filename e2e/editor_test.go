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

	// Vue mounted: no workflow is open yet, so the workspace is
	// hidden and the welcome screen shows.
	waitElement(t, page, ".editor-welcome")

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

// TestEditorEmptyState covers the welcome screen:
// with no workflow selected the editor workspace (tabs, toolbar,
// canvas) is hidden and the welcome screen fills the pane instead.
// Creating a workflow restores the workspace; closing the last tab
// returns to the welcome screen.
func TestEditorEmptyState(t *testing.T) {
	srv := newTestServer(t, t.TempDir()+"/e2e-editor-empty.db")
	defer srv.close()
	browser := newBrowser(t)

	page, w := mustPage(t, browser, srv.browserURL("/?controller=editor"))
	waitVueApp(t, page)

	workspaceHidden := `getComputedStyle(document.querySelector('.editor-workspace')).display === 'none'`

	// Empty state: welcome visible, workspace hidden, no open tabs.
	waitElement(t, page, ".editor-welcome")
	require.True(t, evalBool(t, page, workspaceHidden),
		"editor workspace must be hidden with no selected workflow")
	require.Zero(t, elementsLen(t, page, ".flow-tabs .flow-tab:not(.flow-tab-add)"),
		"no flow tab may be open without a selected workflow")

	// Create a workflow: the workspace returns with a flow tab.
	click(t, page, `button[title="New workflow or workflow group"]`)
	clickText(t, page, ".dropdown-item", "New workflow")
	setInput(t, page, "#newWorkflowModal input", "E2E Empty State")
	click(t, page, "#newWorkflowModal .btn-primary")

	waitElement(t, page, ".flow-tabs .flow-tab:not(.flow-tab-add)")
	require.False(t, evalBool(t, page, workspaceHidden),
		"editor workspace must be visible once a workflow is open")
	require.True(t, evalBool(t, page,
		`getComputedStyle(document.querySelector('.editor-welcome')).display === 'none'`),
		"welcome screen must hide once a workflow is open")

	// Closing the last tab returns to the welcome screen.
	click(t, page, ".flow-tabs .flow-tab .flow-tab-close")
	waitElement(t, page, ".editor-welcome")
	require.True(t, evalBool(t, page, workspaceHidden),
		"editor workspace must hide again after the last tab closes")

	if errs := w.errors(); len(errs) > 0 {
		t.Fatalf("console errors on editor: %v", errs)
	}
}

// TestEditorWorkflowDeepLink covers shareable workflow URLs:
// selecting a workflow writes ?workflow=<id> into the address bar,
// and opening that URL in a fresh page auto-selects the workflow
// (workspace + tab, no welcome screen).
func TestEditorWorkflowDeepLink(t *testing.T) {
	srv := newTestServer(t, t.TempDir()+"/e2e-editor-deep-link.db")
	defer srv.close()
	browser := newBrowser(t)

	// Deep-link target created straight through the API.
	created := apiPost(t, srv.srv.URL, "/api/workflow-create",
		map[string]any{"name": "E2E Deep Link", "nodes": []any{}, "edges": []any{}})
	wf, _ := created["workflow"].(map[string]any)
	require.NotNil(t, wf, "workflow-create should return the workflow")
	id, _ := wf["id"].(string)
	require.NotEmpty(t, id)

	page, w := mustPage(t, browser, srv.browserURL("/?controller=editor"))
	waitVueApp(t, page)
	waitElement(t, page, ".editor-welcome")

	// Selecting the workflow in the explorer writes the URL param.
	clickText(t, page, ".module-item.workflow-row", "E2E Deep Link")
	waitElement(t, page, ".flow-tabs .flow-tab:not(.flow-tab-add)")
	require.True(t, evalBool(t, page,
		`new URLSearchParams(location.search).get('workflow') === `+js(id)),
		"selecting a workflow must write ?workflow=<id> into the URL")

	// A fresh page on that URL auto-selects the workflow: the
	// workspace opens without any explorer click.
	require.NoError(t, chromedp.Run(page,
		chromedp.Navigate(srv.browserURL("/?controller=editor&workflow="+id))))
	waitVueApp(t, page)
	waitElement(t, page, ".flow-tabs .flow-tab:not(.flow-tab-add)")
	require.False(t, evalBool(t, page,
		`getComputedStyle(document.querySelector('.editor-workspace')).display === 'none'`),
		"deep link must open the workspace without explorer interaction")

	if errs := w.errors(); len(errs) > 0 {
		t.Fatalf("console errors on editor: %v", errs)
	}
}

// TestEditorPaletteGrouping covers the Node-RED-style grouped palette:
// the Add Node modal renders category headers (name + count), groups
// collapse on header click, and filtering hides empty groups while
// keeping headers for surviving ones.
func TestEditorPaletteGrouping(t *testing.T) {
	srv := newTestServer(t, t.TempDir()+"/e2e-editor-palette.db")
	defer srv.close()
	browser := newBrowser(t)

	created := apiPost(t, srv.srv.URL, "/api/workflow-create",
		map[string]any{"name": "E2E Palette", "nodes": []any{}, "edges": []any{}})
	wf, _ := created["workflow"].(map[string]any)
	require.NotNil(t, wf)
	id, _ := wf["id"].(string)
	require.NotEmpty(t, id)

	page, w := mustPage(t, browser, srv.browserURL("/?controller=editor&workflow="+id))
	waitVueApp(t, page)

	click(t, page, `button[title="Add node"]:not(:disabled)`)
	waitElement(t, page, "#addNodeModal .palette-group-header")

	total := elementsLen(t, page, "#addNodeModal .palette-group-header")
	require.Greater(t, total, 1, "palette should render multiple category groups")

	// Collapse the first group: its items hide, the header stays.
	firstItems := `[...document.querySelectorAll('#addNodeModal .node-type-item')].filter(e => e.offsetParent !== null).length`
	before := 0
	require.NoError(t, eval(page, firstItems, &before))
	click(t, page, "#addNodeModal .palette-group-header")
	var after int
	require.NoError(t, eval(page, firstItems, &after))
	require.Less(t, after, before, "collapsing a group must hide its items")

	// Re-open it, then filter: headers survive where matches exist,
	// empty groups disappear.
	click(t, page, "#addNodeModal .palette-group-header")
	setInput(t, page, "#addNodeModal input", "debug")
	filtered := elementsLen(t, page, "#addNodeModal .palette-group-header")
	require.Greater(t, filtered, 0, "headers remain for groups with matches")
	require.Less(t, filtered, total, "empty groups are hidden while filtering")
	require.Positive(t, elementsLen(t, page, "#addNodeModal .node-type-item"),
		"filtering by a type name must still list items")

	if errs := w.errors(); len(errs) > 0 {
		t.Fatalf("console errors on editor: %v", errs)
	}
}
