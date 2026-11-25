//go:build e2e

// Package e2e contains browser-level end-to-end tests for nodeadmin,
// driven with chromedp (a Chrome/Edge/Chromium binary must be present —
// chromedp does not auto-download one; see README.md). Each test boots
// its own nodeadmin instance on an httptest server backed by a
// temporary SQLite file, mirroring the wiring in a host app mount.
// Run with:
//
//	go test -tags e2e ./e2e/...
package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/dracory/entitystore"
	"github.com/dracory/nodeadmin"
	"github.com/dracory/noderun"
	"github.com/dracory/nodestore"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// testServer is a running nodeadmin instance over its own sqlite DB,
// wired exactly like a host app mount (nodeadmin mounted at "/").
// The listener binds 0.0.0.0 so a Windows browser under WSL can reach
// it through the WSL interface IP (browserURL); Go-side API calls use
// localURL on 127.0.0.1.
type testServer struct {
	t   *testing.T
	srv *httptest.Server
	db  *sql.DB
}

// localURL is the base for in-process HTTP calls.
func (s *testServer) localURL() string { return s.srv.URL }

// browserURL is the base for browser navigation. For a Windows
// browser the host is 127.0.0.1 (WSL mirrored networking makes
// localhost bidirectional); NODEADMIN_E2E_HOST overrides it for
// non-mirrored WSL (set it to the VM interface IP).
func (s *testServer) browserURL(path string) string {
	if isWindowsBrowser() {
		host := os.Getenv("NODEADMIN_E2E_HOST")
		if host == "" {
			host = "127.0.0.1"
		}
		return "http://" + host + ":" + portOf(s.srv.URL) + path
	}
	return s.srv.URL + path
}

func (s *testServer) url(path string) string { return s.srv.URL + path }

func isWindowsBrowser() bool {
	bin := os.Getenv("NODEADMIN_E2E_BROWSER")
	return bin != "" && strings.HasSuffix(strings.ToLower(bin), ".exe")
}

func portOf(rawurl string) string {
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(rawurl, "http://"))
	return port
}

func newTestServer(t *testing.T, dbPath string) *testServer {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	// One connection serializes writers: the engine's durable inbox
	// store and the entity store share this handle, and modernc sqlite
	// has no busy timeout — a second pooled connection makes
	// deploy+flag-update races fail with SQLITE_BUSY ("database
	// operation failed").
	db.SetMaxOpenConns(1)

	entityStore, err := entitystore.NewStore(entitystore.NewStoreOptions{
		DB:                 db,
		EntityTableName:    "node_entity",
		AttributeTableName: "node_attribute",
		AutomigrateEnabled: true,
	})
	require.NoError(t, err)

	nodeStore, err := nodestore.NewStore(nodestore.NewStoreOptions{EntityStore: entityStore})
	require.NoError(t, err)

	engine := noderunner.NewWorkflowEngine()
	nodeAdmin, err := nodeadmin.New(nodeadmin.Options{
		Store:        nodeStore,
		Engine:       engine,
		NodeAdminURL: "/",
	})
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.Handle("/", nodeAdmin)
	srv := httptest.NewUnstartedServer(mux)
	// Bind all interfaces: a Windows browser reaches WSL services via
	// the VM interface IP, not 127.0.0.1.
	ln, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	srv.Listener = ln
	srv.Start()

	return &testServer{t: t, srv: srv, db: db}
}

// close simulates a process stop: HTTP listener down, DB handle closed.
// A second newTestServer over the same dbPath is a restart.
func (s *testServer) close() {
	s.srv.Close()
	require.NoError(s.t, s.db.Close())
}

