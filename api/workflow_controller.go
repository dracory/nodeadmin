package api

import (
	"errors"
	"net/http"
	"time"

	guapi "github.com/gouniverse/api"

	"github.com/dracory/nodestore"
)

// workflowJSON is the wire shape for workflows — identical to
// flowrunner.Workflow's JSON form
type workflowJSON struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Nodes       []nodeJSON     `json:"nodes"`
	Edges       []edgeJSON     `json:"edges"`
	View        map[string]any `json:"view,omitempty"`
	Deployed    bool           `json:"deployed,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
}

type nodeJSON struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Position positionJSON   `json:"position"`
	Data     map[string]any `json:"data"`
}

type positionJSON struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type edgeJSON struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	SourceKey string `json:"sourceKey,omitempty"`
	Target    string `json:"target"`
	TargetKey string `json:"targetKey,omitempty"`
}

// workflowToJSON maps a nodestore workflow to its wire form
func workflowToJSON(wf nodestore.WorkflowInterface) workflowJSON {
	out := workflowJSON{
		ID:          wf.GetID(),
		Name:        wf.GetName(),
		Description: wf.GetDescription(),
		Nodes:       []nodeJSON{},
		Edges:       []edgeJSON{},
		View:        wf.GetView(),
		Deployed:    wf.GetDeployed(),
		CreatedAt:   wf.GetCreatedAt(),
		UpdatedAt:   wf.GetUpdatedAt(),
	}
	for _, n := range wf.GetNodes() {
		out.Nodes = append(out.Nodes, nodeJSON{
			ID:       n.GetID(),
			Type:     n.GetType(),
			Position: positionJSON{X: n.GetPositionX(), Y: n.GetPositionY()},
			Data:     n.GetData(),
		})
	}
	for _, e := range wf.GetEdges() {
		out.Edges = append(out.Edges, edgeJSON{
			ID:        e.GetID(),
			Source:    e.GetSource(),
			SourceKey: e.GetSourceKey(),
			Target:    e.GetTarget(),
			TargetKey: e.GetTargetKey(),
		})
	}
	return out
}

// workflowFromJSON maps a wire workflow onto nodestore interfaces
func workflowFromJSON(j workflowJSON) (nodestore.WorkflowInterface, error) {
	if j.Name == "" {
		return nil, errors.New("workflow name is required")
	}
	wf := nodestore.NewWorkflow(j.Name).
		SetDescription(j.Description).
		SetView(j.View).
		SetDeployed(j.Deployed)
	if j.ID != "" {
		wf.SetID(j.ID)
	}
	for _, n := range j.Nodes {
		wf.AddNode(nodestore.NewNode(n.ID, n.Type).
			SetPosition(n.Position.X, n.Position.Y).
			SetData(n.Data))
	}
	for _, e := range j.Edges {
		wf.AddEdge(nodestore.NewEdge(e.ID, e.Source, e.Target).
			SetSourceKey(e.SourceKey).
			SetTargetKey(e.TargetKey))
	}
	return wf, nil
}

// idRequest carries an entity ID in the JSON request body
type idRequest struct {
	ID string `json:"id"`
}

func (a *Api) listWorkflows(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.WorkflowList(r.Context(), nodestore.WorkflowQuery())
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	workflows := make([]workflowJSON, 0, len(list))
	for _, wf := range list {
		workflows = append(workflows, workflowToJSON(wf))
	}
	respond(w, guapi.SuccessWithData("workflows retrieved successfully", map[string]any{
		"workflows": workflows,
	}))
}

func (a *Api) getWorkflow(w http.ResponseWriter, r *http.Request) {
	var req idRequest
	if err := bindJSON(r, &req); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	wf, err := a.store.WorkflowFindByID(r.Context(), req.ID)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if wf == nil {
		respond(w, guapi.Error("workflow not found"))
		return
	}
	respond(w, guapi.SuccessWithData("workflow retrieved successfully", map[string]any{
		"workflow": workflowToJSON(wf),
	}))
}

func (a *Api) createWorkflow(w http.ResponseWriter, r *http.Request) {
	var j workflowJSON
	if err := bindJSON(r, &j); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	wf, err := workflowFromJSON(j)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	// Credential extraction (flowadmin creds-store parity): real
	// secret values move out of node.Data into credential entities
	// BEFORE the document persists, so the stored nodes keep only
	// sentinel markers and secrets never hit the workflow record.
	creds := nodestore.ExtractWorkflowCredentials(wf, nil)
	// The deployed flag is server-managed in flowadmin; nodestore has
	// no engine yet, so it is stored as supplied.
	if err := a.store.WorkflowCreate(r.Context(), wf); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if err := a.store.CredentialReplaceForWorkflow(r.Context(), wf.GetID(), creds); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	respond(w, guapi.SuccessWithData("workflow created successfully", map[string]any{
		"workflow": workflowToJSON(wf),
	}))
}

func (a *Api) updateWorkflow(w http.ResponseWriter, r *http.Request) {
	var j workflowJSON
	if err := bindJSON(r, &j); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if j.ID == "" {
		respond(w, guapi.Error("workflow id is required"))
		return
	}
	existing, err := a.store.WorkflowFindByID(r.Context(), j.ID)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if existing == nil {
		respond(w, guapi.Error("workflow not found"))
		return
	}
	wf, err := workflowFromJSON(j)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	// The deployed flag is server-managed: the editor's save payload
	// does not carry it, so trusting j.Deployed here would silently
	// clear the flag on every save and break boot-time resume.
	wf.SetDeployed(existing.GetDeployed())
	// Credential extraction BEFORE update: the previously stored map
	// preserves values the editor sent back as sentinels or omitted;
	// the stored nodes keep only sentinel markers.
	existingCreds, err := a.store.CredentialMapForWorkflow(r.Context(), j.ID)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	creds := nodestore.ExtractWorkflowCredentials(wf, existingCreds)
	if err := a.store.WorkflowUpdate(r.Context(), wf); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if err := a.store.CredentialReplaceForWorkflow(r.Context(), j.ID, creds); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	respond(w, guapi.SuccessWithData("workflow updated successfully", map[string]any{
		"workflow": workflowToJSON(wf),
	}))
}

func (a *Api) deleteWorkflow(w http.ResponseWriter, r *http.Request) {
	var req idRequest
	if err := bindJSON(r, &req); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if req.ID == "" {
		respond(w, guapi.Error("workflow id is required"))
		return
	}
	if err := a.store.WorkflowDeleteByID(r.Context(), req.ID); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	// Cascade the workflow's non-child records (nodes/edges already
	// cascade inside WorkflowDeleteByID): secrets must not linger and
	// deploy history/groups go with their owner.
	_ = a.store.CredentialDeleteByWorkflowID(r.Context(), req.ID)
	_ = a.store.DeployRecordDeleteByWorkflowID(r.Context(), req.ID)
	if groups, err := a.store.NodeGroupList(r.Context(),
		nodestore.NodeGroupQuery().WithWorkflowID(req.ID)); err == nil {
		for _, g := range groups {
			_ = a.store.NodeGroupDeleteByID(r.Context(), g.GetID())
		}
	}
	respond(w, guapi.Success("workflow deleted successfully"))
}
