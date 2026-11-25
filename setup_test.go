package nodeadmin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/dracory/nodestore"
)

// readSetupToken returns the stored setup token — the value a second
// boot must reuse verbatim (Jenkins model: shared across runners,
// never regenerated while setup is incomplete).
func readSetupToken(t *testing.T, store nodestore.StoreInterface) string {
	t.Helper()
	token, err := store.ConfigGet(context.Background(), configKeySetupToken)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// TestSetupModeGate covers the fresh-DB acceptance criterion: no auth
// sources → a setup token is stored, every admin route redirects to
// the setup page, wizard endpoints are the only reachable surface,
// and a wrong token is a 403.
func TestSetupModeGate(t *testing.T) {
	store := newTestStore(t)
	admin, err := New(Options{Store: store})
	if err != nil {
		t.Fatal(err)
	}

	token := readSetupToken(t, store)
	if token == "" {
		t.Fatal("setup mode must mint a setup token into the store config")
	}

	// GET / redirects to the setup page
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusSeeOther || !strings.HasSuffix(rec.Header().Get("Location"), "/setup") {
		t.Fatalf("expected 303 to the setup page, got %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// The API surface redirects too — nothing open during setup
	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/workflows", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("api route must redirect in setup mode, got %d", rec.Code)
	}

	// The setup page itself is reachable
	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("setup page must render, got %d", rec.Code)
	}

	// Wrong token → 403
	form := url.Values{"token": {"wrong"}, "username": {"admin"}, "password": {"s3cret"}}
	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode())))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("wrong token must be 403, got %d", rec.Code)
	}
}

// TestSetupTokenReusedAcrossRestarts simulates a second process/runner
// on the same DB before setup completion: the stored token is reused,
// never regenerated.
func TestSetupTokenReusedAcrossRestarts(t *testing.T) {
	store := newTestStore(t)
	if _, err := New(Options{Store: store}); err != nil {
		t.Fatal(err)
	}
	first := readSetupToken(t, store)

	// "Restart": a fresh admin on the same store must see the same token
	admin, err := New(Options{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	second := readSetupToken(t, store)
	if first == "" || first != second {
		t.Fatalf("second boot must reuse the stored token, got %q vs %q", first, second)
	}
	_ = admin
}

// TestSetupCompletes covers the wizard acceptance criterion: submit
// writes a bcrypt admin user + completed marker; subsequent requests
// enforce HTTP Basic with the new credentials; a second process on
// the same DB sees no wizard.
func TestSetupCompletes(t *testing.T) {
	store := newTestStore(t)
	admin, err := New(Options{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	token := readSetupToken(t, store)

	form := url.Values{"token": {token}, "username": {"boss"}, "password": {"sup3rs3cret"}}
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("successful setup must redirect, got %d", rec.Code)
	}

	// Creds + completed marker written to the store
	raw, err := store.ConfigGet(context.Background(), configKeyAuthUsers)
	if err != nil || !strings.Contains(raw, `"boss"`) {
		t.Fatalf("admin.auth must hold the new user, got %q err %v", raw, err)
	}
	marker, err := store.ConfigGet(context.Background(), configKeySetupCompleted)
	if err != nil || marker == "" {
		t.Fatalf("completed marker must be set, got %q err %v", marker, err)
	}

	// Subsequent requests enforce Basic with the new credentials
	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("post-setup requests must challenge, got %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("boss", "sup3rs3cret")
	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("new credentials must authenticate, got %d", rec.Code)
	}

	// Second process on the same DB: no wizard, same credentials enforced
	admin2, err := New(Options{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	admin2.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("second boot must enforce auth, not setup, got %d", rec.Code)
	}
}

// TestSeedAdmin covers the env-seeded path (Grafana model): seeded
// credentials create the admin at boot — no setup mode — and are
// ignored on restart once users exist.
func TestSeedAdmin(t *testing.T) {
	store := newTestStore(t)
	admin, err := New(Options{Store: store, SeedAdminUser: "root", SeedAdminPassword: "envs3cret"})
	if err != nil {
		t.Fatal(err)
	}

	// No setup mode: requests challenge for Basic, not redirect
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("seeded admin must gate with Basic, got %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("root", "envs3cret")
	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("seeded credentials must authenticate, got %d", rec.Code)
	}

	// Restart with different env values: ignored — stored users win
	admin2, err := New(Options{Store: store, SeedAdminUser: "other", SeedAdminPassword: "changed"})
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("other", "changed")
	rec = httptest.NewRecorder()
	admin2.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("seed vars must be ignored once users exist, got %d", rec.Code)
	}
}

// TestSetupModeGatesListenerRoutes covers the recommended gate scope:
// shared-listener node routes are unreachable during setup too.
func TestSetupModeGatesListenerRoutes(t *testing.T) {
	store := newTestStore(t)
	admin, err := New(Options{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.HTTPRouter().Handle("GET /webhook", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/webhook", nil))
	if rec.Code == http.StatusTeapot {
		t.Fatal("shared-listener route must be gated during setup")
	}
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect to setup, got %d", rec.Code)
	}
}
