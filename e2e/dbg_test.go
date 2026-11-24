//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

func TestDbg(t *testing.T) {
	srv := newTestServer(t, t.TempDir()+"/dbg.db")
	defer srv.close()

	b := launchBrowser(t)

	ctx, cancel := chromedp.NewContext(b.browserCtx)
	defer cancel()
	chromedp.ListenTarget(ctx, func(ev any) {
		t.Logf("event: %T", ev)
	})
	rc, rc2 := context.WithTimeout(ctx, 30*time.Second)
	defer rc2()
	if err := chromedp.Run(rc, runtime.Enable(), chromedp.Navigate(srv.browserURL("/?controller=editor"))); err != nil {
		t.Fatal("nav:", err)
	}
	t.Log("navigated")
	var v bool
	ec, ec2 := context.WithTimeout(ctx, 10*time.Second)
	defer ec2()
	err := chromedp.Run(ec, chromedp.Evaluate(`typeof Vue !== 'undefined'`, &v))
	t.Log("eval:", v, err)
}
