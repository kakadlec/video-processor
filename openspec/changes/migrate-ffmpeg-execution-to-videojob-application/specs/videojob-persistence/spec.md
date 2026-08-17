## ADDED Requirements

### Requirement: Update Persists a VideoJob's Transitioned State

`Repository.Update` SHALL persist an already-loaded `VideoJob`'s current `status`, `frame_count`, `error_reason`, and `storage_key` to its existing `video_jobs` row, identified by its unchanging `id`. Unlike `Create`, `Update` SHALL NOT write a `video_job_outbox` row — the outbox requirement above is scoped to job creation only.

#### Scenario: Update persists a transitioned job

- **GIVEN** a `VideoJob` was previously persisted via `Create` and has since had a transition method applied to it in memory
- **WHEN** `Repository.Update` is called with that job
- **THEN** a subsequent `Repository.FindByID` for its ID returns a job matching the updated `status`, `frame_count`, `error_reason`, and `storage_key`

#### Scenario: Update does not write an outbox row

- **GIVEN** a previously persisted `VideoJob`
- **WHEN** `Repository.Update` is called with it
- **THEN** no new `video_job_outbox` row is committed as a result of that call
