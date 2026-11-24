package api

import (
	"encoding/json"
	"net/http"

	guapi "github.com/gouniverse/api"

	"github.com/dracory/nodestore"
)

// This file serves the module-level export/import endpoints used by
// the editor's subflow/module export and import dialogs.

// moduleExportRequest carries a workflow id plus the module or
// subflow name to export; "module" and "subflow" are accepted as the
// field names.
type moduleExportRequest struct {
	ID      string `json:"id"`
	Module  string `json:"module"`
	Subflow string `json:"subflow"`
}

// exportModule exports a module or subflow definition from a stored
// workflow — the JSON meta and the transitively needed subflow
// definitions.
func (a *Api) exportModule(w http.ResponseWriter, r *http.Request) {
	var req moduleExportRequest
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

	var export *nodestore.ModuleExport
	if req.Subflow != "" {
		export, err = nodestore.ExportSubflowDef(workflow, req.Subflow)
	} else {
		export, err = nodestore.ExportModule(workflow, req.Module)
	}
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}

	respond(w, guapi.SuccessWithData("module exported successfully", map[string]any{
		"module_export": export,
		"warnings":      export.Warnings,
	}))
}

// moduleImportRequest carries a workflow id plus the module envelope;
// "module_export" and "payload" are both accepted as the field name.
type moduleImportRequest struct {
	ID           string          `json:"id"`
	ModuleExport json.RawMessage `json:"module_export"`
	Payload      json.RawMessage `json:"payload"`
}

// importModule merges a module envelope into an existing workflow as
// a NEW module — every id is re-minted and intra-payload references
// rewritten (see nodestore.ImportModule). The updated workflow is
// persisted and returned so the editor can re-render.
func (a *Api) importModule(w http.ResponseWriter, r *http.Request) {
	var req moduleImportRequest
	if err := bindJSON(r, &req); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	raw := req.ModuleExport
	if len(raw) == 0 {
		raw = req.Payload
	}
	if len(raw) == 0 {
		respond(w, guapi.Error("module_export payload is required"))
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

	warnings, err := nodestore.ImportModule(workflow, raw)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if err := a.store.WorkflowUpdate(r.Context(), workflow); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}

	respond(w, guapi.SuccessWithData("module imported successfully", map[string]any{
		"workflow": workflowToJSON(workflow),
		"warnings": warnings,
	}))
}
