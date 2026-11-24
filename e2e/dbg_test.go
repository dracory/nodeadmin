//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestDbg(t *testing.T) {
	b := launchBrowser(t)
	ctx, cancel := chromedp.NewContext(b.browserCtx)
	defer cancel()
	rc, rc2 := context.WithTimeout(ctx, 20*time.Second)
	defer rc2()
	if err := chromedp.Run(rc, chromedp.Navigate("about:blank")); err != nil {
		t.Fatal("nav:", err)
	}
	t.Log("navigated")
	var v bool
	ec, ec2 := context.WithTimeout(ctx, 10*time.Second)
	defer ec2()
	err := chromedp.Run(ec, chromedp.Evaluate(`1+1===2`, &v))
	t.Log("eval:", v, err)
}
