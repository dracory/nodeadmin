# nodeadmin

Mountable admin HTTP/UI surface for nodestore-based workflows — the
Node-RED-style flow editor, workflow management, and JSON API. Follows
the dracory admin-module pattern (like taskadmin/shopadmin).

## Usage

```go
handler, err := nodeadmin.New(nodeadmin.Options{
    Store:        store,              // nodestore.StoreInterface (required)
    AuthUserID:   authUserIDFunc,     // optional; "" redirects to AdminHomeURL
    AdminHomeURL: "/",
    NodeAdminURL: "/nodes",           // mount point used for link building
    FuncLayout:   myLayout,           // optional outer chrome
})
mux.Handle("/nodes/", http.StripPrefix("/nodes", handler))
```

`New` returns an `Interface` (a plain `http.Handler`) mountable on any
router — ServeMux, chi, gin, echo.

## Structure

- `nodeadmin.go` — entry point: `Options`, `Interface`, controller
  dispatch on `?controller=`
- `api/` — JSON API under `/api/*`: workflow/message CRUD,
  start/stop/status, node palette + schemas, inject, run journal,
  queues, groups, global env, Node-RED import. Responses keep the
  gouniverse/api envelope (`status`/`message`/`data`).
- `editor/` — the Drawflow-based flow editor (Vue 3 +
  dracory/floweditor fork via CDN, no build step)
- `home/` — admin dashboard landing section
- `workflows/` — workflow list/groups section
- `messages/` — message inspection section
- `shared/` — section nav, layout fallback, controller constants

## Rules

- Depends on `nodestore.StoreInterface` only — never on concrete store
  types.
- Execution is opt-in: nodeadmin defines a narrow engine interface
  (deploy/stop/inject/status/journal/queue ops) that a noderun
  `WorkflowEngine` satisfies structurally — nodestore stays a pure
  storage layer and knows nothing about execution or admin.
- One wildcard route + manual dispatch — `http.ServeMux` panics on
  overlapping wildcards.
