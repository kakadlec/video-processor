## 1. OpenSpec artifacts

- [x] 1.1 Create `openspec/changes/establish-ddd-architecture-foundation/.openspec.yaml` with `schema: spec-driven` and today's date.
- [x] 1.2 Write `proposal.md`: why (structural debt ahead of planned features), what changes (spec-only, no code), capabilities (new `ddd-architecture`), impact (no application files modified).
- [x] 1.3 Write `design.md`: context, goals/non-goals, bounded contexts (Identity, Video Processing, Notification), VideoJob aggregate (value objects, state machine, invariants), use cases per context, domain events and integration contracts, package topology, dependency rules, ADRs (1–7), risks, open questions, and evolution roadmap (phases 1–8).
- [x] 1.4 Write `specs/ddd-architecture/spec.md`: delta spec formalizing the DDD architecture requirements in OpenSpec requirement/scenario format, covering bounded context isolation, aggregate root invariants, dependency rules, and state machine transitions.
- [x] 1.5 Write this `tasks.md`.

## 2. Acceptance criteria verification

> These items verify this change is complete and correct. They do NOT require code changes — they verify the artifact content is internally consistent and matches the design.

- [ ] 2.1 All three bounded contexts (Identity, Video Processing, Notification) are named and have distinct, non-overlapping responsibilities documented in `design.md`.
- [ ] 2.2 `VideoJob` value objects table lists at least `VideoJobID`, `UserID`, `OriginalFilename`, `StorageKey`, `FrameCount`, `ErrorReason`, and `JobStatus`.
- [ ] 2.3 The `JobStatus` state machine in `design.md` includes exactly these states: `pending`, `queued`, `processing`, `completed`, `failed`, with arrows showing valid transitions only.
- [ ] 2.4 `design.md` lists at least five domain events (`VideoJobCreated`, `VideoJobQueued`, `VideoJobStarted`, `VideoJobCompleted`, `VideoJobFailed`), each with their JSON field signatures.
- [ ] 2.5 Dependency rules in `design.md` explicitly state that `domain` must not import `application` or `infrastructure`, and that no bounded context may import another context's `domain` package.
- [ ] 2.6 ADR-1 explicitly states that RabbitMQ is the async transport and that Redis does NOT substitute for it.
- [ ] 2.7 ADR-4 explicitly lists Redis's four responsibilities: idempotency, rate limiting, status cache, and distributed locks — and states that PostgreSQL is the source of truth.
- [ ] 2.8 The roadmap table in `design.md` contains exactly eight phases (1–8) covering: DDD foundation, identity/auth, VideoJob persistence, Redis capabilities, MinIO, RabbitMQ + worker, notifications, observability.
- [ ] 2.9 `specs/ddd-architecture/spec.md` contains at least one formally stated Requirement and one Scenario per bounded context, plus requirements for dependency rules and aggregate invariants.
- [ ] 2.10 No file outside `openspec/changes/establish-ddd-architecture-foundation/` has been modified by this change.
- [ ] 2.11 `npx --yes @fission-ai/openspec validate establish-ddd-architecture-foundation` exits without error (or the flags `--strict --no-interactive` are not supported and the basic validate passes).
