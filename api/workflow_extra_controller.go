package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	guapi "github.com/dracory/api"

	"github.com/dracory/nodestore"
	"github.com/dracory/nodebase"
)

// This file covers the lifecycle/global-state endpoints the editor
// calls: start/stop/status go through the engine's deploy path (the
// persisted Deployed flag follows the deploy so flows resume at
// boot), and the workflow-groups/global-env tables live in the
// store's config entity. Queue inspection and the run journal are
// served from the engine via EngineInterface.

// globalConfigKey is the config key holding the whole global-config
// blob (env, workflowGroups, engine sections) — mirrors the
// GlobalConfigStore shape used by flowadmin.
const globalConfigKey = "global_config"

// getGlobalConfig loads the whole config blob as a map
func (a *Api) getGlobalConfig(ctx context.Context) (map[string]any, error) {
	raw, err := a.store.ConfigGet(ctx, globalConfigKey)
	if err != nil || raw == "" {
		return map[string]any{}, err
	}
	cfg := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return map[string]any{}, err
	}
	return cfg, nil
}

// saveGlobalConfig persists the whole config blob
func (a *Api) saveGlobalConfig(ctx context.Context, cfg map[string]any) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return a.store.ConfigSet(ctx, globalConfigKey, string(raw))
}

// startRequest carries a workflow ID plus an optional deploy mode.
// "full" forces a full redeploy (every node restarts); anything else —
// including the editor's {id}-only deploy — takes the partial path,
// which restarts only changed nodes on redeploy (Node-RED "modified
// nodes"). A cold deploy is identical either way.
type startRequest struct {
	ID   string `json:"id"`
	Mode string `json:"mode"`
}

// workflowStart deploys a workflow through the engine for real —
// DeployWorkflow waits for activation so a failed bind is reported to
// the caller — then persists the deployed flag so the flow re-activates
// on restart (Node-RED parity).
func (a *Api) workflowStart(w http.ResponseWriter, r *http.Request) {
	var req startRequest
	if err := bindJSON(r, &req); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if a.engine == nil {
		respond(w, guapi.Error("workflow engine not available"))
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

	// Detach from the request context — it is cancelled as soon as
	// this handler returns, which would kill the run immediately.
	payload := nodestore.NewMessage().SetTimestamp(time.Now())
	var executionID string
	if req.Mode == "full" {
		executionID, err = a.engine.DeployWorkflow(context.Background(), workflow, payload)
	} else {
		executionID, err = a.engine.DeployWorkflowPartial(context.Background(), workflow, payload)
	}
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}

	// Persist the deployed flag after a successful deploy; if it
	// cannot be saved, undeploy so the UI and storage agree.
	if !workflow.GetDeployed() {
		workflow.SetDeployed(true)
		if err := a.store.WorkflowUpdate(r.Context(), workflow); err != nil {
			_ = a.engine.StopWorkflow(workflow.GetID())
			respond(w, guapi.Error("workflow not started: deployed flag could not be saved: "+err.Error()))
			return
		}
	}
	// Deploy history (deploy_record entities, P4): append an immutable
	// record of this deploy — the flow snapshot carries sentinel
	// markers, never real secrets. Failure is logged via the response
	// payload but never fails the deploy itself.
	if flowRaw, err := json.Marshal(workflowToJSON(workflow)); err == nil {
		_ = a.store.DeployRecordCreate(r.Context(),
			nodestore.NewDeployRecord(workflow.GetID(), "local", string(flowRaw)))
	}
	respond(w, guapi.SuccessWithData("workflow started", map[string]any{
		"workflow":     workflow,
		"execution_id": executionID,
		"status":       "started",
	}))
}

