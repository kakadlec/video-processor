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

A single OpenSpec change SHALL correspond to exactly one coherent spec delta. If implementing a change reveals a second, distinct spec-level concern, or a design decision not already captured in that change's `design.md`, the change SHALL NOT be expanded in flight to absorb it — a new Change Backlog row (for product/architecture-scope work) or a new change proposal (for workflow-scope work) SHALL be created instead.

#### Scenario: A second spec concern is discovered mid-implementation

- **WHEN** implementing a change surfaces a distinct spec-level concern not covered by that change's own delta spec
- **THEN** the in-flight change is not expanded to cover it; a separate change is proposed for that concern instead

#### Scenario: An undocumented design decision is discovered mid-implementation

- **WHEN** implementing a change requires a design decision that was never written into that change's `design.md`
- **THEN** the decision is documented in that change's `design.md` before proceeding, or split into a follow-up change if it materially changes scope

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
