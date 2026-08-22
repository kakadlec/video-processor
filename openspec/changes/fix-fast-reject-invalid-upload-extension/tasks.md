## 1. Fix (`cmd/api`)

- [ ] 1.1 Add a small helper (e.g. `videoFilePart(req *http.Request) (*multipart.Part, string, error)`) in `cmd/api/video.go` that calls `req.MultipartReader()`, loops `NextPart()` looking for a part with `FormName() == "video"` and non-empty `FileName()`, draining and closing every non-matching part along the way, and returns `io.EOF`-as-not-found the same way `FormFile`'s "no such file" error is surfaced today.
- [ ] 1.2 Rewire `handleVideoUpload` to call this helper instead of `c.Request.FormFile("video")`, validating `isValidVideoFile` on the returned filename **before** any read of the part's body — the fix itself. Downstream code (`io.TeeReader(part, hasher)` → `io.Copy`) takes the returned `*multipart.Part` in place of the old `multipart.File`.
- [ ] 1.3 Confirm every existing response message/status for this handler is byte-identical to before (missing field, bad extension, valid upload) — no observable contract change.

## 2. Tests

- [ ] 2.1 New test proving the fix empirically: a multipart request with an invalid extension and a large body asserts that only a small amount of the body was actually read off the wire (not just that the response is `400`) — a test checking status code alone would pass identically before and after this fix. Revert the fix locally and confirm this new test fails, then restore the fix.
- [ ] 2.2 Confirm existing tests (`TestUpload_MissingFileField_Rejected`, `TestHandleVideoUpload_*`, and any other `cmd/api` upload test) still pass unchanged.
- [ ] 2.3 `go vet ./...` passes; `go test ./... -v` passes (via `docker compose run --build --rm app-test go test ./... -v` if `ffmpeg` isn't on `PATH`).

## 3. Finalization (separate PR, per repo-workflow)

- [ ] 3.1 Archive this change (`openspec/changes/fix-fast-reject-invalid-upload-extension` → `openspec/changes/archive/`) — no delta specs to promote (Modified Capabilities is empty).
- [ ] 3.2 Update `CLAUDE.md`'s existing `isValidVideoFile` gotcha bullet if it makes a claim this change invalidates (confirm rather than assume — it currently only notes extension-only validation, which is unchanged).
- [ ] 3.3 No `docs/roadmap.md` Change Backlog row — this is a bug fix, not a roadmap-scoped feature; skip that step.
