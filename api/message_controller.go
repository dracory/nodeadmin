package api

import (
	"net/http"
	"time"

	guapi "github.com/gouniverse/api"

	"github.com/dracory/nodestore"
)

// messageJSON is the wire shape for messages — identical to
// flowrunner.Message's JSON form
type messageJSON struct {
	ID        string         `json:"id,omitempty"`
	Payload   any            `json:"payload,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Error     *string        `json:"error,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
}

func messageToJSON(m nodestore.MessageInterface) messageJSON {
	return messageJSON{
		ID:        m.GetID(),
		Payload:   m.GetPayload(),
		Metadata:  m.GetMetadata(),
		Error:     m.GetError(),
		Timestamp: m.GetTimestamp(),
	}
}

func messageFromJSON(j messageJSON) nodestore.MessageInterface {
	m := nodestore.NewMessage().
		SetPayload(j.Payload).
		SetMetadata(j.Metadata).
		SetTimestamp(j.Timestamp)
	if j.ID != "" {
		m.SetID(j.ID)
	}
	if j.Error != nil {
		m.SetError(*j.Error)
	}
	return m
}

func (a *Api) listMessages(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.MessageList(r.Context(), nodestore.MessageQuery())
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	messages := make([]messageJSON, 0, len(list))
	for _, m := range list {
		messages = append(messages, messageToJSON(m))
	}
	respond(w, guapi.SuccessWithData("messages retrieved successfully", map[string]any{
		"messages": messages,
	}))
}

func (a *Api) getMessage(w http.ResponseWriter, r *http.Request) {
	var req idRequest
	if err := bindJSON(r, &req); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	m, err := a.store.MessageFindByID(r.Context(), req.ID)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if m == nil {
		respond(w, guapi.Error("message not found"))
		return
	}
	respond(w, guapi.SuccessWithData("message retrieved successfully", map[string]any{
		"message": messageToJSON(m),
	}))
}

func (a *Api) createMessage(w http.ResponseWriter, r *http.Request) {
	var j messageJSON
	if err := bindJSON(r, &j); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	m := messageFromJSON(j)
	if err := a.store.MessageCreate(r.Context(), m); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	respond(w, guapi.SuccessWithData("message created successfully", map[string]any{
		"message": messageToJSON(m),
	}))
}

func (a *Api) updateMessage(w http.ResponseWriter, r *http.Request) {
	var j messageJSON
	if err := bindJSON(r, &j); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if j.ID == "" {
		respond(w, guapi.Error("message id is required"))
		return
	}
	existing, err := a.store.MessageFindByID(r.Context(), j.ID)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if existing == nil {
		respond(w, guapi.Error("message not found"))
		return
	}
	m := messageFromJSON(j)
	if err := a.store.MessageUpdate(r.Context(), m); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	respond(w, guapi.SuccessWithData("message updated successfully", map[string]any{
		"message": messageToJSON(m),
	}))
}

func (a *Api) deleteMessage(w http.ResponseWriter, r *http.Request) {
	var req idRequest
	if err := bindJSON(r, &req); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if req.ID == "" {
		respond(w, guapi.Error("message id is required"))
		return
	}
	if err := a.store.MessageDeleteByID(r.Context(), req.ID); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	respond(w, guapi.Success("message deleted successfully"))
}
