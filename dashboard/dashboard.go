// Package dashboard serves the end-user dashboard client page —
// the single-file HTML shell + widget renderers ported from the
// retiring pkg/flowdashboard. The runtime half (manifest/update/
// input-injection websocket protocol) lives in
// nodebase/dashboard and is mounted by nodeadmin at
// /dashboard/ws and /dashboard/manifest; this package owns only the
// page itself, served for every other GET under the /dashboard/
// mount so deep links all return the same shell. The client derives
// the ws endpoint from its own URL (base + "ws"), so the page works
// under any mount prefix.
package dashboard

import (
	_ "embed"
	"net/http"
)

//go:embed dashboard.html
var pageHTML []byte

// Page serves the embedded single-file dashboard shell.
func Page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(pageHTML)
}
