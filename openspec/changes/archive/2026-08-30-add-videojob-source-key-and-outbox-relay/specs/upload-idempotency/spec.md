## MODIFIED Requirements

### Requirement: Reservation Is Finalized To The Real VideoJobID Only By Its Owning Token

Once `CreateVideoJob` **and** `EnqueueVideoJob` have both succeeded for a request that holds a reservation, `handleVideoUpload` SHALL overwrite the idempotency key's value with a finalized value that embeds both the created `VideoJobID` and the request's own ownership token, and extend its TTL to the full idempotency window (24 hours), but only if the key still holds that same request's reservation under that token — a stale request whose token no longer matches (because its reservation already expired and was reclaimed) SHALL NOT overwrite a newer reservation or finalized value. Embedding the token in the finalized value (not just the reservation) is what lets a later `Clear` call (see below) verify ownership after finalization too, not only while a reservation is still in flight.

The enqueue now sits between creation and finalization, and finalizing before it would be wrong in a way that stays invisible until it fires: a finalized key advertises its `VideoJobID` to every duplicate for 24 hours, so finalizing a job that then failed to reach `queued` would deduplicate later uploads of the same bytes onto a job stuck in `pending` that nothing will ever process. The reservation is therefore finalized only once the job is in a state something can act on.

#### Scenario: Key is finalized after the job is created and queued

- **GIVEN** a request holds a reservation under its own ownership token and both `CreateVideoJob` and `EnqueueVideoJob` have just succeeded
- **WHEN** the handler finalizes the key
- **THEN** the key's value becomes a finalized value embedding both the request's ownership token and the created `VideoJobID`, its TTL becomes the full 24-hour window, and `Lookup` against this key returns that `VideoJobID`

#### Scenario: A stale request cannot finalize over a newer reservation

- **GIVEN** a request's reservation has expired and a second, unrelated request has since reserved the same key under a different ownership token
- **WHEN** the first (stale) request belatedly attempts to finalize using its own, now-superseded token
- **THEN** the finalize attempt is rejected as a no-op, and the second request's reservation is left untouched

### Requirement: A Failure Before Finalize Clears The Reservation; Finalize Failure Is Non-Fatal

If `CreateVideoJob` **or** `EnqueueVideoJob` fails for a request holding a reservation, `handleVideoUpload` SHALL clear that reservation (using its own ownership token) so a subsequent request is not forced to wait out the sentinel's TTL. If both succeed but the subsequent finalize call itself fails, `handleVideoUpload` SHALL log the failure and proceed with processing the created job normally, rather than failing the request — the created `VideoJob` in PostgreSQL remains authoritative and valid even if this attempt is never indexed for future idempotency lookups.

The enqueue branch is not a special case bolted on; it is the rule the creation branch already followed, applied to the second step that can now fail before the job is dispatchable. Both leave the request unable to deliver a result, and a reservation held after either one would deduplicate a retry of the same bytes onto nothing for the full sentinel TTL. The job row `CreateVideoJob` already committed is left behind in `pending` in the enqueue case, deliberately: it is inert, it is the same state `POST /api/video-jobs` produces, and deleting it would need a compensating write on a path that has just demonstrated the database is not cooperating.

#### Scenario: CreateVideoJob failure clears the reservation

- **GIVEN** a request holds a reservation and `CreateVideoJob` returns an error
- **WHEN** the handler handles that error
- **THEN** it clears the reservation using its own ownership token before returning the error response

#### Scenario: EnqueueVideoJob failure clears the reservation and skips processing

- **GIVEN** a request holds a reservation, `CreateVideoJob` succeeded, and `EnqueueVideoJob` returns an error
- **WHEN** the handler handles that error
- **THEN** it clears the reservation using its own ownership token, never invokes `ProcessVideoJob`, and returns an error response — and the source object is still deleted by the handler's existing `defer`, so an immediate retry of the same content is neither deduplicated nor blocked

#### Scenario: Finalize failure does not fail the request

- **GIVEN** `CreateVideoJob` and `EnqueueVideoJob` have both succeeded but the subsequent call to finalize the idempotency key fails
- **WHEN** the handler handles that failure
- **THEN** it logs the failure and proceeds to `ProcessVideoJob` for the newly created job as if finalization had succeeded
