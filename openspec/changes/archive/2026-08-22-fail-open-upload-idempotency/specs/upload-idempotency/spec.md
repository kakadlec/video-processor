## MODIFIED Requirements

### Requirement: Concurrent Identical Requests Are Serialized Via Token-Owned Atomic Reservation

`handleVideoUpload` SHALL atomically reserve its idempotency key in Redis (a conditional set that fails if the key already exists) before calling `CreateVideoJob`, storing a short-lived sentinel value that embeds a unique ownership token generated for that reservation. A second request that fails this reservation SHALL NOT immediately return an error; it SHALL instead look up the key's current value in a short bounded retry loop, and only return `409 Conflict` (without creating a `VideoJob` or invoking `ffmpeg`) if that bound is exceeded without the key resolving to a real `VideoJobID`.

If the reservation attempt itself fails with a store-level error (e.g. Redis is unreachable or times out) rather than resolving to either a successful reservation or a genuine conflict, `handleVideoUpload` SHALL NOT treat this as a conflict or return an error to the caller. It SHALL log the error and proceed directly to `CreateVideoJob` as if no idempotency protection were available for this request. A request that proceeds this way holds no valid reservation token, and SHALL NOT attempt to finalize or clear an idempotency key at any later point in its own handling.

#### Scenario: First of two concurrent identical requests proceeds

- **GIVEN** two requests from the same user with identical content arrive concurrently, and neither has an existing idempotency key
- **WHEN** both attempt to reserve the idempotency key at nearly the same time
- **THEN** exactly one reservation succeeds, with its own unique ownership token, and that request proceeds to `CreateVideoJob`

#### Scenario: Second of two concurrent identical requests resolves once the first finalizes

- **GIVEN** the scenario above, where one request has already reserved the key with its own ownership token
- **WHEN** the second request's reservation attempt fails and the first request's reservation finalizes to a real `VideoJobID` before the second request's bounded retry window elapses
- **THEN** the second request returns that job's status (translated per the requirement below) without creating a `VideoJob` or invoking `ffmpeg`, and never receives `409`

#### Scenario: A reservation that never resolves times out as 409

- **GIVEN** a request has reserved the key but never finalizes or clears it (e.g. it crashed) before a second, near-simultaneous request's bounded retry window elapses
- **WHEN** the second request's bounded retry window elapses without the key resolving to a real `VideoJobID`
- **THEN** the second request returns `409 Conflict` without creating a `VideoJob` or invoking `ffmpeg`

#### Scenario: A Reserve error does not block the upload

- **GIVEN** the idempotency store's `Reserve` call returns a store-level error (e.g. Redis is unreachable) rather than a successful reservation or a genuine conflict
- **WHEN** `handleVideoUpload` handles that error
- **THEN** it logs the error and proceeds to `CreateVideoJob`/`ProcessVideoJob` for this upload, returning the same success/failure response an upload with no idempotency layer at all would return, rather than an error referencing idempotency

#### Scenario: A request that proceeded without a reservation does not attempt to finalize or clear one

- **GIVEN** a request proceeded through `CreateVideoJob` after a `Reserve` error, holding no valid reservation token
- **WHEN** that request later reaches a point in `handleVideoUpload` that would otherwise call `Finalize` or `Clear`
- **THEN** it skips that call entirely, without attempting it against an empty or invalid token
