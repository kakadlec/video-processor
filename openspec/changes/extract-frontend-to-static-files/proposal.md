## Why

The entire frontend (HTML, CSS, and JavaScript) is currently a single Go string literal returned by `getHTMLForm()` in `main.go`. This makes the UI hard to edit (no syntax highlighting, no linting, string-escaping noise) and blocks `extract-cmd-api-entrypoint` (Phase 3), which depends on this change specifically so the composition-root move doesn't have to drag the inline HTML along. `openspec/specs/ddd-architecture/spec.md` already names the target files (`web/index.html`, `web/styles.css`, `web/app.js`) and a scenario ("Frontend extraction preserves GET / behavior") describing exactly this move — this change fulfills that already-specified target state.

## What Changes

- Move the HTML markup out of `getHTMLForm()` into `web/index.html`.
- Move the inline `<style>` block into `web/styles.css`.
- Move the inline `<script>` block into `web/app.js`.
- Embed `web/*` into the binary via `go:embed` and serve `GET /` from the embedded `index.html` instead of the Go string literal; `getHTMLForm()` is removed.
- No behavior change: `GET /` still returns HTTP 200 with the same rendered page, the page's fetch calls to `/upload` and `/api/status` are unchanged, and all existing user-facing pt-BR copy is carried over verbatim.

## Capabilities

### New Capabilities

(none — this is a delivery-layer refactor of already-specified behavior, not a new capability)

### Modified Capabilities

- `ddd-architecture`: the "Frontend as Presentation/Delivery Layer" requirement's description of the frontend as "currently embedded in `getHTMLForm()`" becomes stale once extraction lands; the delta spec updates that requirement to describe the frontend as living in `web/index.html`, `web/styles.css`, `web/app.js`, and marks the "Frontend extraction preserves GET / behavior" scenario as the now-fulfilled current state rather than a future one.

## Impact

- **Code**: `main.go` (`getHTMLForm()` removed, `GET /` handler updated, `go:embed` directive added); new `web/index.html`, `web/styles.css`, `web/app.js`.
- **Tests**: `main_test.go`'s coverage of `GET /` must still pass unchanged (same status code and content expectations).
- **Docs**: `openspec/specs/ddd-architecture/spec.md` (delta spec, per above); `CLAUDE.md`'s architecture section, which currently says "editing the UI means editing the Go string literal" — updated at finalization, not here.
- **Dependencies**: none new (Go's standard-library `embed` package).
- **No API/behavior change**: routes, request/response contracts, and rendered output are unaffected.
