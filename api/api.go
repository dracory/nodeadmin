// Package api provides the JSON API for the node admin surface —
// POST endpoints returning dracory/api envelopes, same convention
// as flowadmin/api but backed by nodestore.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/dracory/nodebase"
	"github.com/dracory/nodestore"
)

// EngineInterface is the narrow engine contract the node admin API
// needs (GOD ruling 2026-11-24): workflow deploy/stop, manual inject,
// queue inspection and the per-node run journal. It is defined here —
// the API never names a concrete runner — and
// noderunner.WorkflowEngine satisfies it structurally. Queue and
// journal data travel through the runner-agnostic contract types in
// nodebase (QueueInspector, RunJournalEntry).
type EngineInterface interface {
	// DeployWorkflow starts a workflow and waits for activation to
	// finish (full deploy: every node restarts).
	DeployWorkflow(ctx context.Context, workflow nodestore.WorkflowInterface, input nodestore.MessageInterface) (string, error)
	// DeployWorkflowPartial is DeployWorkflow with "modified nodes"
	// semantics: a redeploy restarts only changed nodebase.
	DeployWorkflowPartial(ctx context.Context, workflow nodestore.WorkflowInterface, input nodestore.MessageInterface) (string, error)
	// StopWorkflow stops a running workflow.
	StopWorkflow(workflowID string) error
	// GetWorkflowStatus reports "running" | "stopped".
	GetWorkflowStatus(workflowID string) (string, error)
	// FireInjectNode fires a running inject node's own configured
	// payload (the editor fire button).
	FireInjectNode(workflowID, nodeID string) error
	// InjectMessage injects an explicit payload downstream of a node.
	InjectMessage(workflowID, fromNodeID string, payload nodestore.MessageInterface) (string, chan nodestore.MessageInterface, error)
	// DiscardPending drops the response channel of a fire-and-forget
	// injected message.
	DiscardPending(msgID string)
	// QueueHandle returns the live queue behind a queue node; ok is
	// false when the workflow is not running or the node has no queue.
	QueueHandle(workflowID, nodeID string) (nodebase.QueueInspector, bool)
	// RunJournal returns the node's recorded execution journal
	// (oldest first); ok is false when the workflow is not running.
	RunJournal(workflowID, nodeID string) ([]nodebase.RunJournalEntry, bool)
	// GetWorkflowSnapshot returns the workflow run status, per-node
	// statuses and the events recorded since the given sequence
	// number, plus the current last sequence for incremental
	// polling (the editor's debug panel contract).
	GetWorkflowSnapshot(workflowID string, since int64) (string, map[string]string, []nodebase.NodeEvent, int64)
	// LeaseRunner returns the ID of the runner instance currently
	// holding the workflow's execution lease ("" when not leased).
	LeaseRunner(workflowID string) string
}

// Api is the node admin API
type Api struct {
	store  nodestore.StoreInterface
	engine EngineInterface
	mux    *http.ServeMux
}

// New creates the API bound to a nodestore store and an engine. The
// engine may be nil — engine-bound endpoints then report
// "workflow engine not available" instead of pretending to work.
func New(store nodestore.StoreInterface, engine EngineInterface) *Api {
	a := &Api{store: store, engine: engine}
	a.mux = a.buildRoutes()
	return a
}

// ServeHTTP implements http.Handler. Cross-site browser writes are
// blocked before routing: a "simple" cross-origin POST (e.g. a form or
// fetch with text/plain) needs no CORS preflight, so without these
// gates any web page could drive the admin API on localhost. JSON
// Content-Type forces a preflight this server never answers.
func (a *Api) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && r.URL.Path != "/api/health" {
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "cross-site requests are not allowed", http.StatusForbidden)
			return
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			http.Error(w, "unsupported media type: application/json required", http.StatusUnsupportedMediaType)
			return
		}
	}
	a.mux.ServeHTTP(w, r)
}

// buildRoutes registers all API routes on a ServeMux
func (a *Api) buildRoutes() *http.ServeMux {
	mux := http.NewServeMux()

	// Health (GET allowed: probe endpoint carries no parameters)
	mux.HandleFunc("GET /api/health", a.healthCheck)
	mux.HandleFunc("POST /api/health", a.healthCheck)

	// Workflows
	mux.HandleFunc("POST /api/workflow-fetch-all", a.listWorkflows)
	mux.HandleFunc("POST /api/workflow-fetch", a.getWorkflow)
	mux.HandleFunc("POST /api/workflow-create", a.createWorkflow)
	mux.HandleFunc("POST /api/workflow-update", a.updateWorkflow)
	mux.HandleFunc("POST /api/workflow-delete", a.deleteWorkflow)

	// Messages
	mux.HandleFunc("POST /api/message-fetch-all", a.listMessages)
	mux.HandleFunc("POST /api/message-fetch", a.getMessage)
	mux.HandleFunc("POST /api/message-create", a.createMessage)
	mux.HandleFunc("POST /api/message-update", a.updateMessage)
	mux.HandleFunc("POST /api/message-delete", a.deleteMessage)

	// Per-node inbox backlog depth (editor badges): the only GET under
	// /api besides health — read-only, no body, so it rides the same
	// 1.5s status poll without a POST round-trip.
	mux.HandleFunc("GET /api/workflows/{id}/pending", a.workflowPending)

	// Workflow lifecycle (deployed flag)
	mux.HandleFunc("POST /api/workflow-start", a.workflowStart)
	mux.HandleFunc("POST /api/workflow-stop", a.workflowStop)
	mux.HandleFunc("POST /api/workflow-status", a.workflowStatus)

	// Workflow groups + global env (config entity)
	mux.HandleFunc("POST /api/workflow-groups-fetch", a.fetchWorkflowGroups)
	mux.HandleFunc("POST /api/workflow-groups-update", a.updateWorkflowGroups)
	mux.HandleFunc("POST /api/global-env-fetch", a.fetchGlobalEnv)
	mux.HandleFunc("POST /api/global-env-update", a.updateGlobalEnv)

	// Node schemas + manual inject
	mux.HandleFunc("POST /api/node-fetch-all", a.listNodeTypes)
	mux.HandleFunc("POST /api/node-schema-fetch", a.getNodeSchema)
	mux.HandleFunc("POST /api/node-inject", a.nodeInject)
	mux.HandleFunc("POST /api/node-run-journal", a.nodeRunJournal)

	// Engine-bound queues
	mux.HandleFunc("POST /api/queue-items", a.queueItems)
	mux.HandleFunc("POST /api/queue-hold", a.queueHold)
	mux.HandleFunc("POST /api/queue-item-delete", a.queueItemDelete)
	mux.HandleFunc("POST /api/queue-item-requeue", a.queueItemRequeue)

	// Node-RED + child flow import/export
	mux.HandleFunc("POST /api/workflow-import-nodered", a.importNodeRED)
	mux.HandleFunc("POST /api/workflow-export-nodered", a.exportNodeRED)
	mux.HandleFunc("POST /api/child-flow-export", a.exportChildFlow)
	mux.HandleFunc("POST /api/child-flow-import", a.importChildFlow)

	return mux
}

// respond writes a dracory/api envelope as JSON
func respond(w http.ResponseWriter, res interface{ ToString() string }) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(res.ToString()))
}

// bindJSON decodes a JSON request body into v
func bindJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}
