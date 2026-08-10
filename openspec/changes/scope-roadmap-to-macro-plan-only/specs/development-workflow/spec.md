## ADDED Requirements

### Requirement: Roadmap Change Backlog Is Scoped To Product/Architecture Work

`docs/roadmap.md`'s Change Backlog SHALL track only changes within the product/architecture scope of the 8-phase DDD roadmap. Changes whose own subject is this repository's development workflow or OpenSpec process itself (as opposed to product/architecture behavior) SHALL NOT receive a `docs/roadmap.md` Change Backlog row, at any stage — this is a categorical scope boundary, not a per-change exception requiring justification. For changes that are in scope, `docs/roadmap.md` SHALL be edited only at two points: when a row is added or re-scoped (a planning decision), and when a row is marked `archived` (a completion signal). No other status transition (including an intermediate "proposed" state) SHALL require a `docs/roadmap.md` edit; in-flight status for any change that has an `openspec/changes/<name>/` folder is available via `openspec list`.

#### Scenario: A workflow/process-only change is proposed without a roadmap row

- **WHEN** a change's own subject is this repository's development workflow, OpenSpec process, or agent tooling rather than product/architecture behavior
- **THEN** it is proposed and implemented via the normal OpenSpec flow without ever gaining a `docs/roadmap.md` Change Backlog row

#### Scenario: A product/architecture change is added to the backlog as a planning decision

- **WHEN** new product/architecture-scope work is decided and scoped
- **THEN** a row for it is added to `docs/roadmap.md`'s Change Backlog with status `not-started`, as the one required pre-proposal touch to the document

#### Scenario: No mid-flight status-flip PR is required

- **WHEN** a product/architecture-scope change with an existing `docs/roadmap.md` row is proposed and its `openspec/changes/<name>/` folder is created
- **THEN** no separate PR is required to flip that row's status in `docs/roadmap.md`; `openspec list --json` reflects the change's in-progress status from the folder's presence

#### Scenario: A row reaching archived is the completion signal

- **WHEN** a product/architecture-scope change with an existing `docs/roadmap.md` row completes and is archived
- **THEN** its row is flipped to `archived` with links to the archive folder and promoted spec(s), as the one required post-completion touch to the document

### Requirement: One Change Equals One Coherent Spec Delta

A single OpenSpec change SHALL correspond to exactly one coherent spec delta. If implementing a change reveals a second, distinct spec-level concern, or a design decision not already captured in that change's `design.md`, the change SHALL NOT be expanded in flight to absorb it — a new Change Backlog row (for product/architecture-scope work) or a new change proposal (for workflow-scope work) SHALL be created instead. `implement-identity-authentication-from-scratch` is the illustrative precedent this requirement generalizes from: the ownership/access-control concern (`video-processing-access` spec) was only discovered while archiving, and a design decision about unconfigured startup behavior shipped without ever being written into `design.md` up front — it should have been closer to five smaller changes.

#### Scenario: A second spec concern is discovered mid-implementation

- **WHEN** implementing a change surfaces a distinct spec-level concern not covered by that change's own delta spec
- **THEN** the in-flight change is not expanded to cover it; a separate change is proposed for that concern instead

#### Scenario: An undocumented design decision is discovered mid-implementation

- **WHEN** implementing a change requires a design decision that was never written into that change's `design.md`
- **THEN** the in-flight change is not expanded to absorb it either; a new change is proposed to make and document that decision, exactly as the second-spec-concern scenario above requires

## MODIFIED Requirements

### Requirement: Finalization PR Bundles Archive, Documentation, and Roadmap Status

After the implementation PR for a change is merged, an agent SHALL use one finalization PR to: mark the change's completed tasks; promote its delta specs into canonical specs; move the complete change folder from `openspec/changes/<name>/` to `openspec/changes/archive/<date>-<name>/`; update any permanent documentation or agent instructions (`README.md`, `docs/`, `CLAUDE.md`, `AGENTS.md`) that need to reflect the shipped change; and, if the change has a `docs/roadmap.md` Change Backlog row, flip that row to `archived` with links to the archive folder and promoted spec(s). These SHALL NOT be split across separate docs/archive/roadmap PRs. This finalization PR SHALL contain no application code or tests.

