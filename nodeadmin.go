// Package nodeadmin provides a mountable admin interface for the
// nodestore-backed node domain, following the same dracory admin module
// pattern as flowadmin.
//
// The package exposes a single entry point, New(Options), which returns
// an http.Handler that the host application can mount under any path on
// any router (net/http ServeMux, chi, gin, echo, etc.).
//
// The admin UI surface is one Handle(w, r) mounted at "/" dispatching on
// the ?controller= query parameter through a routes map built once at
// construction (default: the home dashboard). Sections are functions
// (section.UI(uiConfig).SectionName(w, r)); outer chrome is host-owned
// through Options.FuncLayout, falling back to shared.Layout. The API is
// served under /api/* by the api subpackage backed by nodestore.
package nodeadmin

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/dracory/nodeadmin/api"
	dashpage "github.com/dracory/nodeadmin/dashboard"
	"github.com/dracory/nodeadmin/editor"
	"github.com/dracory/nodeadmin/home"
	"github.com/dracory/nodeadmin/messages"
	"github.com/dracory/nodeadmin/nodes"
	"github.com/dracory/nodeadmin/settings"
	"github.com/dracory/nodeadmin/shared"
	"github.com/dracory/nodeadmin/workflows"
	"github.com/dracory/nodebase"
	"github.com/dracory/nodebase/dashboard"
	"github.com/dracory/noderun"
	"github.com/dracory/nodestore"
)

// Options contains all dependencies and configuration for the node admin.
type Options struct {
	// Store is the nodestore.StoreInterface used for persistence
	// (required).
	Store nodestore.StoreInterface

	// IsAccessAllowed reports whether the request may use the admin.
	// Authentication is host-owned (GOD ruling 2026-11-25): sessions,
	// login, users and roles all live outside this package — it only
	// asks this one question per request. Nil means open (standalone
	// or dev mode).
	IsAccessAllowed func(r *http.Request) bool

	// OnUnauthorized writes the response for a request IsAccessAllowed
	// denied — the host decides: a redirect to its login page, a JSON
	// 401 for API clients, etc. Nil replies "authentication required"
	// with status 401.
	OnUnauthorized func(w http.ResponseWriter, r *http.Request)

	// GetAuthUserID returns the authenticated user's ID for display
	// and audit purposes only — it is never consulted for the access
	// decision. Optional.
	GetAuthUserID func(r *http.Request) string

	// AdminHomeURL is the admin home link exposed to sections through
	// the request context (default: "/").
	AdminHomeURL string

	// NodeAdminURL is the base URL the node admin is mounted on —
	// injected into the request context for section link building
	// (default: "/nodes").
	NodeAdminURL string

	// Engine is the engine deployed workflows run in, expressed as the
	// narrow api.EngineInterface contract — nodeadmin does not depend
	// on a concrete runner (GOD ruling 2026-11-24; the stock
	// noderunner.WorkflowEngine satisfies it structurally). If nil a
	// fresh noderunner engine is created. On mount, engines that
	// support it resume every workflow whose persisted deployed flag
	// is set (restart-resume).
	Engine api.EngineInterface

	// FuncLayout is an optional function to render the admin interface
	// inside your own layout (branding, menus, etc.). If nil, the
	// default shared.Layout is used.
	FuncLayout shared.FuncLayout

	// DisableChrome hands chrome ownership to the host: the editor —
	// a standalone page that bypasses FuncLayout — emits no nodeadmin
	// navbar or section pills so an embedding host can wrap it in its
	// own chrome. Sections are unaffected (they already render through
	// FuncLayout when set). The zero value keeps the default chrome.
	DisableChrome bool
}

// Interface defines the node admin. It is a standard http.Handler and
// can be mounted on any router.
type Interface interface {
	http.Handler
	// Handle is the single admin-surface entry point: the
	// ?controller= query parameter is dispatched through the routes
	// map built once at construction.
	Handle(w http.ResponseWriter, r *http.Request)
	// HTTPRouter returns the shared-listener adapter mounted on this
	// handler — the nodebase.HTTPRouter the engine is wired with at New.
	// Routes registered through it serve on this handler, behind the
	// same access check.
	HTTPRouter() nodebase.HTTPRouter
}

// admin implements Interface
type admin struct {
	opts      Options
	handler   http.Handler
	endpoints *endpointRouter
	settings  *settings.Section
	dashboard *dashboard.Hub // nil unless the engine is a noderunner host
	routes    map[string]func(w http.ResponseWriter, r *http.Request)
}

