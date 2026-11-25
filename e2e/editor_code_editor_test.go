//go:build e2e

package e2e

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEditorFunctionCodeEditor covers the format:"code" schema
// extension: the function node's dialog mounts a CodeMirror per code
// field under a Node-RED-style tab strip (On Start / On Message /
// On Stop), edits sync back into the form, and saveNodeProperties
// round-trips the script bodies into node.data.
func TestEditorFunctionCodeEditor(t *testing.T) {
	srv := newTestServer(t, t.TempDir()+"/e2e-code-editor.db")
	defer srv.close()
	browser := newBrowser(t)

	created := apiPost(t, srv.srv.URL, "/api/workflow-create",
		map[string]any{"name": "E2E Code Editor", "nodes": []any{}, "edges": []any{}})
	wf, _ := created["workflow"].(map[string]any)
	require.NotNil(t, wf)
	id, _ := wf["id"].(string)
	require.NotEmpty(t, id)

	page, w := mustPage(t, browser, srv.browserURL("/?controller=editor&workflow="+id))
	waitVueApp(t, page)

	vm := `document.getElementById('app').__vue_app__._instance.proxy`

	waitExpr(t, page,
		`(function(){ const vm = `+vm+`; return vm.selectedWorkflow && String(vm.selectedWorkflow.id) === `+js(id)+`; })()`,
		"workflow to be selected")

	// The vendored bundle must have loaded — the fallback path is the
	// bound textarea and is not what this test verifies.
	require.True(t, evalBool(t, page, `typeof CodeMirror !== 'undefined'`),
		"vendored CodeMirror bundle must load")

	var fnID any
	require.NoError(t, eval(page, `(function(){
		const vm = `+vm+`;
		const n = vm.addNode('function', 'fn', 120, 120);
		try { vm.openNodeProperties(n); } catch (e) { return 'open: '+e.stack; }
		return n;
	})()`, &fnID))

	// Schema fetch + editor init are async — wait for the three
	// code-field CodeMirror instances (func, initialize, finalize).
	waitExpr(t, page,
		`(function(){ const vm = `+vm+`;
			return vm.codeFields.length === 3 &&
				!!(vm.codeEditorInst.func && vm.codeEditorInst.initialize && vm.codeEditorInst.finalize); })()`,
		"code editors to mount")

	// Tabs render in codeTabOrder (On Start / On Message / On Stop) and
	// the dialog opens on the required body field (On Message).
	require.True(t, evalBool(t, page, `(function(){
		const vm = `+vm+`;
		const labels = vm.codeFields.map(f => f.codeTabLabel);
		return labels.join(',') === 'On Start,On Message,On Stop' &&
			vm.codeTabActive(vm.codeFields[1]); })()`),
		"tabs must order On Start,On Message,On Stop and open on On Message")

	// Edit the body through CodeMirror, switch to On Start, edit that,
	// then save via the real path — values must land on node.data.
	var failure string
	require.NoError(t, eval(page, `(function(){
		const vm = `+vm+`;
		try {
			vm.codeEditorInst.func.setValue("msg.payload = 42;\nreturn msg;");
			const onStart = vm.editingNodeSchemaFields.find(f => f.key === 'initialize');
			vm.selectCodeTab(onStart);
			vm.codeEditorInst.initialize.setValue("context.set('booted', true);");
			vm.saveNodeProperties();
		} catch (e) { return 'save failed: ' + (e && e.stack || e); }
		return ''; })()`, &failure))
	require.Empty(t, failure)

	require.True(t, evalBool(t, page, `(function(){
		const vm = `+vm+`;
		const data = vm.editor.getNodeFromId(`+js(fmt.Sprint(fnID))+`).data;
		return data.func === "msg.payload = 42;\nreturn msg;" &&
			data.initialize === "context.set('booted', true);"; })()`),
		"saveNodeProperties must round-trip CodeMirror values into node.data")

	if errs := w.errors(); len(errs) > 0 {
		t.Fatalf("console errors on editor: %v", errs)
	}
}