// workflowStop stops a running workflow through the engine and clears
// the persisted deployed flag.
func (a *Api) workflowStop(w http.ResponseWriter, r *http.Request) {
	var req idRequest
	if err := bindJSON(r, &req); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if a.engine == nil {
		respond(w, guapi.Error("workflow engine not available"))
		return
	}
	// Clear the deployed flag first and regardless of whether the
	// flow is running, so a flow that failed to resume at boot stops
	// retrying.
	stopErr := a.engine.StopWorkflow(req.ID)
	workflow, err := a.store.WorkflowFindByID(r.Context(), req.ID)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if workflow == nil {
		respond(w, guapi.Error("workflow not found"))
		return
	}
	if workflow.GetDeployed() {
		workflow.SetDeployed(false)
		if err := a.store.WorkflowUpdate(r.Context(), workflow); err != nil {
			respond(w, guapi.Error("could not clear deployed flag: "+err.Error()))
			return
		}
	}
	if stopErr != nil {
		respond(w, guapi.Error(stopErr.Error()))
		return
	}
	respond(w, guapi.SuccessWithData("workflow stopped", map[string]any{
		"workflow": workflow,
		"status":   "stopped",
	}))
}

// statusRequest carries a workflow ID plus the caller's last seen
// event sequence — the editor polls with `since` and consumes only
// the new events.
type statusRequest struct {
	ID    string `json:"id"`
	Since int64  `json:"since"`
}

// workflowStatus reports the engine's live run status, per-node
// statuses and the execution events since `since`, alongside the
// persisted deployed flag — the same snapshot shape flowadmin's
// workflow-status serves (the editor's pollStatus consumes nodes,
// events, last_seq and runner_id). Without an engine the flag is
// reported as the status (a persistence-only surface cannot know
// more).
func (a *Api) workflowStatus(w http.ResponseWriter, r *http.Request) {
	var req statusRequest
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
	if a.engine == nil {
		status := "stopped"
		if workflow.GetDeployed() {
			status = "running"
		}
		respond(w, guapi.SuccessWithData("workflow status retrieved", map[string]any{
			"status":   status,
			"deployed": workflow.GetDeployed(),
		}))
		return
	}
	status, nodeStatuses, events, lastSeq := a.engine.GetWorkflowSnapshot(req.ID, req.Since)
	respond(w, guapi.SuccessWithData("workflow status retrieved", map[string]any{
		"status":    status,
		"nodes":     nodeStatuses,
		"events":    events,
		"last_seq":  lastSeq,
		"runner_id": a.engine.LeaseRunner(req.ID),
		"deployed":  workflow.GetDeployed(),
	}))
}

// workflowGroupsRequest carries the workflow-group array.
type workflowGroupsRequest struct {
	Groups any `json:"workflow_groups"`
}

// workflowGroupJSON is the wire shape of one sidebar workflow group —
// the same {id,name,parentId,workflows} entries the old global-config
// "workflowGroups" section carried; persistence is now workflow_group
// entities (P4 cutover).
type workflowGroupJSON struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	ParentID  string   `json:"parentId"`
	Workflows []string `json:"workflows"`
}

// fetchWorkflowGroups returns the persisted workflow-group list, read
// from workflow_group entities (the global-config JSON section was
// migrated at startup — see Store.MigrateWorkflowGroupsConfig).
func (a *Api) fetchWorkflowGroups(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.WorkflowGroupList(r.Context(), nodestore.WorkflowGroupQuery())
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	groups := make([]workflowGroupJSON, 0, len(list))
	for _, g := range list {
		groups = append(groups, workflowGroupJSON{
			ID:        g.GetID(),
			Name:      g.GetName(),
			ParentID:  g.GetParentID(),
			Workflows: g.GetWorkflowIDs(),
		})
	}
	respond(w, guapi.SuccessWithData("workflow groups retrieved successfully", map[string]any{
		"workflow_groups": groups,
	}))
}

