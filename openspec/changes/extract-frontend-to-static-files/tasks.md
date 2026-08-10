## 1. Implementation (implementation PR)

- [ ] 1.1 Create `web/index.html` containing the HTML markup currently returned by `getHTMLForm()` (`main.go:481`), with `<link rel="stylesheet" href="/styles.css">` and `<script src="/app.js">` replacing the inline `<style>`/`<script>` blocks. Preserve all existing pt-BR copy verbatim.
- [ ] 1.2 Create `web/styles.css` containing the extracted contents of the inline `<style>` block, unchanged.
- [ ] 1.3 Create `web/app.js` containing the extracted contents of the inline `<script>` block (the `fetch` calls to `/upload` and `/api/status`), unchanged.
- [ ] 1.4 In `main.go`, add a `//go:embed web` directive and an `embed.FS` variable; update the `GET /` handler to serve `web/index.html`'s contents instead of calling `getHTMLForm()`; add routes serving `web/styles.css` at `/styles.css` and `web/app.js` at `/app.js` from the embedded FS.
- [ ] 1.5 Delete `getHTMLForm()` from `main.go`.
- [ ] 1.6 Diff the concatenated content of the three new `web/` files against the original `getHTMLForm()` string literal (e.g. via a throwaway script or manual review) to confirm no content was dropped or altered during extraction.

## 2. Verification

- [ ] 2.1 `go build -o app .` succeeds.
- [ ] 2.2 `go test ./... -v` passes (requires `ffmpeg` on `PATH`, or run via `docker compose run --build --rm app-test go test ./... -v`).
- [ ] 2.3 `go vet ./...` and `gosec ./...` report no new findings.
- [ ] 2.4 Manually load `GET /` in a browser, confirm the page renders identically to before (styling intact, no console errors), and complete one full upload → status → download flow through the UI.

## 3. Finalization (finalization PR, after implementation merges)

- [ ] 3.1 Mark all tasks above complete.
- [ ] 3.2 Update `CLAUDE.md`'s architecture section (currently: "There is no separate frontend build; editing the UI means editing the Go string literal") to describe the extracted `web/` files instead.
- [ ] 3.3 Promote `specs/ddd-architecture/spec.md`'s MODIFIED requirement into `openspec/specs/ddd-architecture/spec.md`.
- [ ] 3.4 Move the change folder to `openspec/changes/archive/`.
- [ ] 3.5 Update `docs/roadmap.md`'s `extract-frontend-to-static-files` Change Backlog row to `archived`, linking the archive folder and the promoted spec.
