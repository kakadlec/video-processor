## ADDED Requirements

### Requirement: GET /api/video-jobs/:id Is The Status Endpoint The Upload Response Names

`GET /api/video-jobs/:id` SHALL be the endpoint a `POST /upload` response directs its caller to for the submitted job's outcome, and its response SHALL be sufficient on its own to decide whether to keep polling, stop and download, or stop and report a failure.

This promotes an endpoint that until now only served a preview API into the asynchronous flow's status channel. Its contract does not change: it remains bearer-authenticated and owner-scoped, and returns the same representation for a job created through either entry point. What changes is that a client depends on it, so the statuses it can return SHALL be treated as a public contract — `pending`, `queued`, `processing`, `completed`, and `failed` — and a `failed` job's response SHALL carry the persisted failure reason so a poller can report it without a second call.

A caller SHALL NOT have to distinguish which entry point created a job in order to poll it.

#### Scenario: A submitted job is polled to completion through this endpoint

- **GIVEN** a video submitted through `POST /upload` by an authenticated user
- **WHEN** that user repeatedly requests the status URL the submission returned
- **THEN** each response is this endpoint's owner-scoped representation, the status advances through `queued` and `processing` to `completed`, and no other endpoint is needed to observe the outcome

#### Scenario: A failed job reports its reason to the poller

- **GIVEN** a submitted job whose processing failed
- **WHEN** its owner polls this endpoint
- **THEN** the response reports `failed` and carries the persisted failure reason

#### Scenario: Polling is owner-scoped like every other read here

- **GIVEN** a job submitted by one user
- **WHEN** a different authenticated user polls its status URL
- **THEN** the request is rejected exactly as this capability's existing owner-scoping requires

## MODIFIED Requirements

### Requirement: Jobs Created Through This API Have No Processing Trigger

A `VideoJob` created via `POST /api/video-jobs` SHALL remain in `pending` status indefinitely within this capability's scope — no code path reachable from these three endpoints calls `EnqueueVideoJob`, `StartProcessing`, `CompleteJob`, or `FailJob`. This capability SHALL NOT be documented or presented as a working end-to-end processing path until a later change explicitly wires a processing trigger.

**This survives the asynchronous cutover, and the distinction is now easy to get wrong.** `POST /upload` is the asynchronous submission endpoint: it stores bytes, creates a job, enqueues it, and returns a reference to *this* capability's read endpoints. `POST /api/video-jobs` accepts a filename in JSON, has no uploaded bytes, and therefore still has nothing to enqueue — a job it creates carries no source key and `videojob-lifecycle`'s enqueue precondition rejects it. The worker introduced by the cutover SHALL NOT be presented as this endpoint's processing trigger.

A `pending` status observed through `GET /api/video-jobs/:id` therefore SHALL NOT be interpreted as "waiting for a worker". A job awaiting a worker is `queued`.

#### Scenario: A created job's status never advances on its own

- **GIVEN** a `VideoJob` was created via `POST /api/video-jobs` and no other system component acts on it
- **WHEN** its status is queried via `GET /api/video-jobs/:id` at any later time
- **THEN** the status is still `pending`

#### Scenario: A worker never picks up a job created through this endpoint

- **GIVEN** a `VideoJob` created via `POST /api/video-jobs`, and a running worker consuming the job queue
- **WHEN** the worker has drained the queue
- **THEN** that job is still `pending`, no message naming it was ever published, and no extraction ran for it
