## MODIFIED Requirements

### Requirement: Update Persists a VideoJob's Transitioned State

`Repository.Update` SHALL persist an already-loaded `VideoJob`'s current `status`, `frame_count`, `error_reason`, and `storage_key` to its existing `video_jobs` row, identified by its unchanging `id`, **by the fence epoch its caller claimed with, and by the row still being `processing`**. **It SHALL write a `video_job_outbox` row describing the terminal outcome, in the same transaction, and only when the conditional statement affected a row.**

That inclusion is Phase 7's deliberate decision, replacing this requirement's previous exclusion. The exclusion existed so that the shape of `VideoJobCompleted`/`VideoJobFailed` would not be settled as a by-product of a general-purpose write before the Notification context had a say; it is now settled on its own terms by `videojob-terminal-events`, which owns the payloads, the event types, and the emission rule. The other half of the original objection — that `Update` is a general-purpose write — SHALL be read as no longer holding in fact: `Update`'s only callers are `CompleteJob` and `FailJob`, and its statement hardcodes `status = 'processing'` as the precondition, so it can persist nothing but a terminal transition. A separate outbox-writing sibling next to `Update` SHALL NOT be introduced, because it would leave `Update` with no callers; the contrast with `Enqueue` is that `Enqueue` has a distinct precondition and a distinct caller, and such a sibling would have neither.

**The event write SHALL be gated by the conditional statement's own row count, not by a separately evaluated predicate.** Affecting no row SHALL write no event, on both of the paths where that happens — the fenced refusal and the already-recorded identical outcome — so that the actor who wins an outcome and the actor who announces it can never diverge.

`Update` SHALL be **fenced**: its predicate SHALL include the caller-supplied epoch, and affecting no row SHALL be classified rather than collapsed — see the three readings below. It SHALL report to its caller whether the write was **applied**, not only whether it errored: an error-only signature cannot express the case where the row already carries exactly this caller's outcome, and `videojob-lease-recovery`'s single-cleanup guarantee depends on that distinction. **The same distinction now also gates event emission.** Any existing row other than the exact same terminal outcome at the same epoch SHALL be reported with a distinct exported sentinel — the row exists, but the caller no longer owns a matching `processing` job. This is a deliberate reversal of this requirement's previous "`Update` SHALL remain unconditional" clause, and the reason it reversed is that recovery now exists: a worker presumed dead can return mid-run, and the only thing that can stop it committing over its successor is a predicate in the same statement as the write.

**The predicate SHALL also require the stored status to be `processing`**, and that conjunct SHALL NOT be dropped as redundant with the epoch. It is what makes a terminal write *exclusive* rather than merely *ordered*: the epoch advances only on a requeue, so two actors can legitimately hold the same epoch for one job — a live but leaseless worker and the sweeper that decided to abandon it both act at the epoch they observed. Both would pass an epoch-only predicate, both would commit, and the second would overwrite the first. With the status conjunct the first write leaves the row terminal, the second matches no row, and exactly one actor may then perform the cleanup that follows a terminal state **and record the one event announcing it**. An argument that the aggregate's own transition check prevents this SHALL NOT be substituted: both actors evaluate that check against copies loaded before either write.

This is safe precisely because `Update` performs only `processing → completed` and `processing → failed`. `Enqueue` owns `pending → queued`, `ClaimForProcessing` owns `queued → processing`, and the requeue method above owns `processing → queued`; no caller of `Update` writes from any other status. **A job whose in-memory status is neither `completed` nor `failed` SHALL be refused with an error before any statement runs, rather than written with no corresponding event type.**

The fence SHALL NOT be confused with the claim. `ClaimForProcessing` decides who *starts* a job; `Update` decides who may *finish* one. `StartProcessing` SHALL NOT be routed through `Update`.

The ways `Update` can affect no row SHALL be distinguished through an authoritative follow-up lookup, like the classification after a lost claim. The complete classification is:

- **No row with the ID** returns `ErrVideoJobNotFound`.
- **A matching epoch on a terminal row carrying exactly the outcome being written** is idempotent success with `Applied=false`, not a fence. The caller then follows its outcome-specific contract: a completion retry may finish its own source/lease cleanup after a possibly lost response, while an already-present failure is acknowledged without cleanup. **No second event is written.**
- **Every other existing row** returns `ErrJobFenced`. This includes a strictly greater epoch (takeover), a matching epoch with a different terminal outcome (another actor at that epoch finished first), a lower epoch, and any non-`processing` status that is not the identical outcome. The caller rejects, keeps the source object, clears no idempotency key, and performs no cleanup. **No event is written.** The application and current worker log need not distinguish those predicates because none grants cleanup rights.

#### Scenario: Update persists a transitioned job

- **GIVEN** a `VideoJob` was previously persisted via `Create` and has since had a transition method applied to it in memory
- **WHEN** `Repository.Update` is called with that job and the epoch its row carries
- **THEN** a subsequent `Repository.FindByID` for its ID returns a job matching the updated `status`, `frame_count`, `error_reason`, and `storage_key`

#### Scenario: Update refuses a write carrying a superseded epoch

- **GIVEN** a persisted `VideoJob` whose `lease_epoch` has advanced since a caller read it
- **WHEN** `Repository.Update` is called with that caller's epoch
- **THEN** it returns the fence sentinel and every column of the row is unchanged

#### Scenario: Two actors at the same epoch cannot both commit a terminal state

- **GIVEN** a persisted `processing` `VideoJob` and two actors that both observed it at the same epoch — a leaseless worker still running and a sweeper that has reached the abandonment bound
- **WHEN** both call `Repository.Update` with that epoch, one writing `completed` and the other `failed`
- **THEN** exactly one affects a row, the other is refused, and the persisted job carries only the winner's outcome

#### Scenario: Update writes the terminal outbox row in the same transaction

- **GIVEN** a persisted `processing` `VideoJob` and a caller holding its current epoch
- **WHEN** `Repository.Update` is called with a terminal outcome
- **THEN** the updated row and exactly one unpublished terminal outbox row naming that job are both visible after the call, and neither is visible before it

#### Scenario: A refused Update writes no outbox row

- **GIVEN** a persisted `VideoJob` whose stored row does not satisfy the caller's epoch-and-`processing` predicate
- **WHEN** `Repository.Update` is called with it
- **THEN** it reports a refusal or an unapplied write, and no new `video_job_outbox` row is committed as a result of that call
