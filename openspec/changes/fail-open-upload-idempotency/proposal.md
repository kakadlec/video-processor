## Why

`POST /upload`'s idempotency reservation (`internal/video/infrastructure/idempotency`'s `Reserve`, called from `cmd/api/video.go`) treats any Redis error as fatal to the request, returning `500` ("Failed to check upload idempotency") and refusing to process the video at all. This is the only one of this codebase's three Redis-backed features that fails closed — rate limiting (`internal/platform/ratelimit`) and the status cache (`internal/video/infrastructure/cache`) both fail open, degrading gracefully to "no rate limit" / "read from PostgreSQL" rather than blocking the request. Confirmed by reproducing it manually: stopping the `redis` container and calling `POST /upload` returns `500` even though PostgreSQL and `ffmpeg` are both healthy and capable of processing the video correctly.

Idempotency here is a pure cost-efficiency optimization (skip re-running `ffmpeg` for byte-identical content from the same user) with no correctness or safety consequence if missed — unlike payment-style idempotency keys, the worst case of a missed dedup is a second `VideoJob` and a second `ffmpeg` run for the same content, not a corrupted or duplicated side effect. Given that, and given this system is being prepared to run multiple `cmd/api` replicas (Phase 5+ of `docs/roadmap.md`), a Redis outage should never be able to take down the entire fleet's upload path at once — it should only be allowed to degrade the optimization it backs.

## What Changes

- `cmd/api/video.go`'s `handleVideoUpload`: on a `Reserve` error (Redis unreachable/erroring — distinct from the existing, unchanged "key already exists" conflict path signaled via the `reserved bool` return with no error), log the error and proceed directly to `CreateVideoJob` as if no idempotency protection were available for this request, instead of returning `500`. `domain.IdempotencyStore.Reserve`'s contract and `RedisStore.Reserve`'s implementation are unchanged — they keep faithfully reporting the Redis error; only the caller's response to that error changes.
- The same handler's four downstream `Finalize`/`Clear` call sites (after `CreateVideoJob` failure, after `Finalize` itself, after an extraction failure, and after an artifact-ownership failure) are each guarded so a request that proceeded without a valid reservation never calls them with an empty/invalid token.
- No change to the `409 Conflict` path for a genuine reservation conflict between two concurrent identical uploads while Redis is healthy — that behavior is correct today and stays as-is.

## Capabilities

### New Capabilities
(none)

### Modified Capabilities
- `upload-idempotency`: modifies the existing "Concurrent Identical Requests Are Serialized Via Token-Owned Atomic Reservation" requirement to add an explicit exception for a `Reserve` error — the reservation is no longer unconditionally required before `CreateVideoJob`. This must be a `MODIFIED Requirements` delta (not an additional `ADDED` requirement), since an added requirement alongside the existing unconditional one would leave the canonical spec simultaneously requiring and forbidding the fail-open path.

## Impact

- **Changed code**: `cmd/api/video.go`'s `handleVideoUpload` — the `Reserve`-error branch (currently lines ~282-291), plus its four downstream `Finalize`/`Clear` call sites, each guarded on whether a valid reservation was actually obtained. `internal/video/infrastructure/idempotency/redis_store.go` and `internal/video/domain/idempotency_store.go` are unchanged.
- **No changes** to `Finalize`/`Clear`'s own error handling (already non-fatal, per existing spec) or to the conflict-detection path (`reserved == false` with no error).
- **Docs** (finalization PR only): `CLAUDE.md`'s existing idempotency bullet under "Notable constraints / gotchas" (currently silent on `Reserve`'s failure mode — needs a clause, not a rewrite); `docs/architecture.md`'s request-flow description of the idempotency path (currently presents only the `reserved: true`/`reserved: false` cases and unconditional finalize/clear behavior); `openspec/specs/upload-idempotency/spec.md`'s promoted version once the delta lands; `docs/roadmap.md`'s Change Backlog row for this change, flipped to `archived`.
- **Dependencies**: none new.
