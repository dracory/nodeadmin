package api

import (
	"net/http"

	guapi "github.com/dracory/api"
	"github.com/dracory/neat"
)

// inboxMessagesTable is the durable per-node handoff table noderun's
// NeatInboxStore maintains (pending -> claimed -> deleted). The name
// is duplicated here rather than imported so the read side needs no
// noderun version bump — the table contract is the schema itself.
const inboxMessagesTable = "node_inbox_messages"

// inboxDepthRow is the slim projection the pending endpoint reads:
// which node a surviving row targets and its lifecycle status.
type inboxDepthRow struct {
	NodeID string `db:"node_id"`
	Status string `db:"status"`
}

// TableName specifies the table name
func (inboxDepthRow) TableName() string { return inboxMessagesTable }

// workflowPending answers GET /api/workflows/{id}/pending with the
// per-node backlog depth: a {nodeId: count} map built from the
// surviving rows of node_inbox_messages (pending + claimed statuses —
// the same rows a runner would drain, so the badge numbers match what
// the Messages/inbox surface reports). Nodes with no rows are absent
// from the map (the editor hides the badge at 0).
func (a *Api) workflowPending(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	workflow, err := a.store.WorkflowFindByID(r.Context(), id)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if workflow == nil {
		respond(w, guapi.Error("workflow not found"))
		return
	}
	counts, err := a.pendingCounts(id)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	out := make(map[string]any, len(counts))
	for nodeID, n := range counts {
		out[nodeID] = n
	}
	respond(w, guapi.SuccessWithData("pending counts retrieved", out))
}

// pendingCounts aggregates node_inbox_messages by node_id for one
// workflow. Rows whose status is NULL count as pending — the drain's
// claimable predicate treats them the same (legacy rows written
// before the status column existed). A nil entity DB (memory-only
// store) or a missing table (no durable inbox ever created) yields an
// empty map rather than an error — the badge poll must stay a cheap
// no-op where durability was never wired.
func (a *Api) pendingCounts(workflowID string) (map[string]int64, error) {
	counts := map[string]int64{}
	sqlDB := a.store.GetEntityStore().GetDB()
	if sqlDB == nil {
		return counts, nil
	}
	db, err := neat.NewFromSQLDB(sqlDB)
	if err != nil {
		return nil, err
	}
	if !db.Schema().HasTable(inboxMessagesTable) {
		return counts, nil
	}
	var rows []inboxDepthRow
	err = db.Query().Model(&inboxDepthRow{}).
		Select("node_id, status").
		Where("workflow_id = ?", workflowID).
		Where("(status = 'pending' OR status = 'claimed' OR status IS NULL)").
		Find(&rows)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		counts[row.NodeID]++
	}
	return counts, nil
}