// updateWorkflowGroups replaces the persisted workflow-group set:
// submitted entries are upserted by id, entities missing from the
// submission are deleted (replace-in-full semantics, same as the old
// config-blob write).
func (a *Api) updateWorkflowGroups(w http.ResponseWriter, r *http.Request) {
	var req workflowGroupsRequest
	if err := bindJSON(r, &req); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	groups := req.Groups
	if groups == nil {
		groups = []any{}
	}
	rawList, ok := groups.([]any)
	if !ok {
		respond(w, guapi.Error("workflow_groups must be an array of {id,name,parentId,workflows} entries"))
		return
	}
	rawJSON, err := json.Marshal(rawList)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	entries := []workflowGroupJSON{}
	if err := json.Unmarshal(rawJSON, &entries); err != nil {
		respond(w, guapi.Error("workflow_groups must be an array of {id,name,parentId,workflows} entries"))
		return
	}

	existing, err := a.store.WorkflowGroupList(r.Context(), nodestore.WorkflowGroupQuery())
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	keep := map[string]bool{}
	for _, e := range entries {
		if e.ID == "" {
			continue
		}
		keep[e.ID] = true
		group, err := a.store.WorkflowGroupFindByID(r.Context(), e.ID)
		if err != nil {
			respond(w, guapi.Error(err.Error()))
			return
		}
		if group == nil {
			group = nodestore.NewWorkflowGroup(e.Name).SetID(e.ID)
			group.SetParentID(e.ParentID).SetWorkflowIDs(e.Workflows)
			err = a.store.WorkflowGroupCreate(r.Context(), group)
		} else {
			group.SetName(e.Name).SetParentID(e.ParentID).SetWorkflowIDs(e.Workflows)
			err = a.store.WorkflowGroupUpdate(r.Context(), group)
		}
		if err != nil {
			respond(w, guapi.Error(err.Error()))
			return
		}
	}
	for _, g := range existing {
		if keep[g.GetID()] {
			continue
		}
		if err := a.store.WorkflowGroupDeleteByID(r.Context(), g.GetID()); err != nil {
			respond(w, guapi.Error(err.Error()))
			return
		}
	}
	respond(w, guapi.SuccessWithData("workflow groups updated successfully", map[string]any{
		"workflow_groups": entries,
	}))
}

// globalEnvRequest carries the env table array.
type globalEnvRequest struct {
	Env any `json:"env"`
}

// fetchGlobalEnv returns the persisted global-config env table.
func (a *Api) fetchGlobalEnv(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.getGlobalConfig(r.Context())
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	env := cfg["env"]
	if env == nil {
		env = []any{}
	}
	respond(w, guapi.SuccessWithData("global env retrieved successfully", map[string]any{
		"env": env,
	}))
}

// updateGlobalEnv replaces the global-config env table.
func (a *Api) updateGlobalEnv(w http.ResponseWriter, r *http.Request) {
	var req globalEnvRequest
	if err := bindJSON(r, &req); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	env := req.Env
	if env == nil {
		env = []any{}
	}
	if _, ok := env.([]any); !ok {
		respond(w, guapi.Error("env must be an array of {name,type,value} entries"))
		return
	}
	cfg, err := a.getGlobalConfig(r.Context())
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	cfg["env"] = env
	if err := a.saveGlobalConfig(r.Context(), cfg); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	respond(w, guapi.SuccessWithData("global env updated successfully", map[string]any{
		"env": env,
	}))
}

// nodeRunJournal serves POST /api/node-run-journal: {workflow_id,
// node_id} → {node_id, size, entries:[…]} with entries oldest-first —
// the engine's bounded per-node execution journal. Entries are
// run-scoped: a stopped or never-deployed workflow reports an error
// like the queue endpoints do.
func (a *Api) nodeRunJournal(w http.ResponseWriter, r *http.Request) {
	var req queueRequest // same {workflow_id, node_id} envelope
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
	entries, ok := a.engine.RunJournal(req.WorkflowID, req.NodeID)
	if !ok {
		respond(w, guapi.Error("workflow not running — deploy it to record run details"))
		return
	}
	respond(w, guapi.SuccessWithData("run journal retrieved successfully", map[string]any{
		"node_id": req.NodeID,
		"size":    nodebase.RunJournalSize,
		"entries": entries,
	}))
}

/* ---- Queue inspection endpoints ---- */

// queueRequest carries workflow + node ids for queue endpoints.
type queueRequest struct {
	WorkflowID string `json:"workflow_id"`
	NodeID     string `json:"node_id"`
}

// queueHoldRequest toggles the queue's hold gate.
type queueHoldRequest struct {
	WorkflowID string `json:"workflow_id"`
	NodeID     string `json:"node_id"`
	Hold       bool   `json:"hold"`
}

// queueItemRequest targets one queued item by its durable row seq.
// Route (optional, checkpoints only) names the outgoing edge the
// release takes — a route label or an "output_N" port key; empty
// releases on all edges. By optionally records the approver identity
// into the released payload's checkpoint metadata.
type queueItemRequest struct {
	WorkflowID string `json:"workflow_id"`
	NodeID     string `json:"node_id"`
	Seq        int64  `json:"seq"`
	Route      string `json:"route"`
	By         string `json:"by"`
}

