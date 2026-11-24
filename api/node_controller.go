package api

import (
	"net/http"
	"time"

	guapi "github.com/gouniverse/api"

	"github.com/dracory/nodestore"
	"github.com/dracory/nodebase"

	_ "github.com/dracory/nodebase/builtin" // register built-in node types (palette + deploy)
)

// NodeType represents metadata about a node type
type NodeType struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Category    string `json:"category"`
	Set         string `json:"set,omitempty"`
}

// RegisterNodeSchema registers a JSON schema for a custom node type,
// replacing any schema already registered under typeID. The schema is
// served by /api/node-schema-fetch, so the schema-driven properties
// form works for the custom type for free (including "showIf",
// "format": "credential", enums). It is a thin adapter over the
// unified noderunner registry: the type's factory registration (if
// any) is preserved.
func RegisterNodeSchema(typeID string, schema map[string]interface{}) {
	reg, _ := nodebase.RegisteredNode(typeID)
	reg.Descriptor.Type = typeID
	reg.Descriptor.Schema = schema
	_ = nodebase.UpsertNode(reg)
}

// RegisterNodeType registers a custom node type's editor metadata so
// it appears in the node palette list (/api/node-fetch-all) — a thin
// adapter over the unified noderunner registry. Re-registering a Type
// replaces its metadata; the type's factory registration (if any) is
// preserved.
func RegisterNodeType(meta NodeType) {
	reg, _ := nodebase.RegisteredNode(meta.Type)
	reg.Descriptor.Type = meta.Type
	reg.Descriptor.Name = meta.Name
	reg.Descriptor.Description = meta.Description
	reg.Descriptor.Icon = meta.Icon
	reg.Descriptor.Category = meta.Category
	if meta.Set != "" {
		reg.Descriptor.Set = meta.Set
	}
	_ = nodebase.UpsertNode(reg)
}

// nodeSchemaFor returns the schema registered for typeID.
func nodeSchemaFor(typeID string) (map[string]interface{}, bool) {
	return nodebase.NodeSchemaFor(typeID)
}

// listNodeTypes returns a list of all available node types
func (a *Api) listNodeTypes(w http.ResponseWriter, r *http.Request) {
	// A registered type IS what the palette shows — built-ins and pack
	// types alike come from the unified noderunner registry.
	var nodeTypes []NodeType
	for _, reg := range nodebase.RegisteredNodes() {
		d := reg.Descriptor
		name := d.Name
		if name == "" {
			name = d.Type
		}
		nodeTypes = append(nodeTypes, NodeType{
			Type:        d.Type,
			Name:        name,
			Description: d.Description,
			Icon:        d.Icon,
			Category:    d.Category,
			Set:         d.Set,
		})
	}
	respond(w, guapi.SuccessWithData("node types retrieved successfully", map[string]any{
		"node_types": nodeTypes,
	}))
}

// getNodeSchema returns the JSON schema for the specified node type
func (a *Api) getNodeSchema(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type string `json:"type"`
	}
	if err := bindJSON(r, &req); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}

	schema, ok := nodeSchemaFor(req.Type)
	if !ok {
		respond(w, guapi.Error("node type not found"))
		return
	}

	respond(w, guapi.SuccessWithData("schema retrieved successfully", map[string]any{
		"schema": schema,
	}))
}

// injectRequest carries the target workflow/node IDs plus an optional
// payload override for a manual inject (editor "fire" button)
type injectRequest struct {
	WorkflowID string                 `json:"workflow_id"`
	NodeID     string                 `json:"node_id"`
	Payload    map[string]interface{} `json:"payload"`
}

// nodeInject fires a manual inject from the given node into the running
// workflow (editor fire button equivalent of Node-RED's inject button).
// Fire-and-forget: the pending response channel is discarded since no
// reply is expected.
func (a *Api) nodeInject(w http.ResponseWriter, r *http.Request) {
	var req injectRequest
	if err := bindJSON(r, &req); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if a.engine == nil {
		respond(w, guapi.Error("workflow engine not available"))
		return
	}
	if req.WorkflowID == "" || req.NodeID == "" {
		respond(w, guapi.Error("workflow_id and node_id are required"))
		return
	}

	// Node-RED parity: an explicit payload override is injected
	// verbatim; otherwise the fire button fires the node's OWN
	// configured payload (timestamp by default) via its live executor.
	if req.Payload == nil {
		if err := a.engine.FireInjectNode(req.WorkflowID, req.NodeID); err != nil {
			respond(w, guapi.Error(err.Error()))
			return
		}
		respond(w, guapi.SuccessWithData("message injected", nil))
		return
	}

	payload := nodestore.NewMessage().
		SetPayload(req.Payload).
		SetMetadata(map[string]interface{}{"origin": "manual"}).
		SetTimestamp(time.Now())
	msgID, _, err := a.engine.InjectMessage(req.WorkflowID, req.NodeID, payload)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	a.engine.DiscardPending(msgID)

	respond(w, guapi.SuccessWithData("message injected", map[string]any{
		"msg_id": msgID,
	}))
}
