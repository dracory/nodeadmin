// Package settings is the settings section of the node admin, modeled
// on flowadmin's settingsmanager (itself the taskadmin/shopadmin
// folder-per-controller pattern): the nodeadmin dispatch map invokes
// section.UI(uiConfig).SettingsManager(w, r) for ?controller=settings —
// no action renders the page through the UiConfig layout,
// ?action=load-settings and ?action=save-setting answer JSON AJAX
// requests in the dracory/api envelope under the same controller URL.
//
// Editable values are runtime engine overrides persisted inside the
// config store's "global_config" blob (the "engine" section next to
// "env") and applied to the live engine; startup/env-var values and
// server facts are reported read-only. The package never names a
// runner in its exported surface — the stock
// noderunner.WorkflowEngine is asserted structurally.
package settings

import (
	"encoding/json"
	"net/http"
	"strings"

	guapi "github.com/dracory/api"

	"github.com/dracory/nodeadmin/shared"
	"github.com/dracory/noderun"
	"github.com/dracory/nodestore"
)

const (
	actionLoadSettings = "load-settings"
	actionSaveSetting  = "save-setting"
)

// globalConfigKey is the config-store key holding the global-config
// blob (env, engine sections) — the same key the nodeadmin API uses.
const globalConfigKey = "global_config"

// engineConfigKey is the blob section carrying persisted engine
// overrides ({"engine": {maxSteps?, allowExec?, fileRoot?}}).
const engineConfigKey = "engine"

// saveSettingRequest carries the full override set the settings page
// submits. Omitted keys (null/absent) clear that override.
type saveSettingRequest struct {
	MaxSteps  *int    `json:"maxSteps"`
	AllowExec *bool   `json:"allowExec"`
	FileRoot  *string `json:"fileRoot"`
}

// engineOverridesFromConfig decodes the persisted "engine" section.
func engineOverridesFromConfig(raw any) saveSettingRequest {
	var req saveSettingRequest
	m, ok := raw.(map[string]any)
	if !ok {
		return req
	}
	if v, ok := m["maxSteps"]; ok {
		if f, ok := v.(float64); ok {
			n := int(f)
			req.MaxSteps = &n
		}
	}
	if v, ok := m["allowExec"]; ok {
		if b, ok := v.(bool); ok {
			req.AllowExec = &b
		}
	}
	if v, ok := m["fileRoot"]; ok {
		if s, ok := v.(string); ok {
			req.FileRoot = &s
		}
	}
	return req
}

// overridesMap serializes an override set for the config blob; only
// keys actually overridden are present.
func overridesMap(req saveSettingRequest) map[string]any {
	m := map[string]any{}
	if req.MaxSteps != nil {
		m["maxSteps"] = *req.MaxSteps
	}
	if req.AllowExec != nil {
		m["allowExec"] = *req.AllowExec
	}
	if req.FileRoot != nil {
		m["fileRoot"] = *req.FileRoot
	}
	return m
}

// UiInterface defines the settings section's UI interface.
type UiInterface interface {
	SettingsManager(w http.ResponseWriter, r *http.Request)
}

// Section owns the settings surface state: the startup snapshot (taken
// before persisted overrides apply), the engine/store handles and the
// host-supplied server facts.
type Section struct {
	engine           *noderunner.WorkflowEngine // nil for foreign engines
	store            nodestore.StoreInterface
	startupMaxSteps  int
	startupAllowExec bool
	startupFileRoot  string
	serverInfo       map[string]any
}

// ui implements UiInterface — the per-request handler carrying the
// section state plus this request's UiConfig.
type ui struct {
	section *Section
	config  shared.UiConfig
}

// New creates the settings section. engine may be anything — the stock
// *noderunner.WorkflowEngine gets live tuning; foreign engines report
// an empty engine block. Startup values are snapshotted before
// persisted overrides apply so clearing an override restores them.
func New(engine any, store nodestore.StoreInterface) *Section {
	s := &Section{store: store}
	if e, ok := engine.(*noderunner.WorkflowEngine); ok && e != nil {
		s.engine = e
		s.startupMaxSteps = e.MaxSteps
		s.startupAllowExec = e.AllowExec
		s.startupFileRoot = e.FileRoot
	}
	return s
}

// SeedEngineOverrides applies persisted engine overrides (the "engine"
// section of the global-config blob) onto the engine at boot — called
// by nodeadmin.New so env-var/flag values remain the fallback for keys
// with no override. No-op for foreign engines.
func SeedEngineOverrides(engine any, cfg map[string]any) {
	e, ok := engine.(*noderunner.WorkflowEngine)
	if !ok || e == nil {
		return
	}
	s := &Section{engine: e}
	s.startupMaxSteps = e.MaxSteps
	s.startupAllowExec = e.AllowExec
	s.startupFileRoot = e.FileRoot
	s.applyEngineOverrides(engineOverridesFromConfig(cfg[engineConfigKey]))
}

