## Why

Phase 3 of the DDD migration needs a `VideoJob` aggregate as the foundation for persisting job state in PostgreSQL and eventually moving processing off the synchronous in-request `ffmpeg` pipeline. This change introduces only the domain and application layers — no infrastructure, no HTTP wiring — so it can be reviewed and merged as an independent, low-risk slice that unblocks `add-videojob-infrastructure` next, per `docs/roadmap.md`'s Phase 3 Change Backlog.

## What Changes

- New `internal/video/domain` package: `VideoJob` aggregate root with value objects `VideoJobID`, `OriginalFilename`, `StorageKey`, `FrameCount`, `ErrorReason`, and a `JobStatus` enum covering all five states (`pending`, `queued`, `processing`, `completed`, `failed`) plus a pure transition-validation function. No state-mutating methods beyond creation (`pending`) are added yet, since none of the three use cases in scope trigger a transition. All four transition use cases (`EnqueueVideoJob`, `StartProcessing`, `CompleteJob`, `FailJob`) stay out of scope here and are added together by the later `migrate-ffmpeg-execution-to-videojob-application` change, which calls all four synchronously in-process — per the canonical state machine, `completed`/`failed` are reachable only via `processing`, itself reachable only via `queued`, so a synchronous path cannot add just the last two. Phase 6 (`implement-rabbitmq-and-worker`) only replaces that in-process sequencing with a real message broker and a separate worker process; it introduces no new transition logic.
- New, context-local `UserID` value object in `internal/video/domain`, distinct from `internal/identity/domain`'s `UserID` — no shared package. Video Processing only ever consumes an already-verified ID string (never mints one), so this type needs a validating constructor but no ID generator.
- New `VideoJobRepository` port interface in `internal/video/domain` (`Create`, `FindByID`, `FindByUserID` with offset/limit pagination) — no concrete implementation; that is `add-videojob-infrastructure`'s scope.
- New `internal/video/application` package with exactly three use cases: `CreateVideoJob`, `GetJobStatus`, `ListUserJobs` — following the same port-based, infra-agnostic pattern as `internal/identity/application`.
- A `internal/video/dependency_rules_test.go` mirroring `internal/identity/dependency_rules_test.go`, enforcing that `domain`/`application` never import infrastructure, HTTP, SQL, or another context's packages.
- No changes to `main.go`, `main_test.go`, routes, or runtime behavior — the new packages are compiled and unit-tested in isolation but not yet reachable from any request path.

## Capabilities

### New Capabilities
- `videojob-lifecycle`: the `VideoJob` aggregate, its state machine and value objects, the `VideoJobRepository` port, and the three synchronous read/create use cases that make up Video Processing's persistence-ready domain model.

### Modified Capabilities
- `ddd-architecture`: the "No direct cross-context domain imports" scenario (under "Package Dependency Rules") currently directs cross-context `UserID` references to "the shared UserID value object from pkg/". This change corrects that to: each bounded context defines and owns its own local `UserID` value object, translated between contexts at the composition root — no shared kernel package. A shared `pkg/` type would couple Identity's and Video Processing's ID schemes together, contradicting the bounded-context autonomy the architecture is built around, and was never implemented (`pkg/` does not exist in the repository today). This also brings `ddd-architecture` in line with the already-shipped `video-processing-access` spec, which already requires Video Processing to consume only an opaque `UserID` "SHALL NOT import Identity domain or application packages."

## Impact

- **Affected code (implementation PR)**: new `internal/video/domain/*.go`, `internal/video/application/*.go`, `internal/video/dependency_rules_test.go`, and their `_test.go` files. No existing file changes outside `openspec/`.
- **Not affected**: `main.go`, `main_test.go`, HTTP routes, `go.mod` (no new dependencies expected — the identity package's patterns need only the standard library and the existing `github.com/google/uuid`, already a dependency).
- **Affected docs (finalization PR, after implementation merges)**: `docs/domain-model.md`'s "Cross-Context Contracts" section and `docs/architecture.md`'s "Target Package Topology" tree and Dependency Rule 5 both currently describe the `pkg/`-shared-kernel model this change replaces; both need correcting alongside the spec promotion.
- **Unblocks**: `add-videojob-infrastructure` (PostgreSQL adapter for `VideoJob`), the next row in `docs/roadmap.md`'s Phase 3 Change Backlog.
