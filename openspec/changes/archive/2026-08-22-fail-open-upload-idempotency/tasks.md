## 1. Handler control flow (`cmd/api/video.go`, implementation PR)

- [x] 1.1 Introduce a local `hasReservation bool` in `handleVideoUpload`, set to `true` only when `Reserve` returns `reserved == true, err == nil`.
- [x] 1.2 On `Reserve` returning a non-nil `err`: log it (matching the existing `log.Printf("idempotency reserve for upload %s: %v", ...)` call site), leave `hasReservation` false, and fall through to `CreateVideoJob` instead of responding `500` and returning. Do not run `cleanupRedundantUpload` in this branch — the upload is proceeding, not being discarded.
- [x] 1.3 Confirm the `reserved == false, err == nil` branch (genuine conflict — `waitForFinalizedIdempotencyKey`/`409`) is reached only when `err == nil`, unchanged from today.
- [x] 1.4 Guard all three existing `m.idempotency.Clear(...)` call sites and the one `m.idempotency.Finalize(...)` call site with `if hasReservation { ... }`, so none of them run against an empty/invalid token when the request proceeded without a reservation.
- [x] 1.5 Confirm `token`'s zero value (`""`) is never passed to `Finalize`/`Clear` after this change — `hasReservation` is the sole guard, not an inferred check on `token`.

## 2. Tests (implementation PR)

Each test below must include a discriminating assertion that proves the request actually proceeded past the `Reserve` error into the guarded call site being tested (e.g. an extractor invocation count, or a distinguishable response), not just that `Finalize`/`Clear` weren't called — a call-count assertion alone is trivially true for the wrong reason if the request never got that far (this is exactly how the pre-fix code would still pass a naive version of these tests, since it also never calls `Finalize`/`Clear` — it just fails earlier, at the `Reserve` error itself). Verify each test empirically: temporarily revert the corresponding guard in 1.4, confirm the test fails, then restore it.

- [x] 2.1 Test: `Reserve` returning an error results in the upload still succeeding (equivalent response to a no-idempotency-layer upload, extractor invoked once), not a `500` referencing idempotency — using a fake/mock `domain.IdempotencyStore` that returns an error from `Reserve`.
- [x] 2.2 Test: in the success scenario above, the fake store's `Finalize` and `Clear` are never called (call-count assertion).
- [x] 2.3 Test: `Reserve` error + `CreateVideoJob` failure (inject a repository that fails `Create`) — `Clear` (the call site guarding the `CreateVideoJob`-failure path) is never called, and the response reflects the `CreateVideoJob` failure itself, not the old Reserve-error `500`.
- [x] 2.4 Test: `Reserve` error + extraction failure (a failing `FrameExtractor`) — the extractor is confirmed invoked (proving the request reached extraction, not an early return), and `Clear` (the call site guarding the extraction-failure path) is never called.
- [x] 2.5 Test: `Reserve` error + artifact-ownership-recording failure (the fourth guarded `Clear` call site, after a successful extraction) — force `recordArtifactOwner`'s own path-confinement check to fail deterministically (e.g. a `StorageKey` containing `..`, no filesystem-permission tricks needed), confirm the extractor was invoked (proving the request reached this far) and `Clear` is never called.
- [x] 2.7 Test: the existing genuine-conflict path (`reserved == false, err == nil`, no `reserveErr` injected) is unaffected — still resolves via lookup or `409`, unchanged from current behavior. This is a regression guard: it must keep passing exactly as today, proving this change didn't widen its "proceed anyway" behavior to the unrelated conflict path.
- [x] 2.8 Test: the existing successful-reservation path (`reserved == true, err == nil`) is unaffected — `Finalize`/`Clear` still get called with the real token at the same points as today (already covered by this file's existing idempotency tests — confirm, don't just assume, that they still pass unchanged).
- [x] 2.9 Confirm (don't assume) `go test ./... -v` passes, run via `docker compose run --build --rm app-test go test ./... -v` if `ffmpeg` isn't on `PATH` locally.

## 3. Manual verification (implementation PR)

- [x] 3.1 Stop the `redis` container (`docker compose stop redis`) with the rest of the stack running, then call `POST /upload` with a valid video — confirm it now succeeds (frames extracted, zip produced) instead of returning `500`. Restart `redis` afterward: an upload of that *same* content will typically create a *new* `VideoJob` and run `ffmpeg` again — the outage-time reservation attempt usually leaves no usable key behind, though design.md's second Risk notes it isn't guaranteed (a lost acknowledgment can leave a real, orphaned reservation instead). Either way this is expected, not a bug. A *further* identical upload after that one — which does establish a key — should dedupe normally.

## 4. Finalization (separate PR, per repo-workflow)

- [x] 4.1 Promote this change's `upload-idempotency` delta spec (a `MODIFIED Requirements` delta) into `openspec/specs/upload-idempotency/spec.md`, then archive the change folder.
- [x] 4.2 Update `CLAUDE.md`'s existing idempotency bullet under "Notable constraints / gotchas" to note that a `Reserve` error now fails open (proceeds without dedup protection for that request) rather than blocking the upload — a clause added to the existing bullet, not a rewrite.
- [x] 4.3 Update `docs/architecture.md`'s request-flow description of the idempotency path, which currently presents only the `reserved: true`/`reserved: false` cases and unconditional finalize/clear behavior — add the `Reserve`-error/fail-open case.
- [x] 4.4 Flip this change's `docs/roadmap.md` Change Backlog row to `archived`, with links to the archive folder and promoted spec, and update the Phase 4 Phase Summary row / "Current State" prose. The row itself is added by a separate, preceding planning PR (`docs/roadmap.md` edits are out of scope for the propose PR — see `docs/development.md`'s PR Separation Rule), which must merge before this finalization PR does.