#### Scenario: Finalization PR after implementation contains all closure operations together

- **WHEN** the implementation PR is merged and a subsequent PR marks tasks complete, updates canonical specs from the delta, moves the change folder to archive, updates permanent documentation, and flips the roadmap backlog status (if the change has a row)
- **THEN** it is a valid finalization PR

#### Scenario: Finalization PR before implementation merges is premature

- **WHEN** a PR checks off tasks or archives a change whose implementation PR has not merged
- **THEN** that PR SHALL NOT be merged

#### Scenario: Finalization PR containing application code is invalid

- **WHEN** a finalization PR modifies application source or test files
- **THEN** it is NOT a valid finalization PR and the code changes SHALL be removed

#### Scenario: Documentation is not silently bundled into implementation

- **WHEN** permanent documentation, agent instructions, or configuration must change as a consequence of implementation
- **THEN** those files SHALL be delivered in the finalization PR, never bundled with the application code (implementation) PR

#### Scenario: A workflow/process-only change has no roadmap row to flip

- **WHEN** the finalization PR is for a workflow/process-only change that never received a `docs/roadmap.md` Change Backlog row
- **THEN** the finalization PR proceeds without a roadmap-status edit — its absence is expected, not an omission

### Requirement: Explore Precedes Propose For Complex Or Ambiguous Changes

Among changes that are not exempt from the OpenSpec flow under the existing trivial-edit criteria (typo fixes, comment tweaks, dependency bumps), those that are complex or ambiguous SHALL go through `/opsx:explore` before `/opsx:propose`. A change qualifies as complex or ambiguous under the same criteria this project already uses to decide whether a change needs a `design.md`: cross-cutting impact across multiple modules/services, a new architectural pattern or external dependency, security/performance/migration complexity, or open design decisions not already settled by the change's own scoping description — a `docs/roadmap.md` Change Backlog row's description for product/architecture-scope work, or the idea's own stated scope for a workflow/process-only change that has no such row. Changes that are simple and already unambiguously scoped by that description MAY go straight to `/opsx:propose` without an explore step. This is a judgment call made when picking up the work, not a mechanically checked gate — when genuinely unsure, `/opsx:explore` SHALL be run.

#### Scenario: A complex or ambiguous change is proposed

- **WHEN** the next `not-started` row picked from `docs/roadmap.md`'s Change Backlog involves cross-cutting impact, a new architectural pattern or external dependency, security/performance/migration complexity, or design questions the row description doesn't already settle
- **THEN** `/opsx:explore` is run on it before `/opsx:propose`

#### Scenario: A simple, already-scoped change skips straight to propose

- **WHEN** the next `not-started` row picked from `docs/roadmap.md`'s Change Backlog is narrowly scoped to a single file or config change with no open design questions (e.g. fixing one stale documentation link, adding one already-fully-specified service to `docker-compose.yml`)
- **THEN** `/opsx:propose` may be run directly, without an `/opsx:explore` step

#### Scenario: A trivial edit remains exempt from the whole flow, explore included

- **WHEN** a change qualifies for the existing trivial-edit exemption from the full OpenSpec flow (typo fix, comment tweak, dependency bump)
- **THEN** it never reaches `/opsx:propose` or `/opsx:explore` at all — this requirement only applies to changes that already go through OpenSpec

#### Scenario: A workflow/process-only change with no backlog row is still evaluated for explore

- **WHEN** a workflow/process-only change with no `docs/roadmap.md` Change Backlog row (per "Roadmap Change Backlog Is Scoped To Product/Architecture Work" above) is complex or ambiguous by the same criteria — cross-cutting impact, a new architectural pattern or external dependency, security/performance/migration complexity, or open design questions its own stated scope doesn't already settle
- **THEN** `/opsx:explore` is run on it before `/opsx:propose`, exactly as it would be for a backlog row — the absence of a row does not exempt it from this requirement
