# E2E browser tests

Driver: **go-rod** (`github.com/go-rod/rod`). Rationale: rod's launcher
downloads its own Chromium on first run — fully self-contained, no
system Chrome required (chromedp needs a Chrome binary present).

Run:

```bash
go test -v -tags e2e ./e2e/...
```

The suite is excluded from `go test ./...` by the `e2e` build tag.

Every test boots its own nodeadmin instance on an ephemeral
`httptest` listener over a temporary SQLite file — same Options
wiring a host app uses (store + engine + `nodeadmin.New` mounted
at "/"). No manual server, no shared data dir.

## Scenarios

- `TestSectionPagesRender` — every `?controller=` page (home,
  workflows, messages, editor, settings) renders the navbar
  (`#goflow-navbar`) and section quick bar (`.nav-pills`) with no
  console errors.
- `TestEditorCreateSavePersist` — editor loads, Add Node palette lists
  node types, create workflow → add node → save → persists across a
  reload (verified via API + explorer tree).
- `TestDeployRestartResume` — httpTrigger→httpResponse flow deployed,
  status `running`, listener serves; server rebuilt on the same DB →
  `ResumeDeployed` brings it back (deployed-flag regression).
- `TestSettingsMountsAndSaves` — settings Vue app mounts, maxSteps
  override saves and persists.

## Environment variables

- `NODEADMIN_E2E_BROWSER` — path to a browser binary; unset uses rod's
  auto-downloaded Chromium.
- `NODEADMIN_E2E_WINDOWS_TMP` — required when the binary is a Windows
  `.exe` under WSL: a WSL path under `/mnt/c` for the browser profile
  (e.g. `/mnt/c/Temp`).
- `NODEADMIN_E2E_HOST` — host the browser uses to reach the test server;
  defaults to `127.0.0.1` (correct under WSL mirrored networking).
  On NAT-mode WSL set it to the VM interface IP (`hostname -I`).

Example (WSL + Windows Edge, as verified):

```bash
NODEADMIN_E2E_BROWSER="/mnt/c/Program Files (x86)/Microsoft/Edge/Application/msedge.exe" \
NODEADMIN_E2E_WINDOWS_TMP=/mnt/c/Temp \
go test -v -tags e2e ./e2e/
```
