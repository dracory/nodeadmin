//go:build e2e

package e2e

import (
	"testing"
	"time"
)

// TestSectionPagesRender covers scenario (a): every ?controller=
// section renders the shared navbar and the section quick bar (nav
// pills) with a clean console.
func TestSectionPagesRender(t *testing.T) {
	srv := newTestServer(t, t.TempDir()+"/e2e-sections.db")
	defer srv.close()
	browser := newBrowser(t)

	controllers := []string{"home", "workflows", "messages", "editor", "settings"}
	for _, c := range controllers {
		t.Run(c, func(t *testing.T) {
			page, w := mustPage(t, browser, srv.browserURL("/?controller="+c))

			// Top navbar (brand + section links + theme switcher).
			waitElement(t, page, "#admin-navbar")
			// Quick bar: the section nav pills under the navbar.
			waitElement(t, page, ".nav-pills")

			// Wait for the load event to settle (deferred module errors
			// — e.g. Vue mount failures — surface as uncaught
			// exceptions by then), then a short beat for async work.
			eventually(t, 15*time.Second, "document readyState complete", func() bool {
				return evalBool(t, page, `document.readyState === 'complete'`)
			})
			time.Sleep(500 * time.Millisecond)
			if errs := w.errors(); len(errs) > 0 {
				t.Fatalf("console errors on ?controller=%s: %v", c, errs)
			}
		})
	}
}
