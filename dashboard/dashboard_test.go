package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPage serves the embedded dashboard shell and guards its
// mount-point invariants: HTML content type, a <title>, and the root
// element the client renders into.
func TestPage(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/dashboard/", nil)
	rec := httptest.NewRecorder()
	Page(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}
	body := rec.Body.String()
	for _, needle := range []string{"<title>Flow Dashboard</title>", "<main"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("page missing %q", needle)
		}
	}
}
