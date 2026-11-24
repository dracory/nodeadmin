package nodeadmin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// mount mounts a recording handler and fails the test on conflict.
func mount(t *testing.T, r *endpointRouter, pattern string) {
	t.Helper()
	if err := r.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(pattern))
	})); err != nil {
		t.Fatalf("mount %q: %v", pattern, err)
	}
}

// request drives the router's manual dispatch and returns the recorder.
func request(r *endpointRouter, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	if !r.serve(rec, req) {
		rec.WriteHeader(http.StatusNotFound)
	}
	return rec
}

func TestRouterServesLiteralRoute(t *testing.T) {
	r := newEndpointRouter()
	mount(t, r, "GET /hello")
	if rec := request(r, http.MethodGet, "/hello"); rec.Code != http.StatusOK || rec.Body.String() != "GET /hello" {
		t.Fatalf("unexpected response: %d %q", rec.Code, rec.Body.String())
	}
	if rec := request(r, http.MethodGet, "/missing"); rec.Code != http.StatusNotFound {
		t.Fatalf("unmounted path must fall through, got %d", rec.Code)
	}
}

func TestRouterWildcardAndPathValue(t *testing.T) {
	r := newEndpointRouter()
	var got string
	if err := r.Handle("GET /hook/{id}", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got = req.PathValue("id")
		w.WriteHeader(http.StatusOK)
	})); err != nil {
		t.Fatal(err)
	}
	if rec := request(r, http.MethodGet, "/hook/42"); rec.Code != http.StatusOK || got != "42" {
		t.Fatalf("wildcard dispatch failed: %d param=%q", rec.Code, got)
	}
}

func TestRouterLiteralBeatsWildcard(t *testing.T) {
	r := newEndpointRouter()
	mount(t, r, "GET /a/{x}")
	mount(t, r, "GET /a/b") // overlapping but strictly more specific — allowed
	if rec := request(r, http.MethodGet, "/a/b"); rec.Body.String() != "GET /a/b" {
		t.Fatalf("literal segment must win, got %q", rec.Body.String())
	}
	if rec := request(r, http.MethodGet, "/a/c"); rec.Body.String() != "GET /a/{x}" {
		t.Fatalf("wildcard route must serve non-literal path, got %q", rec.Body.String())
	}
}

func TestRouterRejectsAmbiguousOverlap(t *testing.T) {
	r := newEndpointRouter()
	mount(t, r, "GET /a/{x}/c")
	// Equal specificity, literal/wildcard in different positions —
	// the case http.ServeMux panics on; must be an error instead.
	if err := r.Handle("GET /a/b/{y}", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})); err == nil {
		t.Fatal("ambiguous wildcard overlap must be a conflict error, not a panic")
	}
	// Same shape under different wildcard names is ambiguous too.
	if err := r.Handle("GET /a/{y}/c", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})); err == nil {
		t.Fatal("renamed-wildcard duplicate must conflict")
	}
	// Disjoint methods never conflict.
	mount(t, r, "POST /a/{x}/c")
	// Exact duplicate conflicts even when the method differs? No —
	// methods differ so both can coexist; exact same pattern errors.
	if err := r.Handle("GET /a/{x}/c", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})); err == nil {
		t.Fatal("exact duplicate must conflict")
	}
}

func TestRouterMethodMismatch405(t *testing.T) {
	r := newEndpointRouter()
	mount(t, r, "POST /submit")
	rec := request(r, http.MethodGet, "/submit")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestRouterUnregister(t *testing.T) {
	r := newEndpointRouter()
	mount(t, r, "GET /gone")
	r.Unregister("GET /gone")
	if rec := request(r, http.MethodGet, "/gone"); rec.Code != http.StatusNotFound {
		t.Fatalf("unregistered route must fall through, got %d", rec.Code)
	}
	r.Unregister("GET /never") // idempotent, must not panic
}

func TestRouterReservedPrefixes(t *testing.T) {
	r := newEndpointRouter()
	for _, p := range []string{"GET /", "GET /api/x", "POST /api", "GET /editor", "GET /css/app.css", "GET /js/app.js"} {
		if err := r.Handle(p, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})); err == nil {
			t.Fatalf("reserved route %q must be refused", p)
		}
	}
}