// New creates a new node admin instance.
// Returns ErrStoreRequired if Options.Store is nil.
func New(opts Options) (Interface, error) {
	if opts.Store == nil {
		return nil, ErrStoreRequired
	}
	if opts.AdminHomeURL == "" {
		opts.AdminHomeURL = "/"
	}
	if opts.NodeAdminURL == "" {
		opts.NodeAdminURL = "/nodes"
	}
	if opts.Engine == nil {
		opts.Engine = noderunner.NewWorkflowEngine()
	}

	// Always-persisted messages (task always_persisted_messages):
	// the nodestore entity DB doubles as the shared handoff store — every
	// hop persists a node_inbox_messages row so any runner on the same DB
	// can drain undelivered work. A caller-supplied DurableInboxStore wins.
	// This wiring is noderunner-specific — a mount-site detail, not part
	// of the API contract — so it applies only to the stock engine.
	if engine, ok := opts.Engine.(*noderunner.WorkflowEngine); ok && engine.DurableInboxStore == nil {
		if sqlDB := opts.Store.GetEntityStore().GetDB(); sqlDB != nil {
			if inboxStore, err := noderunner.NewNeatInboxStore(sqlDB); err == nil {
				engine.DurableInboxStore = inboxStore
			} else {
				// Degrade to the in-memory fallback rather than fail the
				// mount — logged so the durability loss is visible.
				log.Printf("nodeadmin: durable inbox store unavailable (in-memory inboxes): %v", err)
			}
		}
	}
	// Persistent function-node context (node/flow/global scopes): the
	// same entity DB stores serialized context values. A caller-supplied
	// ContextStore wins — like the inbox and lease stores — and stores
	// without the context-value capability keep the engine's explicit
	// in-memory fallback.
	if engine, ok := opts.Engine.(*noderunner.WorkflowEngine); ok && engine.ContextStore == nil {
		if sqlDB := opts.Store.GetEntityStore().GetDB(); sqlDB != nil {
			// The persistent-context capability is discovered structurally
			// so this admin remains compilable against runner versions that
			// predate it; those versions keep the in-memory fallback.
			if wirer, ok := opts.Engine.(interface {
				ConfigureEntityContextStore(nodestore.StoreInterface) error
			}); ok {
				if err := wirer.ConfigureEntityContextStore(opts.Store); err != nil {
					log.Printf("nodeadmin: persistent context store unavailable (in-memory context): %v", err)
				}
			} else {
				log.Printf("nodeadmin: persistent context store unavailable (in-memory context): runner does not support entity context stores")
			}
		}
	}
	if engine, ok := opts.Engine.(*noderunner.WorkflowEngine); ok {
		// Lease coordination (P4): the same shared DB hosts the
		// workflow_leases/runners tables — a caller-supplied
		// LeaseStore wins, same precedence as the inbox store.
		if engine.LeaseStore == nil {
			if sqlDB := opts.Store.GetEntityStore().GetDB(); sqlDB != nil {
				if leaseStore, err := noderunner.NewNeatLeaseStore(sqlDB); err == nil {
					engine.LeaseStore = leaseStore
				} else {
					log.Printf("nodeadmin: lease store unavailable (single-runner mode): %v", err)
				}
			}
		}
		// Deploy-time credential injection (P4): stored flows carry
		// sentinel markers; the engine asks the credential entities
		// for the real values on every deploy path.
		if engine.CredentialsProvider == nil {
			engine.CredentialsProvider = opts.Store.CredentialMapForWorkflow
		}
	}

	// Persisted engine overrides ("engine" section of the global
	// config blob) win over the env-var/flag values at boot — same
	// seed flowadmin's settingsmanager runs.
	if raw, err := opts.Store.ConfigGet(context.Background(), "global_config"); err == nil && raw != "" {
		cfg := map[string]any{}
		if json.Unmarshal([]byte(raw), &cfg) == nil {
			settings.SeedEngineOverrides(opts.Engine, cfg)
		}
	}

	// Data migrations (P4/P5, all idempotent — safe at every startup):
	// legacy ATTRIBUTE_NODES/ATTRIBUTE_EDGES workflow blobs become
	// node/edge child entities; sidebar groupings move out of the
	// global-config blob into workflow_group entities; editor
	// view.groups become node_group entities with group_id stamped on
	// member nodebase. Order matters: the blob migration runs first so
	// group stamping finds real node entities. The migrator is a
	// *Store capability, kept off StoreInterface.
	if migrator, ok := opts.Store.(interface {
		MigrateWorkflowBlobAttributes(ctx context.Context) (int, error)
		MigrateWorkflowGroupsConfig(ctx context.Context, configKey string) (int, error)
		MigrateViewGroups(ctx context.Context) (int, error)
	}); ok {
		if n, err := migrator.MigrateWorkflowBlobAttributes(context.Background()); err != nil {
			log.Printf("nodeadmin: workflow blob migration failed: %v", err)
		} else if n > 0 {
			log.Printf("nodeadmin: migrated %d workflow(s) to node/edge entities", n)
		}
		if n, err := migrator.MigrateWorkflowGroupsConfig(context.Background(), "global_config"); err != nil {
			log.Printf("nodeadmin: workflow groups migration failed: %v", err)
		} else if n > 0 {
			log.Printf("nodeadmin: migrated %d workflow group(s) to entities", n)
		}
		if n, err := migrator.MigrateViewGroups(context.Background()); err != nil {
			log.Printf("nodeadmin: view groups migration failed: %v", err)
		} else if n > 0 {
			log.Printf("nodeadmin: migrated %d view group(s) to entities", n)
		}
	}

	a := &admin{opts: opts}

	// Install the shared-listener adapter BEFORE ResumeDeployed
	// launches below — the resume goroutine can deploy httpTrigger
	// "main" listeners immediately and must not see a nil router.
	// Engine wiring is noderunner-specific (a mount-site detail like
	// DurableInboxStore), so it applies through the concrete type.
	a.endpoints = newEndpointRouter()
	if engine, ok := opts.Engine.(*noderunner.WorkflowEngine); ok {
		engine.HTTPRouter = a.endpoints
		// Node-served listeners authenticate through the same host
		// seam as the admin surface — nodeadmin owns no credentials.
		switch {
		case opts.IsAccessAllowed != nil:
			engine.HTTPAuthenticator = func(r *http.Request) (string, bool) {
				if !opts.IsAccessAllowed(r) {
					return "", false
				}
				if opts.GetAuthUserID != nil {
					return opts.GetAuthUserID(r), true
				}
				return "", true
			}
		case opts.GetAuthUserID != nil:
			engine.HTTPAuthenticator = func(r *http.Request) (string, bool) {
				userID := opts.GetAuthUserID(r)
				return userID, userID != ""
			}
		}
		// Dashboard runtime bridge (task dashboard_hub_nodes):
		// the engine-agnostic hub lives in nodes/dashboard; the
		// noderunner-specific composition point is here — New binds
		// the UIWidgetSink seam and the hub is installed as the
		// engine's deploy observer so scopes follow deploys/stops.
		// ResumeDeployed below replays activations through it.
		if hub, err := dashboard.New(dashboard.Options{Host: engine}); err == nil {
			engine.SetDeployObserver(hub)
			a.dashboard = hub
		} else {
			log.Printf("nodeadmin: dashboard hub unavailable: %v", err)
		}
	}
	// Settings section (P4): snapshots the engine's startup values
	// once — build it before routes so ?controller=settings closes
	// over the single section instance.
	a.settings = settings.New(opts.Engine, opts.Store)
	a.settings.SetServerInfo(map[string]any{
		"authEnabled": opts.IsAccessAllowed != nil,
	})

	a.routes = a.buildRoutes()
	a.handler = a.buildHandler()

	// Restart-resume: redeploy every workflow whose persisted
	// deployed flag is set — durability is a semantic, not a flag.
	// Runs in the background so mounting never blocks on slow
	// activations; each failed workflow is logged individually.
	// ResumeDeployed is beyond the narrow API contract — engines that
	// support it are detected structurally.
	if resumer, ok := opts.Engine.(interface {
		ResumeDeployed(context.Context, nodestore.StoreInterface) []error
	}); ok {
		go func() {
			for _, err := range resumer.ResumeDeployed(context.Background(), opts.Store) {
				log.Printf("nodeadmin: resume deployed workflow: %v", err)
			}
		}()
	}

	return a, nil
}

