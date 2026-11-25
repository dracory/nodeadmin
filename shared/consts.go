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
	CONTROLLER_NODES     = "nodes"
	CONTROLLER_MESSAGES  = "messages"
	CONTROLLER_EDITOR    = "editor"
	CONTROLLER_SETTINGS  = "settings"
)
