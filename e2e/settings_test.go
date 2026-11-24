//go:build e2e

package e2e

import (
	"testing"

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
	page.MustElement("#maxSteps").MustWaitVisible()

	// Set a max-steps override and save.
	page.MustElement("#maxSteps").MustSelectAllText().MustInput("1234")
	page.MustElementR("button", "Save Changes").MustClick()

	// The save roundtrips and reloads the form; assert via a fresh
	// page load that the override persisted in the config store.
	page.MustNavigate(srv.browserURL("/?controller=settings"))
	waitVueApp(t, page)
	input := page.MustElement("#maxSteps").MustWaitVisible()
	require.Equal(t, "1234", input.MustProperty("value").String())

	if errs := w.errors(); len(errs) > 0 {
		t.Fatalf("console errors on settings: %v", errs)
	}
}
