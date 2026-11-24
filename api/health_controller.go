package api

import (
	"net/http"

	guapi "github.com/gouniverse/api"
)

// healthCheck is a liveness probe endpoint
func (a *Api) healthCheck(w http.ResponseWriter, r *http.Request) {
	respond(w, guapi.Success("healthy"))
}
