## Context

`getHTMLForm()` (`main.go:481`, ~386 lines) returns one big backtick string literal containing a full HTML document with an inline `<style>` block and an inline `<script>` block (vanilla JS `fetch` calls to `/upload` and `/api/status`). `GET /` (`main.go:65`) calls `c.String(200, getHTMLForm())`. There is no other place in the codebase that touches this content. `openspec/specs/ddd-architecture/spec.md` already names the target layout (`web/index.html`, `web/styles.css`, `web/app.js`) and requires `GET /` to keep returning HTTP 200 with a page that "renders without JavaScript errors" post-extraction.

## Goals / Non-Goals

**Goals:**
- Move the HTML/CSS/JS out of the Go string literal into `web/index.html`, `web/styles.css`, `web/app.js`.
- Serve them from the compiled binary (no new runtime file-path dependency, no change to how the app is deployed/containerized).
- Preserve `GET /` behavior in substance: same status code, same visible rendering, same pt-BR copy, same JS behavior against `/upload` and `/api/status`. The HTML markup itself is *not* byte-for-byte identical — replacing the inline `<style>`/`<script>` elements with `<link rel="stylesheet">`/`<script src>` references necessarily changes the served HTML; that's an intentional, expected normalization, not a regression.

**Non-Goals:**
- No UI redesign, copy changes, or new frontend features.
- No change to `POST /upload`, `GET /download/:filename`, or `GET /api/status` contracts.
- No frontend build tooling (bundler, minifier, framework) — this stays static files served as-is.
- No change to how `extract-cmd-api-entrypoint` will later move the composition root; this change only removes the reason that move would need to drag inline HTML along.

## Decisions

- **`go:embed` over `os.ReadFile` at request time**: the binary already ships as a single deployable artifact (see `harden-dockerfile`'s multi-stage build with no separate asset-copy step); embedding keeps that property and avoids adding a new runtime dependency on `web/`'s presence relative to the working directory. Alternative considered: reading from disk on each request — rejected, since it would require the `runtime` Docker stage to also `COPY web/`, a container-image change out of scope for a "no behavior change" refactor.
- **One `web/` directory with three files, matching the spec's named paths exactly**: `openspec/specs/ddd-architecture/spec.md`'s "Frontend extraction preserves GET / behavior" scenario already names `web/index.html`, `web/styles.css`, `web/app.js` — using those exact names/paths means no delta needed to that scenario's wording, only to the requirement's now-stale "currently embedded in `getHTMLForm()`" parenthetical.
- **`index.html` references `styles.css`/`app.js` via relative `<link>`/`<script src>` tags, served by the same embedded FS at `/styles.css` and `/app.js`**: keeps the HTML document itself close to a normal static page rather than re-inlining CSS/JS back into it after extraction, and needs only two additional trivial static routes (no templating engine).
- **`getHTMLForm()` is deleted, not deprecated**: nothing else calls it; keeping it around as a dead function would be exactly the kind of unused code the project's conventions say to remove outright.

## Risks / Trade-offs

- [Copy-paste transcription errors moving ~386 lines of embedded HTML/CSS/JS into separate files could subtly change rendered output or break the JS] → compare the extracted `web/styles.css` and `web/app.js` bodies against the original inline `<style>`/`<script>` content verbatim, and review the remaining HTML against the original markup accounting for the intentional `<link>`/`<script src>` normalization (concatenating all three files and diffing against the original string literal would not work, since the HTML itself is expected to differ — see the Goals note above); back this with new `GET /`, `/styles.css`, `/app.js` integration tests (`main_test.go` has none today) plus a manual browser check of the upload flow before merging.
- [gosec or `go vet` flagging the new `embed.FS` usage or route wiring] → run the standard `go vet` / `gosec` gates locally before opening the PR, per `repo-workflow`'s definition-of-done.
- [Static files served under `/styles.css` and `/app.js` might collide with existing routes] → checked against `setupRouter()`'s existing route table (`/`, `/upload`, `/download/:filename`, `/api/status`, plus static mounts for `/uploads` and `/outputs`) — no collision.

## Migration Plan

Single implementation PR: add `web/` files, wire `go:embed` + routes, delete `getHTMLForm()`, run `go test ./... -v`. No data migration, no runtime state, no rollback concerns beyond a normal `git revert` — the change is a pure code/asset move with no persisted format.