// ServeHTTP implements http.Handler. Access is host-owned: when
// IsAccessAllowed is wired it is the only check — the response for a
// denied request is OnUnauthorized (default: plain 401).
func (a *admin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if a.opts.IsAccessAllowed != nil && !a.opts.IsAccessAllowed(r) {
		if a.opts.OnUnauthorized != nil {
			a.opts.OnUnauthorized(w, r)
		} else {
			http.Error(w, "authentication required", http.StatusUnauthorized)
		}
		return
	}

	// Shared-listener node routes (listener "main") are checked
	// first — after auth, so a mounted verbatim path wins over the
	// admin catch-all while staying behind the same credentials.
	if a.endpoints.serve(w, r) {
		return
	}
	a.handler.ServeHTTP(w, r)
}

// HTTPRouter returns the shared-listener adapter (see Interface).
func (a *admin) HTTPRouter() nodebase.HTTPRouter {
	return a.endpoints
}

// Handle is the single admin-surface entry point: config values are
// injected into the request context, then the ?controller= query
// parameter is looked up in the routes map.
func (a *admin) Handle(w http.ResponseWriter, r *http.Request) {
	ctx := context.WithValue(r.Context(), shared.KeyEndpoint, r.URL.Path)
	ctx = context.WithValue(ctx, shared.KeyAdminHomeURL, a.opts.AdminHomeURL)
	ctx = context.WithValue(ctx, shared.KeyNodeAdminURL, a.opts.NodeAdminURL)
	r = r.WithContext(ctx)

	controller := strings.TrimSpace(r.URL.Query().Get("controller"))
	if controller == "" {
		controller = shared.CONTROLLER_HOME
	}

	handler, ok := a.routes[controller]
	if !ok {
		handler = a.routes[shared.CONTROLLER_HOME]
	}

	handler(w, r)
}

