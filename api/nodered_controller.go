package api

import (
	"encoding/json"
	"net/http"

	guapi "github.com/gouniverse/api"

	"github.com/dracory/nodestore"
)

// This file serves the Node-RED flow-format endpoints used by the
// editor's Import/Export buttons. Import accepts the flat Node-RED
// flow array (tab objects, subflow definitions, nodes with wires) and
// creates a workflow; export converts a stored workflow back into the
// same array so it round-trips.

// importNodeREDRequest carries the pasted Node-RED flow array; "flow"
// and "items" are accepted as the array field name.
type importNodeREDRequest struct {
	Name  string          `json:"name"`
	Flow  json.RawMessage `json:"flow"`
	Items json.RawMessage `json:"items"`
}

// importNodeRED converts a Node-RED flow JSON array into a stored
// workflow via nodestore.ImportNodeRED.
func (a *Api) importNodeRED(w http.ResponseWriter, r *http.Request) {
	var req importNodeREDRequest
	if err := bindJSON(r, &req); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	raw := req.Flow
	if len(raw) == 0 {
		raw = req.Items
	}
	if len(raw) == 0 {
		respond(w, guapi.Error("flow JSON array is required"))
		return
	}

	workflow, err := nodestore.ImportNodeRED(raw)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if req.Name != "" {
		workflow.SetName(req.Name)
	}

	if err := a.store.WorkflowCreate(r.Context(), workflow); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}

	respond(w, guapi.SuccessWithData("flow imported successfully", map[string]any{
		"workflow": workflowToJSON(workflow),
	}))
}

// exportNodeRED converts a stored workflow into the Node-RED flow
// JSON array — the inverse of importNodeRED.
func (a *Api) exportNodeRED(w http.ResponseWriter, r *http.Request) {
	var req idRequest
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

	raw, err := nodestore.ExportNodeRED(workflow)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}

	var flow []map[string]any
	if err := json.Unmarshal(raw, &flow); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}

	respond(w, guapi.SuccessWithData("flow exported successfully", map[string]any{
		"flow": flow,
	}))
}