// queueLookup resolves the live queue or writes the error
// response and returns nil.
func (a *Api) queueLookup(w http.ResponseWriter, workflowID, nodeID string) nodebase.QueueInspector {
	if a.engine == nil {
		respond(w, guapi.Error("workflow engine not available"))
		return nil
	}
	if workflowID == "" || nodeID == "" {
		respond(w, guapi.Error("workflow_id and node_id are required"))
		return nil
	}
	q, ok := a.engine.QueueHandle(workflowID, nodeID)
	if !ok {
		respond(w, guapi.Error("queue not found — is the workflow deployed and is the node a queue?"))
		return nil
	}
	return q
}

// queuePreview renders a one-line payload preview for the item list.
func queuePreview(item nodebase.QueueItem) string {
	if item.Payload == nil {
		return "null"
	}
	raw, err := json.Marshal(item.Payload.GetPayload())
	if err != nil {
		raw = []byte(fmt.Sprintf("%v", item.Payload.GetPayload()))
	}
	preview := string(raw)
	const max = 200
	if len(preview) > max {
		preview = preview[:max] + "..."
	}
	return preview
}

// queueItemsResponse builds the shared {count, hold, items} payload.
func queueItemsResponse(q nodebase.QueueInspector) map[string]any {
	now := time.Now()
	items := q.Items()
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]any{
			"seq":         item.Seq,
			"enqueued_at": item.EnqueuedAt,
			"age_seconds": now.Sub(item.EnqueuedAt).Seconds(),
			"preview":     queuePreview(item),
		})
	}
	return map[string]any{
		"count": q.Count(),
		"hold":  q.Held(),
		"items": out,
		// Checkpoint nodes expose their named release routes in port
		// order; plain queues report an empty list.
		"routes": q.RouteLabels(),
	}
}

// queueItems lists the queue's queued items (seq, age, payload
// preview) plus depth and hold state.
func (a *Api) queueItems(w http.ResponseWriter, r *http.Request) {
	var req queueRequest
	if err := bindJSON(r, &req); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	q := a.queueLookup(w, req.WorkflowID, req.NodeID)
	if q == nil {
		return
	}
	respond(w, guapi.SuccessWithData("queue items retrieved successfully", queueItemsResponse(q)))
}

// queueHold sets or clears the queue's hold gate. Held queues keep
// accepting deliveries but do not drain.
func (a *Api) queueHold(w http.ResponseWriter, r *http.Request) {
	var req queueHoldRequest
	if err := bindJSON(r, &req); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	q := a.queueLookup(w, req.WorkflowID, req.NodeID)
	if q == nil {
		return
	}
	q.SetHold(req.Hold)
	respond(w, guapi.SuccessWithData("queue hold updated", map[string]any{
		"hold":  q.Held(),
		"count": q.Count(),
	}))
}

// queueItemDelete drops one queued item (and its persisted row).
func (a *Api) queueItemDelete(w http.ResponseWriter, r *http.Request) {
	var req queueItemRequest
	if err := bindJSON(r, &req); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	q := a.queueLookup(w, req.WorkflowID, req.NodeID)
	if q == nil {
		return
	}
	if err := q.DeleteItem(req.Seq); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	respond(w, guapi.SuccessWithData("queue item deleted", map[string]any{
		"count": q.Count(),
	}))
}

// queueItemRequeue releases one queued item downstream immediately
// (manual single-item release / dead-letter rework). For checkpoint
// nodes an optional route field picks the single outgoing edge the
// item takes — the same endpoint serves both node types.
func (a *Api) queueItemRequeue(w http.ResponseWriter, r *http.Request) {
	var req queueItemRequest
	if err := bindJSON(r, &req); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	q := a.queueLookup(w, req.WorkflowID, req.NodeID)
	if q == nil {
		return
	}
	if err := q.ReleaseItem(req.Seq, req.Route, req.By); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	respond(w, guapi.SuccessWithData("queue item requeued", map[string]any{
		"count": q.Count(),
	}))
}
