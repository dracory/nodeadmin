# nodeadmin

Mountable admin HTTP/UI surface for nodestore-based workflows — the
Node-RED-style flow editor, workflow management, and JSON API. Follows
the dracory admin-module pattern (like taskadmin/shopadmin).

## Usage

```go
handler, err := nodeadmin.New(nodeadmin.Options{
    Store:            store,            // nodestore.StoreInterface (required)
    IsAccessAllowed:  isAllowed,       // optional; nil = open. Host-owned auth.
    OnUnauthorized:   onDenied,        // optional denied-response writer
    GetAuthUserID:    authUserIDFunc,  // optional identity for display/audit
    AdminHomeURL:     "/",
    NodeAdminURL:     "/nodes",        // mount point used for link building
    FuncLayout:       myLayout,        // optional outer chrome
})
mux.Handle("/nodes/", http.StripPrefix("/nodes", handler))
```

`New` returns an `Interface` (a plain `http.Handler`) mountable on any
router — ServeMux, chi, gin, echo.

## Auth

Auth is host-owned: the library stores no users and verifies no
credentials. `IsAccessAllowed(r)` is the only gate — wire it to your
session middleware, SSO, or a Basic-auth adapter. `OnUnauthorized(w, r)`
writes the denied response (redirect to your login, JSON 401, …);
default is a plain 401. `GetAuthUserID(r)` supplies an identity string
for display/audit only. The same seam protects node-served HTTP
listeners (listener "main") via the engine's `HTTPAuthenticator`.

## Structure

- `nodeadmin.go` — entry point: `Options`, `Interface`, controller
  dispatch on `?controller=`
- `api/` — JSON API under `/api/*`: workflow/message CRUD,
  start/stop/status, node palette + schemas, inject, run journal,
  queues, groups, global env, Node-RED import. Responses keep the
  dracory/api envelope (`status`/`message`/`data`).
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
