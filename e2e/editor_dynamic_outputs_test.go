//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// waitExpr polls a page JS expression until it returns truthy.
func waitExpr(t *testing.T, ctx context.Context, expr, what string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if evalBool(t, ctx, expr) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestEditorSwitchDynamicOutputs covers config-driven output ports
// (task dynamic_output_ports): saving switch properties with more
// rules grows the node's Drawflow outputs in place; saving fewer
// rules shrinks them and drops wires on the removed ports (Node-RED
// behaviour — dangling wires are deleted, surviving ports keep their
// edges). The scenario drives the real save path
// (openNodeProperties -> saveNodeProperties -> syncDynamicOutputs)
// through the mounted Vue instance, so no reload is involved.
func TestEditorSwitchDynamicOutputs(t *testing.T) {
	srv := newTestServer(t, t.TempDir()+"/e2e-switch-ports.db")
	defer srv.close()
	browser := newBrowser(t)

	created := apiPost(t, srv.srv.URL, "/api/workflow-create",
		map[string]any{"name": "E2E Switch Ports", "nodes": []any{}, "edges": []any{}})
	wf, _ := created["workflow"].(map[string]any)
	require.NotNil(t, wf)
	id, _ := wf["id"].(string)
	require.NotEmpty(t, id)

	page, w := mustPage(t, browser, srv.browserURL("/?controller=editor&workflow="+id))
	waitVueApp(t, page)

	// Vue 3 stores the app on its mount element; the root component
	// proxy exposes the editor methods used below.
	vm := `document.getElementById('app').__vue_app__._instance.proxy`

	// The deep link selects the workflow asynchronously: nodes added
	// before changeModule/import finishes are wiped by the incoming
	// module data. Wait until the workflow is the selected one.
	waitExpr(t, page,
		`(function(){ const vm = `+vm+`; return vm.selectedWorkflow && String(vm.selectedWorkflow.id) === `+js(id)+`; })()`,
		"workflow to be selected")

	// A default switch node has two rules -> two outputs. Wire a debug
	// node onto its second (last) output so a later shrink must drop it.
	var ids map[string]any
	require.NoError(t, eval(page, `(function(){
		const vm = `+vm+`;
		const sw = vm.addNode('switch', 'sw', 120, 120);
		const dbg = vm.addNode('debug', 'dbg', 420, 120);
		vm.editor.addConnection(sw, dbg, 'output_2', 'input_1');
		return { sw: sw, dbg: dbg,
			outs: Object.keys(vm.editor.getNodeFromId(sw).outputs).length };
	})()`, &ids))
	require.Equal(t, float64(2), ids["outs"],
		"default switch should render two output ports")
	t.Logf("node ids: %v", ids)

	// saveSwitch drives the real JSON-fallback save path: set the
	// form state openNodeProperties populates, force the raw-JSON
	// branch (schema fetch is async and cannot interleave inside one
	// synchronous eval), then save.
	saveSwitch := func(rules int, otherwise bool) {
		ruleObjs := "["
		for i := 0; i < rules; i++ {
			if i > 0 {
				ruleObjs += ","
			}
			ruleObjs += `{"vt":"str","v":"` + fmt.Sprint(i) + `"}`
		}
		ruleObjs += "]"
		expr := `(function(){ const vm = ` + vm + `; ` +
			`const nodeId = ` + js(fmt.Sprint(ids["sw"])) + `; ` +
			`try { vm.openNodeProperties(nodeId); } catch (e) { return 'open: '+e.stack; } ` +
			`vm.editingNodeSchema = null; ` +
			`vm.editingNodeSchemaFields = []; ` +
			`vm.editingNodeData = JSON.stringify({ rules: ` + ruleObjs +
			`, otherwise: ` + fmt.Sprint(otherwise) + ` }); ` +
			`try { vm.saveNodeProperties(); } ` +
			`catch (e) { return 'save failed: ' + (e && e.stack || e); } ` +
			`return ''; })()`
		var failure string
		require.NoError(t, eval(page, expr, &failure))
		require.Empty(t, failure)
	}
	portState := func() map[string]any {
		var st map[string]any
		require.NoError(t, eval(page, `(function(){
			const vm = `+vm+`;
			const sw = `+js(fmt.Sprint(ids["sw"]))+`;
			const dbg = `+js(fmt.Sprint(ids["dbg"]))+`;
			const node = vm.editor.getNodeFromId(sw);
			const dbgNode = vm.editor.getNodeFromId(dbg);
			return {
				outs: Object.keys(node.outputs || {}).length,
				dom: document.querySelectorAll('#node-' + sw + ' .outputs .output').length,
				out2wires: ((node.outputs.output_2 || {}).connections || []).length,
				dbgIn: (((dbgNode.inputs || {}).input_1 || {}).connections || []).length,
				labels: (node.data.outputs || []).length
			};
		})()`, &st))
		return st
	}

	// Grow: three rules -> three ports, edge on output_2 survives.
	saveSwitch(3, false)
	st := portState()
	require.Equal(t, float64(3), st["outs"], "three rules must grow to three output sockets")
	require.Equal(t, float64(3), st["dom"], "canvas must show the new socket without reload")
	require.Equal(t, float64(3), st["labels"], "data.outputs labels must track the port count")
	require.Equal(t, float64(1), st["out2wires"], "wire on surviving output_2 must be kept")
	require.Equal(t, float64(1), st["dbgIn"], "debug input must keep its incoming wire")

	// Shrink: one rule -> one port; the output_2 wire is dropped.
	saveSwitch(1, false)
	st = portState()
	require.Equal(t, float64(1), st["outs"], "one rule must shrink to a single output socket")
	require.Equal(t, float64(1), st["dom"], "canvas must drop the removed socket")
	require.Equal(t, float64(1), st["labels"])
	require.Zero(t, st["dbgIn"], "wire into debug must be dropped with the removed port")

	// Otherwise flag adds a trailing port again.
	saveSwitch(1, true)
	st = portState()
	require.Equal(t, float64(2), st["outs"], "one rule + otherwise must render two sockets")
	require.Equal(t, float64(2), st["dom"])

	if errs := w.errors(); len(errs) > 0 {
		t.Fatalf("console errors on editor: %v", errs)
	}
}