// SetServerInfo installs host-supplied facts (auth enabled, ...)
// reported read-only by load-settings; safe to leave unset.
func (s *Section) SetServerInfo(info map[string]any) {
	s.serverInfo = info
}

// UI creates a new settings section UI from the given config.
func (s *Section) UI(config shared.UiConfig) UiInterface {
	return &ui{section: s, config: config}
}

// SettingsManager is the single ?controller=settings endpoint:
// ?action= dispatches to the JSON AJAX handlers, anything else renders
// the page through the UiConfig layout.
func (u *ui) SettingsManager(w http.ResponseWriter, r *http.Request) {
	// Same cross-site/JSON gates as the admin API: a "simple"
	// cross-origin POST needs no CORS preflight, so without them any
	// web page could drive the save action on localhost.
	if r.Method == http.MethodPost {
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "cross-site requests are not allowed", http.StatusForbidden)
			return
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			http.Error(w, "unsupported media type: application/json required", http.StatusUnsupportedMediaType)
			return
		}
	}

	switch strings.TrimSpace(r.URL.Query().Get("action")) {
	case actionLoadSettings:
		u.section.handleLoadSettings(w, r)
	case actionSaveSetting:
		u.section.handleSaveSetting(w, r)
	default:
		u.renderPage(w, r)
	}
}

// globalConfig loads the whole config blob as a map.
func (s *Section) globalConfig(r *http.Request) (map[string]any, error) {
	raw, err := s.store.ConfigGet(r.Context(), globalConfigKey)
	if err != nil || raw == "" {
		return map[string]any{}, err
	}
	cfg := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return map[string]any{}, err
	}
	return cfg, nil
}

// handleLoadSettings reports the effective engine values, the startup
// values, the persisted overrides, and read-only server facts.
func (s *Section) handleLoadSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.globalConfig(r)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	overrides := engineOverridesFromConfig(cfg[engineConfigKey])

	engine := map[string]any{}
	if s.engine != nil {
		engine = map[string]any{
			"maxSteps":        s.engine.MaxSteps,
			"allowExec":       s.engine.AllowExec,
			"fileRoot":        s.engine.FileRoot,
			"runnerId":        s.engine.RunnerID,
			"runnerVersion":   s.engine.RunnerVersion,
			"defaultMaxSteps": noderunner.DefaultMaxSteps,
		}
	}

	server := map[string]any{}
	for k, v := range s.serverInfo {
		server[k] = v
	}

	respond(w, guapi.SuccessWithData("settings retrieved successfully", map[string]any{
		"engine":    engine,
		"startup":   map[string]any{"maxSteps": s.startupMaxSteps, "allowExec": s.startupAllowExec, "fileRoot": s.startupFileRoot},
		"overrides": overridesMap(overrides),
		"server":    server,
		// The config store is part of StoreInterface — overrides
		// always persist.
		"globalConfigSupported": true,
	}))
}

// handleSaveSetting replaces the persisted engine-override set and
// applies it live.
func (s *Section) handleSaveSetting(w http.ResponseWriter, r *http.Request) {
	var req saveSettingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	cfg, err := s.globalConfig(r)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	om := overridesMap(req)
	if len(om) == 0 {
		delete(cfg, engineConfigKey)
	} else {
		cfg[engineConfigKey] = om
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	if err := s.store.ConfigSet(r.Context(), globalConfigKey, string(raw)); err != nil {
		respond(w, guapi.Error(err.Error()))
		return
	}
	s.applyEngineOverrides(req)
	respond(w, guapi.SuccessWithData("settings updated successfully", map[string]any{
		"overrides": om,
	}))
}

// applyEngineOverrides pushes an override set onto the engine,
// falling back to the startup snapshot for keys with no override.
func (s *Section) applyEngineOverrides(req saveSettingRequest) {
	if s.engine == nil {
		return
	}
	if req.MaxSteps != nil {
		s.engine.MaxSteps = *req.MaxSteps
	} else {
		s.engine.MaxSteps = s.startupMaxSteps
	}
	if req.AllowExec != nil {
		s.engine.AllowExec = *req.AllowExec
	} else {
		s.engine.AllowExec = s.startupAllowExec
	}
	if req.FileRoot != nil {
		s.engine.FileRoot = *req.FileRoot
	} else {
		s.engine.FileRoot = s.startupFileRoot
	}
}

// respond writes a dracory/api envelope as JSON
func respond(w http.ResponseWriter, res interface{ ToString() string }) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(res.ToString()))
}