// buildRoutes creates the section dispatch map once at construction
// time — controllers are functions over the shared UiConfig.
func (a *admin) buildRoutes() map[string]func(w http.ResponseWriter, r *http.Request) {
	uiConfig := shared.UiConfig{
		Store:  a.opts.Store,
		Engine: a.opts.Engine,
		Layout: a.render,
	}

	return map[string]func(w http.ResponseWriter, r *http.Request){
		shared.CONTROLLER_HOME:      func(w http.ResponseWriter, r *http.Request) { home.UI(uiConfig).Home(w, r) },
		shared.CONTROLLER_WORKFLOWS: func(w http.ResponseWriter, r *http.Request) { workflows.UI(uiConfig).Workflows(w, r) },
		shared.CONTROLLER_NODES:     func(w http.ResponseWriter, r *http.Request) { nodes.UI(uiConfig).Nodes(w, r) },
		shared.CONTROLLER_MESSAGES:  func(w http.ResponseWriter, r *http.Request) { messages.UI(uiConfig).Messages(w, r) },
		shared.CONTROLLER_EDITOR:    editor.New(a.opts.Store, !a.opts.DisableChrome).Editor,
		shared.CONTROLLER_SETTINGS:  func(w http.ResponseWriter, r *http.Request) { a.settings.UI(uiConfig).SettingsManager(w, r) },
	}
}

// render wraps section content in the layout. If FuncLayout is provided
// and returns non-empty, it is used; otherwise the default shared.Layout
// emits the minimal admin chrome.
func (a *admin) render(w http.ResponseWriter, r *http.Request, webpageTitle, webpageHtml string, options shared.LayoutOptions) string {
	if a.opts.FuncLayout != nil {
		if custom := a.opts.FuncLayout(w, r, webpageTitle, webpageHtml, options); custom != "" {
			_, _ = w.Write([]byte(custom))
			return ""
		}
	}
	webpage := shared.Layout(w, r, webpageTitle, webpageHtml, options)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(webpage))
	return ""
}

// buildHandler assembles the internal handler: the single admin UI
// surface on "/" plus the JSON API under /api/*.
func (a *admin) buildHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", api.New(a.opts.Store, a.opts.Engine))
	// Dashboard: the runtime bridge (websocket sync + JSON manifest)
	// on its two literal endpoints plus the client page shell on
	// every other GET under the mount — deep links serve the shell,
	// which derives the ws URL from its own path. Prefix-stripped so
	// the hub stays mount-agnostic.
	if a.dashboard != nil {
		hub := http.StripPrefix("/dashboard", a.dashboard)
		mux.Handle("GET /dashboard/ws", hub)
		mux.Handle("GET /dashboard/manifest", hub)
		mux.HandleFunc("GET /dashboard/", dashpage.Page)
	}
	mux.HandleFunc("/", a.Handle)
	return mux
}
