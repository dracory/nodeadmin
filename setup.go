package nodeadmin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"strings"

	"github.com/dracory/noderun"
	"github.com/dracory/nodestore"
)

// First-run setup (Jenkins/Grafana model): when no admin credentials
// resolve from any source — the auth file, seeded options, or the
// config store — the admin enters setup mode. All routes redirect to
// a token-gated wizard that creates the first admin user.
//
// configKeySetupToken holds the setup token in the store config so
// every runner sharing the DB sees the same token; it is minted once
// on first entry into setup mode and never regenerated while setup is
// incomplete. configKeySetupCompleted is the marker written together
// with the credentials document so a mid-setup restart re-enters the
// wizard cleanly.
const (
	configKeySetupToken     = "admin.setup_token"
	configKeySetupCompleted = "admin.setup_completed"
)

// generateSetupToken returns a fresh random setup token (Jenkins-style
// unlock key — proof of access to the consuming application's log).
func generateSetupToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// ensureSetupToken returns the stored setup token, minting and
// persisting one on first use. The stored value is shared across
// runners — a second boot reuses it verbatim.
func ensureSetupToken(ctx context.Context, store nodestore.StoreInterface) (string, error) {
	token, err := store.ConfigGet(ctx, configKeySetupToken)
	if err != nil {
		return "", fmt.Errorf("nodeadmin: cannot read setup token: %w", err)
	}
	if token != "" {
		return token, nil
	}
	token, err = generateSetupToken()
	if err != nil {
		return "", fmt.Errorf("nodeadmin: cannot generate setup token: %w", err)
	}
	if err := store.ConfigSet(ctx, configKeySetupToken, token); err != nil {
		return "", fmt.Errorf("nodeadmin: cannot persist setup token: %w", err)
	}
	return token, nil
}

// writeAdminCredentials persists the credentials document (authFile
// JSON shape under configKeyAuthUsers) and the completed marker. The
// creds write lands first: a crash between the two leaves resolvable
// users, which the resolution order treats as configured — never a
// half-state that skips auth or loops the wizard.
func writeAdminCredentials(ctx context.Context, store nodestore.StoreInterface, username, passwordHash string) error {
	doc, err := json.Marshal(authFile{Users: []authUser{{Username: username, Password: passwordHash}}})
	if err != nil {
		return err
	}
	if err := store.ConfigSet(ctx, configKeyAuthUsers, string(doc)); err != nil {
		return err
	}
	return store.ConfigSet(ctx, configKeySetupCompleted, "1")
}

// setupPath returns the mount-relative wizard path
// ("/setup" under whatever base the host mounted the admin on).
func (a *admin) setupPath() string {
	return strings.TrimSuffix(a.opts.NodeAdminURL, "/") + "/setup"
}

// mountRelativePath strips the mount base from the request path so
// wizard matching works whether the host strips its prefix or not.
func (a *admin) mountRelativePath(r *http.Request) string {
	p := r.URL.Path
	if base := strings.TrimSuffix(a.opts.NodeAdminURL, "/"); base != "" && strings.HasPrefix(p, base+"/") {
		p = p[len(base):]
	}
	return p
}

// serveSetup implements the setup-mode gate: only the wizard
// endpoints are reachable; every other route — including
// shared-listener node routes — redirects to the setup page.
func (a *admin) serveSetup(w http.ResponseWriter, r *http.Request) {
	if a.mountRelativePath(r) == "/setup" {
		switch r.Method {
		case http.MethodGet:
			a.setupPage(w, r, "")
		case http.MethodPost:
			a.setupSubmit(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	http.Redirect(w, r, a.setupPath(), http.StatusSeeOther)
}

// setupPage renders the token-gated wizard. Wording is generic —
// nodeadmin is a public module; the consuming application supplies
// the context (its startup log holds the token).
func (a *admin) setupPage(w http.ResponseWriter, r *http.Request, errMsg string) {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html><head><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">")
	b.WriteString("<title>First-run setup</title></head><body>")
	b.WriteString("<main style=\"max-width:32rem;margin:4rem auto;font-family:sans-serif\">")
	b.WriteString("<h1>First-run setup</h1>")
	b.WriteString("<p>No admin user exists yet. Enter the setup token printed to the " +
		"startup log of the consuming application, then choose the admin credentials.</p>")
	if errMsg != "" {
		b.WriteString("<p style=\"color:#b00\">" + html.EscapeString(errMsg) + "</p>")
	}
	b.WriteString("<form method=\"post\" action=\"" + html.EscapeString(a.setupPath()) + "\">")
	b.WriteString("<p><label>Setup token<br><input type=\"text\" name=\"token\" required autocomplete=\"off\"></label></p>")
	b.WriteString("<p><label>Username<br><input type=\"text\" name=\"username\" required autocomplete=\"username\"></label></p>")
	b.WriteString("<p><label>Password<br><input type=\"password\" name=\"password\" required autocomplete=\"new-password\"></label></p>")
	b.WriteString("<p><button type=\"submit\">Create admin user</button></p>")
	b.WriteString("</form></main></body></html>")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

// setupSubmit validates the setup token and creates the first admin
// user. A wrong token is a flat 403 — same response regardless of the
// other fields, so the endpoint leaks nothing about setup state.
func (a *admin) setupSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Form.Get("token")), []byte(a.setupToken)) != 1 {
		http.Error(w, "invalid setup token", http.StatusForbidden)
		return
	}
	username := strings.TrimSpace(r.Form.Get("username"))
	password := r.Form.Get("password")
	if username == "" || password == "" {
		a.setupPage(w, r, "Username and password are required.")
		return
	}
	hash, err := HashPassword(password)
	if err != nil {
		http.Error(w, "cannot hash password", http.StatusInternalServerError)
		return
	}
	if err := writeAdminCredentials(r.Context(), a.opts.Store, username, hash); err != nil {
		http.Error(w, "cannot persist credentials", http.StatusInternalServerError)
		return
	}

	// Flip this instance out of setup mode immediately — no restart
	// needed; the stored values already cover every other runner's
	// next boot.
	users := []authUser{{Username: username, Password: hash}}
	a.mu.Lock()
	a.users = users
	a.setupMode = false
	a.mu.Unlock()
	if engine, ok := a.opts.Engine.(*noderunner.WorkflowEngine); ok {
		engine.HTTPAuthenticator = func(r *http.Request) (string, bool) {
			return basicAuthUser(r, users)
		}
	}
	log.Printf("nodeadmin: first-run setup completed — admin user %q created", username)

	home := a.opts.NodeAdminURL
	if home == "" {
		home = "/"
	}
	http.Redirect(w, r, home, http.StatusSeeOther)
}
