//go:build e2e

package e2e

import (
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"
)

// TestSettingsMountsAndSaves covers scenario (d): the settings Vue
// app mounts, a field edit + Save persists an engine override, and a
// reload shows the saved value.
func TestSettingsMountsAndSaves(t *testing.T) {
	srv := newTestServer(t, t.TempDir()+"/e2e-settings.db")
	defer srv.close()
	browser := newBrowser(t)

	page, w := mustPage(t, browser, srv.browserURL("/?controller=settings"))
	waitVueApp(t, page)

	// Vue mounted and finished loading: the form is visible.
	waitElement(t, page, "#maxSteps")

	// Set a max-steps override and save.
	setInput(t, page, "#maxSteps", "1234")
	clickText(t, page, "button", "Save Changes")

	// The save roundtrips and reloads the form; assert via a fresh
	// page load that the override persisted in the config store.
	require.NoError(t, chromedp.Run(page,
		chromedp.Navigate(srv.browserURL("/?controller=settings"))))
	waitVueApp(t, page)
	waitElement(t, page, "#maxSteps")
	require.Equal(t, "1234", inputValue(t, page, "#maxSteps"))

	if errs := w.errors(); len(errs) > 0 {
		t.Fatalf("console errors on settings: %v", errs)
	}
}
