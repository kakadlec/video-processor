## MODIFIED Requirements

### Requirement: A Source Object Is Deleted By Whichever Component Owns Its Job's Outcome

A stored source object SHALL be deleted by exactly one component, and which one it is SHALL be decided by whether the job was successfully enqueued.

**Before the enqueue commits, the object belongs to the request.** Every `POST /upload` request that stores a source object SHALL attempt to delete it before the request completes on every exit path that does not end in a queued job: a duplicate-content conflict, a `CreateVideoJob` error, an `EnqueueVideoJob` error, and any other early return after the object was written. The attempt SHALL be made from a single deferred call registered as soon as the object is stored, guarded on whether the enqueue succeeded, rather than from per-path cleanup calls — so no future early-return path can be added without it.

**Once the enqueue commits, the object belongs to the job's consumer.** The handler SHALL NOT delete it, and SHALL NOT delete it "just in case" on its way out: the bytes are the consumer's input, and a handler that removed them would destroy a running extraction. `videojob-worker` defines the consumer's obligation and the one condition under which it must also refrain.

The deletion SHALL be performed on a context detached from the request's own, because a canceled request context may itself be the reason the request is unwinding. Deleting a key that is already absent SHALL NOT be treated as an error.

This is an obligation to **attempt**, deliberately not a guarantee of absence. The attempt is one call with no retry and no persisted cleanup record, so a storage failure at that instant leaves the object behind with nothing in this system to reclaim it. A failure SHALL be logged with the leaked key — so the residual set is enumerable from logs rather than invisible — and SHALL NOT fail the request.

**A job that is enqueued but never dispatched leaks its source object permanently, and no requirement here claims otherwise.** The handler has given up ownership and no consumer ever took it, so nothing deletes the object: this happens when the relay never publishes the row, when the message is dead-lettered before a claim, or when a worker dies between delivery and its own cleanup. An expiration lifecycle rule on the `uploads/` key prefix is no longer a recommended backstop but the **only** guarantee that such objects are ever reclaimed, and `docs/operations.md` SHALL describe it in those terms.

#### Scenario: A queued upload does not delete its own source object

- **WHEN** a video is uploaded and the job is successfully created and enqueued
- **THEN** the response returns with the source object still present in the bucket, because the consumer that will process the job needs it

#### Scenario: A duplicate's source object is deleted without touching the original's

- **GIVEN** a duplicate request that stored its own source object under its own `uploadID` before discovering the conflict, and storage reachable throughout
- **WHEN** the handler cleans up
- **THEN** that request's own source object is deleted and the original job's artifacts are untouched

#### Scenario: A job that cannot be created or enqueued deletes its source object

- **GIVEN** a stored source object whose `CreateVideoJob` or `EnqueueVideoJob` call fails
- **WHEN** the handler unwinds
- **THEN** no object exists under that request's source key, because ownership never transferred

#### Scenario: A client disconnect before the enqueue still triggers cleanup

- **GIVEN** a request whose context is canceled after its source object was stored but before the job was enqueued
- **WHEN** the handler unwinds
- **THEN** the deletion is still attempted, because the cleanup does not run on the canceled request context

#### Scenario: A client disconnect after the enqueue does not

- **GIVEN** a request whose context is canceled after the job was successfully enqueued
- **WHEN** the handler unwinds
- **THEN** the source object is left in place, and the job is processed normally by its consumer

#### Scenario: A cleanup failure is logged, not fatal, and not specified away

- **GIVEN** a request whose source object was stored, whose job was not enqueued, and whose deletion fails
- **WHEN** the handler unwinds
- **THEN** the response is whatever the request's own outcome dictates — unchanged by the cleanup failure — and the failure is logged with the source key, leaving an object the application will not retry
