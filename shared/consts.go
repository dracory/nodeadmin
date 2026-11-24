package shared

// context keys used on the admin UI surface
const (
	KeyEndpoint     = "endpoint"
	KeyAdminHomeURL = "admin_home_url"
	KeyNodeAdminURL = "node_admin_url"
)

// ?controller= dispatch names
const (
	CONTROLLER_HOME      = "home"
	CONTROLLER_WORKFLOWS = "workflows"
	CONTROLLER_MESSAGES  = "messages"
	CONTROLLER_EDITOR    = "editor"
	CONTROLLER_SETTINGS  = "settings"
)

// MountAppJS mounts the shared app-navigation chrome — every section
// page passes it via LayoutOptions.Scripts so the navbar renders
// (the shell layout emits <app-navigation> inside #app)
const MountAppJS = `// Node admin Vue app — mounts the shared app-navigation chrome.
const { createApp } = Vue;
const app = createApp({});
app.component('app-navigation', window.AppNavigation);
app.mount('#app');`
