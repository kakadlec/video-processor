## ADDED Requirements

### Requirement: Redis Unavailability Degrades Idempotency Protection Instead Of Blocking Uploads

If `Reserve` returns an error (a Redis-layer failure — unreachable, timeout, or any other backend error) rather than a normal `reserved: false` conflict result, `handleVideoUpload` SHALL log the error and proceed to `CreateVideoJob` as if no reservation existed for this request, instead of rejecting the upload. A request that proceeds this way SHALL NOT hold a valid reservation token, and SHALL NOT attempt to `Finalize` or `Clear` an idempotency key at any later point in its own handling — those calls are guarded on having obtained a valid reservation in the first place. This behavior applies only to a genuine store error; it SHALL NOT change the existing behavior for a real reservation conflict (`reserved: false` with no error), which continues to resolve via the bounded-retry-then-lookup path or `409 Conflict`.

#### Scenario: A Reserve error does not block the upload

- **GIVEN** the idempotency store's `Reserve` call returns an error (e.g. Redis is unreachable)
- **WHEN** `handleVideoUpload` handles that error
- **THEN** it logs the error and proceeds to `CreateVideoJob`/`ProcessVideoJob` for this upload, returning the same success/failure response an upload with no idempotency layer at all would return, rather than a `500` referencing idempotency

#### Scenario: A request that proceeded without a reservation does not attempt to finalize or clear one

- **GIVEN** a request proceeded through `CreateVideoJob` after a `Reserve` error, holding no valid reservation token
- **WHEN** that request later reaches a point in `handleVideoUpload` that would otherwise call `Finalize` or `Clear`
- **THEN** it skips that call entirely, without attempting it against an empty or invalid token

#### Scenario: A genuine reservation conflict is unaffected

- **GIVEN** `Reserve` returns `reserved: false` with no error, because a concurrent identical upload already holds the key
- **WHEN** `handleVideoUpload` handles that result
- **THEN** it follows the existing bounded-retry-then-lookup behavior (returning the existing job's status, or `409 Conflict` if the retry window elapses) — unchanged by this requirement
