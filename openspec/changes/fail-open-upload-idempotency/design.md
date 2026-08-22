## Context

`cmd/api/video.go`'s `handleVideoUpload` calls `m.idempotency.Reserve(ctx, idemKey)` (`domain.IdempotencyStore.Reserve`, implemented by `internal/video/infrastructure/idempotency.RedisStore.Reserve`) before creating a `VideoJob`. `Reserve` returns `(token string, reserved bool, err error)`. Today, any non-nil `err` (Redis unreachable, timeout, or any other backend error — `RedisStore.Reserve` wraps whatever `SetNX` returns) makes the handler respond `500` immediately, without attempting `CreateVideoJob` at all. This was verified by manually stopping the `redis` container and calling `POST /upload`: the request fails even though PostgreSQL and `ffmpeg` are both healthy.

`token` returned by a successful `Reserve` is threaded through the rest of the handler and passed to three later `Clear` calls and one `Finalize` call, each of which does its own Redis round trip. All four of those calls are already non-fatal on error today (each just logs) — this change's job is to also skip them outright when there was never a valid reservation to finalize or clear, rather than let them run doomed Redis-CAS attempts against an empty token.

The other two Redis-backed features in this codebase — `internal/platform/ratelimit` and `internal/video/infrastructure/cache` — already fail open on any Redis error. This change brings idempotency's `Reserve`-error path in line with that established posture, closing the last place a Redis outage can take down a request this codebase doesn't need it to.

## Goals / Non-Goals

**Goals:**
- A `Reserve` error (Redis down/erroring) no longer blocks `POST /upload`; the request proceeds through `CreateVideoJob`/`ProcessVideoJob` exactly as if idempotency didn't exist for that one request.
- No wasted/misleading Redis calls: once a request is known to have no reservation (because `Reserve` itself errored), it must not later call `Finalize` or `Clear` with a meaningless empty token.
- Zero behavior change to the two paths that already work correctly today: `reserved == true` (normal path) and `reserved == false, err == nil` (genuine conflict with a concurrent identical upload — resolves via `waitForFinalizedIdempotencyKey` or `409`).

**Non-Goals:**
- Changing `domain.IdempotencyStore`'s interface or `RedisStore`'s implementation — `Reserve` keeps faithfully returning the Redis error; this change only changes how the caller reacts to it.
- Retrying `Reserve` before giving up — a single attempt, then fail open immediately, matching the rate limiter's "one bounded attempt, then open" posture rather than adding retry latency to every request during an outage.
- Any change to `Finalize`/`Clear`'s own already-non-fatal error handling when they *do* have a valid token and Redis errors on that specific call — only the "there was never a valid token" case (this change) is new.

## Decisions

### 1. Track reservation validity with a local flag, not by inspecting `token`'s zero value

Introduce a local `bool` (e.g. `hasReservation`) set to `true` only when `Reserve` returns `reserved == true, err == nil`. Every downstream call site that currently calls `m.idempotency.Finalize(...)` or `m.idempotency.Clear(...)` gets guarded by `if hasReservation { ... }`. This is more explicit and greppable than relying on `token == ""`, and matches this codebase's existing style of naming intent rather than inferring it from a zero value (e.g. `reserved bool` itself, in the interface this mirrors).

Alternative considered: let `Finalize`/`Clear` run anyway with an empty token, relying on their own compare-and-swap Lua scripts to safely no-op (an empty token can never match a real reservation's value). Rejected — technically safe, but wastes a Redis round trip on every single request made *during an outage that already just failed one Redis call*, and produces a confusing "clear idempotency key: cleared=false err=nil" log line that looks like a bug report rather than the expected consequence of a decision already made three call sites earlier.

### 2. On `Reserve` error: log, set `hasReservation = false`, and fall through — do not return early

Replace the current `if err != nil { ...; c.JSON(500, ...); return }` block with logging the error and continuing execution into the same code path `reserved == true` already takes (skip the `if !reserved` conflict-handling block entirely, since there is nothing to check a conflict against). Concretely: restructure so the `err != nil` and `reserved == true` cases converge on "proceed to `CreateVideoJob`" while `reserved == false, err == nil` remains the only path that goes through `waitForFinalizedIdempotencyKey`/`409`.

### 3. Scaling rationale (why fail open is the correct default here, not just an acceptable one)

Idempotency here is a pure cost-efficiency optimization — a missed dedup means at most one extra `VideoJob` and one extra `ffmpeg` run for byte-identical content, not a corrupted or duplicated externally-visible side effect (unlike payment-style idempotency keys, where a missed dedup means a double charge). Given that ceiling on the cost of getting it wrong, the failure posture should be judged by what it does to *availability*, not by trying to eliminate the (bounded, self-healing) cost of an occasional duplicate.

This system is being prepared to run multiple `cmd/api` replicas (Phase 5+, `docs/roadmap.md`), and eventually a `cmd/worker` (Phase 6). Under the old fail-closed behavior, a single Redis blip becomes a synchronized, fleet-wide outage of `POST /upload` — every replica's next reservation attempt fails at once, with no partial degradation. Under fail-open, the same blip costs at most a burst of duplicate `ffmpeg` runs proportional to concurrent identical uploads during the outage window, and self-heals the moment Redis recovers (the very next `Reserve` on that key succeeds normally). Redis must never be a single point of failure for the critical path (video processing) in a horizontally-scaled deployment — it is only allowed to degrade the optimizations it backs. This is the same principle already applied to rate limiting and the status cache; this change closes the one gap where it wasn't yet true.

## Risks / Trade-offs

- **[Risk]** While Redis is down, *all* upload deduplication is silently disabled — concurrent identical uploads that would normally be serialized via the reservation now all proceed independently and may run `ffmpeg` in parallel for the same content. → **Mitigation**: accepted; bounded to the outage window, costs compute/storage but not correctness, and is strictly better than the alternative (the whole upload path being down for everyone, including non-duplicate uploads, for the same window).
- **[Risk]** A request that proceeds without a reservation also can't be indexed for future idempotency lookups (no key was ever written) — a second identical upload arriving *after* Redis recovers but before this job completes will not find it and will also proceed independently, even though Redis is healthy again by then. → **Mitigation**: accepted as a narrow, self-resolving edge case (limited to the duration of one in-flight job that started during the outage); no different in kind from the existing "reservation crashed and never finalized" case already handled by the bounded-retry-then-409 path.
- **[Risk]** Logging every `Reserve` error means a sustained Redis outage produces a log line per upload request. → **Mitigation**: acceptable; this is the same log-and-proceed pattern already used for `Finalize`/`Clear` failures elsewhere in this handler, and a sustained Redis outage is exactly the situation operators need those log lines for.

## Migration Plan

No data migration. This is a pure code change to error-handling control flow in one handler; rollback is a plain revert. No new configuration, no new dependency, no schema change.
