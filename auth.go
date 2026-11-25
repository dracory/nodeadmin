package nodeadmin

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/dracory/nodestore"

	"golang.org/x/crypto/bcrypt"
)

// authFile mirrors the Node-RED settings.js adminAuth shape: a JSON
// document holding a "users" list with a username and a bcrypt
// password hash each, e.g.
//
//	{"users": [{"username": "admin", "password": "$2a$10$..."}]}
//
// Hashes can be produced with HashPassword (or Node-RED's own
// `node-red-admin hash-pw` — the format is compatible). The same shape
// is used for the interim config-store credentials (see
// configKeyAuthUsers).
type authFile struct {
	Users []authUser `json:"users"`
}

// authUser is one credential entry in the auth file
type authUser struct {
	Username string `json:"username"`
	// Password is a bcrypt hash ($2a$/$2b$…)
	Password string `json:"password"`
}

// configKeyAuthUsers is the interim config-store key holding the
// admin credentials document (authFile JSON shape). Interim source
// per the P2 design ruling: a dedicated creds entity lands in P4 and
// replaces this key.
const configKeyAuthUsers = "admin.auth"

// HashPassword returns a bcrypt hash suitable for the auth file's
// "password" field
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(h), err
}

// parseAuthDocument validates a decoded auth document
func parseAuthDocument(raw []byte, origin string) ([]authUser, error) {
	var f authFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("nodeadmin: invalid auth document in %s: %w", origin, err)
	}
	if len(f.Users) == 0 {
		return nil, fmt.Errorf("nodeadmin: auth document in %s contains no users", origin)
	}
	return f.Users, nil
}

// loadAuthFile reads and validates the credentials file
func loadAuthFile(path string) ([]authUser, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("nodeadmin: cannot read auth file: %w", err)
	}
	return parseAuthDocument(raw, path)
}

// loadAuthConfig reads the interim credentials document from the
// nodestore config store (key configKeyAuthUsers, authFile JSON
// shape). Returns nil users when the key is unset — credentials
// simply are not configured yet.
func loadAuthConfig(ctx context.Context, store nodestore.StoreInterface) ([]authUser, error) {
	raw, err := store.ConfigGet(ctx, configKeyAuthUsers)
	if err != nil {
		return nil, fmt.Errorf("nodeadmin: cannot read auth config %q: %w", configKeyAuthUsers, err)
	}
	if raw == "" {
		return nil, nil
	}
	return parseAuthDocument([]byte(raw), "config store key "+configKeyAuthUsers)
}

// dummyHash is a valid bcrypt hash compared for unknown usernames so
// the response time does not reveal whether the account exists.
var dummyHash = []byte("$2a$10$7EqJtq98hPqEX7fNZaFWoOhi/RlqSdNI7i5Bd5hYRT1AdzF7Q5pZu")

// basicAuthUser verifies the request's HTTP Basic credentials against
// the loaded users (constant-time username compare, bcrypt for the
// password — always run, even for unknown users, to avoid user
// enumeration via timing) and returns the authenticated username.
func basicAuthUser(r *http.Request, users []authUser) (string, bool) {
	username, password, ok := r.BasicAuth()
	if !ok {
		return "", false
	}
	matched := false
	hash := dummyHash
	for _, u := range users {
		if subtle.ConstantTimeCompare([]byte(u.Username), []byte(username)) == 1 {
			matched = true
			hash = []byte(u.Password)
			break
		}
	}
	if matched && bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil {
		return username, true
	}
	return "", false
}

// BasicAuthenticator loads a Node-RED-style auth file and returns a
// request authenticator reporting the authenticated username — the
// signature noderunner.WorkflowEngine.HTTPAuthenticator expects, so
// runtime listeners (submitIn auth "admin") share the admin
// credentials.
func BasicAuthenticator(path string) (func(r *http.Request) (string, bool), error) {
	users, err := loadAuthFile(path)
	if err != nil {
		return nil, err
	}
	return func(r *http.Request) (string, bool) {
		return basicAuthUser(r, users)
	}, nil
}

// requireAuth replies with a Basic-auth challenge
func requireAuth(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="nodeadmin"`)
	http.Error(w, "authentication required", http.StatusUnauthorized)
}
