package api

import (
	"encoding/json"
	"net/http"

	guapi "github.com/dracory/api"

	"github.com/dracory/nodestore"
)

// This file serves the child-flow export/import endpoints used by
// the editor's component/child-flow export and import dialogs.

// childFlowExportRequest carries a workflow id plus the child flow or
// component id to export. childFlow is the canonical field; flow is
// accepted as a legacy alias.
type childFlowExportRequest struct {
	ID        string `json:"id"`
	ChildFlow string `json:"childFlow"`
	Flow      string `json:"flow"`
	Component string `json:"component"`
}

// exportChildFlow exports a child flow or component definition from a
// stored workflow — the JSON meta and the transitively needed component
// definitions.
func (a *Api) exportChildFlow(w http.ResponseWriter, r *http.Request) {
	var req childFlowExportRequest
	if err := bindJSON(r, &req); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	workflow, err := a.store.WorkflowFindByID(r.Context(), req.ID)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if workflow == nil {
		respond(w, guapi.Error("workflow not found"))
		return
	}

	var export *nodestore.ChildFlowExport
	if req.Component != "" {
		export, err = nodestore.ExportComponentDef(workflow, req.Component)
	} else {
		flowRef := req.ChildFlow
		if flowRef == "" {
			flowRef = req.Flow
		}
		export, err = nodestore.ExportChildFlow(workflow, flowRef)
	}
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}

	respond(w, guapi.SuccessWithData("child flow exported successfully", map[string]any{
		"child_flow_export": export,
		"warnings":          export.Warnings,
	}))
}

// childFlowImportRequest carries a workflow id plus the child-flow
// envelope. child_flow_export is canonical; payload and the legacy
// module_export field are accepted as input aliases.
type childFlowImportRequest struct {
	ID              string          `json:"id"`
	ChildFlowExport json.RawMessage `json:"child_flow_export"`
	Payload         json.RawMessage `json:"payload"`
	ModuleExport    json.RawMessage `json:"module_export"`
}

// importChildFlow merges a child-flow envelope into an existing
// workflow as a NEW child flow — every id is re-minted and
// intra-payload references rewritten (see nodestore.ImportChildFlow).
// The updated workflow is persisted and returned so the editor can
// re-render.
func (a *Api) importChildFlow(w http.ResponseWriter, r *http.Request) {
	var req childFlowImportRequest
	if err := bindJSON(r, &req); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	raw := req.ChildFlowExport
	if len(raw) == 0 {
		raw = req.Payload
	}
	if len(raw) == 0 {
		raw = req.ModuleExport
	}
	if len(raw) == 0 {
		respond(w, guapi.Error("child_flow_export payload is required"))
		return
	}

	workflow, err := a.store.WorkflowFindByID(r.Context(), req.ID)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if workflow == nil {
		respond(w, guapi.Error("workflow not found"))
		return
	}

	warnings, err := nodestore.ImportChildFlow(workflow, raw)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if err := a.store.WorkflowUpdate(r.Context(), workflow); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}

	respond(w, guapi.SuccessWithData("child flow imported successfully", map[string]any{
		"workflow": workflowToJSON(workflow),
		"warnings": warnings,
	}))
}
