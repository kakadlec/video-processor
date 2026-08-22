## 1. Handler control flow (`cmd/api/video.go`, implementation PR)

- [ ] 1.1 Introduce a local `hasReservation bool` in `handleVideoUpload`, set to `true` only when `Reserve` returns `reserved == true, err == nil`.
- [ ] 1.2 On `Reserve` returning a non-nil `err`: log it (matching the existing `log.Printf("idempotency reserve for upload %s: %v", ...)` call site), leave `hasReservation` false, and fall through to `CreateVideoJob` instead of responding `500` and returning. Do not run `cleanupRedundantUpload` in this branch — the upload is proceeding, not being discarded.
- [ ] 1.3 Confirm the `reserved == false, err == nil` branch (genuine conflict — `waitForFinalizedIdempotencyKey`/`409`) is reached only when `err == nil`, unchanged from today.
- [ ] 1.4 Guard all three existing `m.idempotency.Clear(...)` call sites and the one `m.idempotency.Finalize(...)` call site with `if hasReservation { ... }`, so none of them run against an empty/invalid token when the request proceeded without a reservation.
- [ ] 1.5 Confirm `token`'s zero value (`""`) is never passed to `Finalize`/`Clear` after this change — `hasReservation` is the sole guard, not an inferred check on `token`.

## 2. Tests (implementation PR)

- [ ] 2.1 Test: `Reserve` returning an error results in the upload still succeeding (equivalent response to a no-idempotency-layer upload), not a `500` referencing idempotency — using a fake/mock `domain.IdempotencyStore` that returns an error from `Reserve`.
- [ ] 2.2 Test: in that same scenario, the fake store's `Finalize` and `Clear` are never called (call-count assertion), proving the guard in 1.4 actually skips them rather than calling them with an empty token.
- [ ] 2.3 Test: the existing genuine-conflict path (`reserved == false, err == nil`) is unaffected — still resolves via lookup or `409`, unchanged from current behavior.
- [ ] 2.4 Test: the existing successful-reservation path (`reserved == true, err == nil`) is unaffected — `Finalize`/`Clear` still get called with the real token at the same points as today.
- [ ] 2.5 Confirm (don't assume) `go test ./... -v` passes, run via `docker compose run --build --rm app-test go test ./... -v` if `ffmpeg` isn't on `PATH` locally.

## 3. Manual verification (implementation PR)

- [ ] 3.1 Stop the `redis` container (`docker compose stop redis`) with the rest of the stack running, then call `POST /upload` with a valid video — confirm it now succeeds (frames extracted, zip produced) instead of returning `500`. Restart `redis` afterward and confirm a subsequent identical upload is deduplicated normally again (proves the fail-open path doesn't leave the store in a broken state).

## 4. Finalization (separate PR, per repo-workflow)

- [ ] 4.1 Promote this change's `upload-idempotency` delta spec into `openspec/specs/upload-idempotency/spec.md`, then archive the change folder.
- [ ] 4.2 Update `CLAUDE.md`'s existing idempotency bullet under "Notable constraints / gotchas" to note that a `Reserve` error now fails open (proceeds without dedup protection for that request) rather than blocking the upload — a clause added to the existing bullet, not a rewrite.
- [ ] 4.3 Update `docs/architecture.md` if its Request pipeline / idempotency description makes a claim this change invalidates (confirm rather than assume).
- [ ] 4.4 Confirm whether this change needs a `docs/roadmap.md` Change Backlog row — it's a bug fix to already-shipped Phase 4 behavior, not a new capability, so it likely does not need one; confirm against `docs/roadmap.md`'s own scoping rule rather than assuming either way.