// apiPost calls a nodeadmin JSON endpoint and unwraps the
// {status, data} envelope (guapi shape).
func apiPost(t *testing.T, base, path string, body any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)

	resp, err := http.Post(base+path, "application/json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var envelope struct {
		Status  string         `json:"status"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(payload, &envelope), "raw body: %s", payload)
	require.Equal(t, "success", envelope.Status, "api %s failed: %s", path, envelope.Message)
	return envelope.Data
}

// browser wraps a chromedp browser driving ONE tab for the whole
// suite. A single CDP session keeps the message pipeline simple —
// multiplexing a second session over the same remote-debugging socket
// proved flaky on this setup (responses stopped being routed mid-run).
// allocatorCancel shuts everything down.
type browser struct {
	tabCtx          context.Context
	watcher         *consoleWatcher
	allocatorCancel context.CancelFunc
}

var (
	sharedBrowser     *browser
	sharedBrowserOnce sync.Once
	browserCmd        *exec.Cmd // Windows-binary child process, if any
	browserProfile    string    // its Windows profile dir, for cleanup
)

// TestMain closes the shared browser after the suite.
func TestMain(m *testing.M) {
	code := m.Run()
	if sharedBrowser != nil {
		sharedBrowser.allocatorCancel()
	}
	if browserProfile != "" {
		killByProfile(browserProfile)
	} else if browserCmd != nil {
		_ = browserCmd.Process.Kill()
	}
	os.Exit(code)
}

// newBrowser returns the suite's shared browser, launching it on
// first use. One browser per run keeps Windows-binary spawns (WSL)
// cheap and avoids port/profile races between tests.
//
// chromedp has no auto-download: NODEADMIN_E2E_BROWSER must name a
// browser binary, or one is located on PATH (see findBrowser). With
// neither, the test is skipped. On WSL a Windows browser
// ("...msedge.exe" via /mnt/c) also works: it is spawned on a random
// debug port with a Windows-side profile dir and chromedp connects via
// the remote allocator.
func newBrowser(t *testing.T) *browser {
	sharedBrowserOnce.Do(func() { sharedBrowser = launchBrowser(t) })
	return sharedBrowser
}

// launchBrowser performs the actual browser spawn described on
// newBrowser.
func launchBrowser(t *testing.T) *browser {
	t.Helper()
	bin := os.Getenv("NODEADMIN_E2E_BROWSER")
	if bin != "" && strings.HasSuffix(strings.ToLower(bin), ".exe") {
		return newWindowsBrowser(t, bin)
	}
	if bin == "" {
		bin = findBrowser()
	}
	if bin == "" {
		t.Skip("no Chrome/Edge/Chromium found on PATH; set NODEADMIN_E2E_BROWSER")
	}
	return newLocalBrowser(t, bin)
}

// findBrowser looks for a usable Chrome-family binary on PATH (Linux
// names plus common /mnt/c Windows locations for WSL without
// NODEADMIN_E2E_BROWSER set).
func findBrowser() string {
	for _, name := range []string{
		"google-chrome", "google-chrome-stable", "chromium",
		"chromium-browser", "microsoft-edge", "msedge",
	} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	for _, p := range []string{
		"/mnt/c/Program Files/Google/Chrome/Application/chrome.exe",
		"/mnt/c/Program Files (x86)/Google/Chrome/Application/chrome.exe",
		"/mnt/c/Program Files (x86)/Microsoft/Edge/Application/msedge.exe",
		"/mnt/c/Program Files/Microsoft/Edge/Application/msedge.exe",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// newLocalBrowser spawns a Linux/native browser via chromedp's exec
// allocator (headless, temp profile managed by chromedp).
func newLocalBrowser(t *testing.T, bin string) *browser {
	t.Helper()
	// Skip DefaultExecAllocatorOptions[0] (the default ExecPath);
	// append ours last so they win over the defaults.
	opts := append(append([]chromedp.ExecAllocatorOption{},
		chromedp.DefaultExecAllocatorOptions[1:]...),
		chromedp.ExecPath(bin),
		chromedp.Flag("headless", "new"),
	)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	tabCtx, _ := chromedp.NewContext(allocCtx)
	// The first Run on the context spawns the process and attaches the
	// suite's single tab.
	require.NoError(t, chromedp.Run(tabCtx, runtime.Enable()))
	return &browser{tabCtx: tabCtx, watcher: watchConsole(tabCtx), allocatorCancel: cancelAlloc}
}

// newWindowsBrowser spawns a Windows Chromium-family browser (Edge or
// Chrome) — invoked from WSL via interop or natively on Windows — and
// returns a connected browser. NODEADMIN_E2E_WINDOWS_TMP must name
// the profile parent dir (under /mnt/c on WSL: UNC profile paths are
// ignored by the browser and it would defer to the user's running
// session); empty falls back to os.TempDir.
func newWindowsBrowser(t *testing.T, bin string) *browser {
	t.Helper()

	wslTmp := os.Getenv("NODEADMIN_E2E_WINDOWS_TMP")
	if wslTmp == "" {
		wslTmp = os.TempDir()
	}
	// Pick a random high port. Reserving via net.Listen doesn't work
	// across the WSL/Windows boundary — the released socket lingers
	// bound on the Windows side ("Only one usage of each socket
	// address") — so instead retry a few random ports and keep the
	// first whose devtools endpoint answers.
	var wsURL string
	for attempt := 0; attempt < 5 && wsURL == ""; attempt++ {
		// Fresh profile per attempt — a still-shutting-down previous
		// instance holds the profile lock and the new instance would
		// defer to it instead of opening devtools.
		dir, err := os.MkdirTemp(wslTmp, "e2e-")
		require.NoError(t, err)
		profile := windowsPath(t, dir)

		port := 20000 + rand.Intn(30000)
		cmd := exec.Command(bin,
			"--headless=new",
			"--disable-gpu",
			"--no-first-run",
			"--no-default-browser-check",
			"--remote-debugging-port="+strconv.Itoa(port),
			"--user-data-dir="+profile,
			"about:blank",
		)
		require.NoError(t, cmd.Start())

		versionURL := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) && wsURL == "" {
			if resp, err := http.Get(versionURL); err == nil {
				var v struct {
					WebSocket string `json:"webSocketDebuggerUrl"`
				}
				if json.NewDecoder(resp.Body).Decode(&v) == nil {
					wsURL = v.WebSocket
				}
				resp.Body.Close()
			}
			if wsURL == "" {
				time.Sleep(300 * time.Millisecond)
			}
		}
		if wsURL != "" {
			browserCmd = cmd
			browserProfile = profile
			t.Cleanup(func() { os.RemoveAll(dir) })
			break
		}
		killByProfile(profile)
		os.RemoveAll(dir)
	}
	require.NotEmpty(t, wsURL, "browser devtools endpoint never came up")

	allocCtx, cancelAlloc := chromedp.NewRemoteAllocator(context.Background(), wsURL)
	tabCtx, _ := chromedp.NewContext(allocCtx)
	// Attaches to the browser's first tab (the about:blank the binary
	// was spawned with).
	require.NoError(t, chromedp.Run(tabCtx, runtime.Enable()))
	return &browser{tabCtx: tabCtx, watcher: watchConsole(tabCtx), allocatorCancel: cancelAlloc}
}

// windowsPath converts dir to a Windows path the browser binary
// understands: wslpath on Linux/WSL, identity on Windows.
func windowsPath(t *testing.T, dir string) string {
	t.Helper()
	if goruntime.GOOS == "windows" {
		return dir
	}
	out, err := exec.Command("wslpath", "-w", dir).Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

// killByProfile kills every Windows browser process whose command
// line carries the given profile dir — WSL interop Process.Kill only
// kills the wrapper, leaving Edge orphaned and still holding the
// debug port, so the suite matches on the unique profile path.
func killByProfile(profile string) {
	ps := `Get-CimInstance Win32_Process |` +
		` Where-Object { $_.CommandLine -like '*` + profile + `*' } |` +
		` ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }`
	powershell := "powershell.exe"
	if goruntime.GOOS != "windows" {
		powershell = "/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe"
	}
	_ = exec.Command(powershell, "-NoProfile", "-Command", ps).Run()
}

// consoleWatcher collects console.error() calls and uncaught page
// exceptions for the life of the page. Call errors() after the page
// settles to assert a clean console.
type consoleWatcher struct {
	mu   sync.Mutex
	errs []string
}

func (w *consoleWatcher) errors() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, len(w.errs))
	copy(out, w.errs)
	return out
}

// reset clears collected errors — called per page so each test sees
// only its own console output on the shared tab.
func (w *consoleWatcher) reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.errs = nil
}

// watchConsole registers a chromedp target listener on ctx. It must be
// called before the first chromedp.Run on ctx so no early console
// events are missed.
func watchConsole(ctx context.Context) *consoleWatcher {
	w := &consoleWatcher{}
	chromedp.ListenTarget(ctx, func(ev any) {
		switch e := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			if e.Type != runtime.APITypeError {
				return
			}
			w.mu.Lock()
			defer w.mu.Unlock()
			for _, a := range e.Args {
				w.errs = append(w.errs, a.Description)
			}
		case *runtime.EventExceptionThrown:
			w.mu.Lock()
			defer w.mu.Unlock()
			txt := e.ExceptionDetails.Text
			if e.ExceptionDetails.Exception != nil {
				txt += " " + e.ExceptionDetails.Exception.Description
			}
			w.errs = append(w.errs, "uncaught: "+txt)
		}
	})
	return w
}

// js quotes s for embedding in a JS single-line expression.
func js(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

// eval evaluates a JS expression in the page and returns its value.
// The ctx is wrapped in a 45s bound: Runtime.evaluate replies only
// once the renderer's main thread is free, which — while CDN scripts
// are still parsing — can take tens of seconds on this setup; without
// a bound a reply that never comes wedges the test forever.
func eval(ctx context.Context, expr string, out any) error {
	ec, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return chromedp.Run(ec, chromedp.Evaluate(expr, out))
}

func evalBool(t *testing.T, ctx context.Context, expr string) bool {
	var v bool
	if err := eval(ctx, expr, &v); err != nil {
		t.Logf("evalBool error: %v", err)
		return false
	}
	return v
}

// waitElement waits for sel to be visible with a DOM dump on timeout —
// headless CI failures otherwise give no hint what the page looked
// like.
func waitElement(t *testing.T, ctx context.Context, sel string) {
	t.Helper()
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := chromedp.Run(cctx, chromedp.WaitVisible(sel, chromedp.ByQuery)); err != nil {
		var html string
		_ = eval(ctx, `document.documentElement.outerHTML`, &html)
		if len(html) > 4000 {
			html = html[:4000]
		}
		t.Fatalf("element %q not found: %v\npage html:\n%s", sel, err, html)
	}
}

// waitElementText waits until some element matching sel contains text
// (case-insensitive substring — innerText reflects text-transform).
// Exact-match variants use clickText.
func waitElementText(t *testing.T, ctx context.Context, sel, text string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	expr := `[...document.querySelectorAll(` + js(sel) + `)]` +
		`.some(e => (e.innerText||'').toLowerCase().includes(` + js(strings.ToLower(text)) + `))`
	for time.Now().Before(deadline) {
		if evalBool(t, ctx, expr) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no %q element containing text %q", sel, text)
}

// click waits for sel then clicks it via a fresh querySelector inside
// the page — Vue re-renders elements when reactive props flip (e.g.
// :disabled), so an element handle grabbed before the click can be
// detached by the time JS runs on it.
func click(t *testing.T, ctx context.Context, sel string) {
	t.Helper()
	waitElement(t, ctx, sel)
	require.NoError(t, eval(ctx,
		`{ const el = document.querySelector(`+js(sel)+`); el && el.click(); }`, nil))
}

// clickText waits for an element matching sel whose trimmed text
// equals text (case-insensitive — innerText reflects text-transform),
// then clicks it via JS (exact match avoids the
// "New workflow" / "New workflow group" prefix collision).
func clickText(t *testing.T, ctx context.Context, sel, text string) {
	t.Helper()
	expr := `[...document.querySelectorAll(` + js(sel) + `)]` +
		`.find(e => (e.innerText||'').trim().toLowerCase() === ` + js(strings.ToLower(text)) + `)`
	deadline := time.Now().Add(45 * time.Second)
	for {
		if evalBool(t, ctx, `!!(`+expr+`)`) {
			require.NoError(t, eval(ctx, `(`+expr+`)?.click()`, nil))
			return
		}
		if time.Now().After(deadline) {
			var seen []string
			_ = eval(ctx,
				`[...document.querySelectorAll(`+js(sel)+`)].map(e => e.innerText)`,
				&seen)
			t.Fatalf("no %q element with text %q (saw %v)", sel, text, seen)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// setInput sets an input's value and fires 'input'/'change' events so
// Vue's v-model picks it up (a bare .value assignment doesn't).
func setInput(t *testing.T, ctx context.Context, sel, value string) {
	t.Helper()
	waitElement(t, ctx, sel)
	require.NoError(t, eval(ctx, `{
		const el = document.querySelector(`+js(sel)+`);
		el.focus(); el.select(); el.value = `+js(value)+`;
		el.dispatchEvent(new Event('input', {bubbles: true}));
		el.dispatchEvent(new Event('change', {bubbles: true}));
	}`, nil))
}

// inputValue returns the .value property of sel.
func inputValue(t *testing.T, ctx context.Context, sel string) string {
	t.Helper()
	var v string
	require.NoError(t, eval(ctx,
		`document.querySelector(`+js(sel)+`).value`, &v))
	return v
}

// elementsLen returns the number of elements matching sel.
func elementsLen(t *testing.T, ctx context.Context, sel string) int {
	t.Helper()
	var n int
	require.NoError(t, eval(ctx,
		`document.querySelectorAll(`+js(sel)+`).length`, &n))
	return n
}

// mustPage navigates the suite's shared tab to url and resets the
// console watcher so each page gets a clean error list. The returned
// ctx is the shared tab context for all further actions.
func mustPage(t *testing.T, b *browser, url string) (context.Context, *consoleWatcher) {
	t.Helper()
	b.watcher.reset()
	runCtx, runCancel := context.WithTimeout(b.tabCtx, 120*time.Second)
	defer runCancel()
	require.NoError(t, chromedp.Run(runCtx, chromedp.Navigate(url)))
	return b.tabCtx, b.watcher
}

// waitVueApp waits until the CDN globals the Vue pages need are
// present and Vue has mounted #app. CDN fetches (jsDelivr bootstrap /
// Vue / drawflow) intermittently fail under headless — without this,
// clicks land on the unmounted mustache template and time out far
// downstream. Reloads up to 2 times on missing globals.
func waitVueApp(t *testing.T, ctx context.Context) {
	t.Helper()
	expr := `typeof Vue !== 'undefined' &&` +
		`typeof bootstrap !== 'undefined' &&` +
		`!!document.querySelector('#app[data-v-app]')`
	for attempt := 0; attempt < 3; attempt++ {
		ready := false
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			if evalBool(t, ctx, expr) {
				ready = true
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if ready {
			return
		}
		// Reload has no internal timeout — bound it so a stalled
		// load can't wedge the whole suite.
		rc, rcancel := context.WithTimeout(ctx, 60*time.Second)
		if err := chromedp.Run(rc, chromedp.Reload()); err != nil {
			t.Logf("waitVueApp reload: %v", err)
		}
		rcancel()
	}
	t.Fatal("Vue app never mounted (CDN scripts unavailable after reloads)")
}

// eventually polls cond until it returns true or the timeout lapses —
// the browser-side counterpart of an event wait (status transitions
// have no DOM signal to wait on).
func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out (%s) waiting for %s", timeout, what)
}

// workflowStatus returns the engine-side run status via the API.
func workflowStatus(t *testing.T, base, id string) string {
	t.Helper()
	data := apiPost(t, base, "/api/workflow-status", map[string]any{"id": id})
	s, _ := data["status"].(string)
	return s
}

// createWorkflow posts a workflow document and returns its ID.
func createWorkflow(t *testing.T, base, name string, nodes, edges []map[string]any) string {
	t.Helper()
	if edges == nil {
		edges = []map[string]any{}
	}
	data := apiPost(t, base, "/api/workflow-create", map[string]any{
		"name":  name,
		"nodes": nodes,
		"edges": edges,
	})
	wf, ok := data["workflow"].(map[string]any)
	require.True(t, ok, "workflow missing in create response: %v", data)
	id, _ := wf["id"].(string)
	require.NotEmpty(t, id)
	return id
}

func debugNode(id string) map[string]any {
	return map[string]any{
		"id":       id,
		"type":     "debug",
		"position": map[string]any{"x": 100, "y": 100},
		"data":     map[string]any{},
	}
}
