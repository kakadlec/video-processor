## ADDED Requirements

### Requirement: OpenSpec Lifecycle Is Explicitly Opt-In

OpenSpec lifecycle requirements in this specification SHALL apply only when a developer explicitly requests OpenSpec, invokes an `/opsx:*` command, or explicitly continues a named active OpenSpec change. A request to implement, fix, investigate, or answer a task SHALL NOT by itself activate OpenSpec, regardless of the request's size, type, or perceived complexity.

#### Scenario: Direct implementation remains direct

- **WHEN** a developer asks an agent to implement or fix a change without explicitly selecting OpenSpec
- **THEN** the agent SHALL perform the work without proposing, requiring, or refusing work for an OpenSpec lifecycle

#### Scenario: Explicit OpenSpec request activates the lifecycle

- **WHEN** a developer asks to use OpenSpec, requests the OpenSpec lifecycle, invokes an `/opsx:*` command, or names an active OpenSpec change to continue
- **THEN** the agent SHALL apply the OpenSpec lifecycle requirements in this specification

## MODIFIED Requirements

### Requirement: Roadmap Change Backlog Is Scoped To Product/Architecture Work

For a change that has explicitly opted into OpenSpec, `docs/roadmap.md`'s Change Backlog SHALL track only changes within the product/architecture scope of the 8-phase DDD roadmap. OpenSpec changes whose own subject is this repository's development workflow or OpenSpec process itself (as opposed to product/architecture behavior) SHALL NOT receive a `docs/roadmap.md` Change Backlog row, at any stage. For an opted-in change that is in scope, `docs/roadmap.md` SHALL be edited only when a row is added or re-scoped and when the row is marked `archived`. Direct work that has not opted into OpenSpec SHALL NOT be required to create, update, or consult a Change Backlog row.

#### Scenario: A direct change has no roadmap obligation

- **WHEN** a developer implements a product or architecture change without opting into OpenSpec
- **THEN** no `docs/roadmap.md` Change Backlog edit or lookup is required

#### Scenario: An opted-in workflow/process-only change has no roadmap row

- **WHEN** an explicitly opted-in change concerns the repository development workflow, OpenSpec process, or agent tooling rather than product/architecture behavior
- **THEN** it proceeds through the selected OpenSpec lifecycle without a `docs/roadmap.md` Change Backlog row

#### Scenario: An opted-in product/architecture change is added to the backlog

- **WHEN** a developer opts into OpenSpec for new product/architecture-scope work and scopes that work
- **THEN** a row for it is added to `docs/roadmap.md`'s Change Backlog with status `not-started`

#### Scenario: No mid-flight status-flip PR is required

- **WHEN** an opted-in product/architecture-scope change with an existing `docs/roadmap.md` row is proposed and its `openspec/changes/<name>/` folder is created
- **THEN** no separate PR is required to flip that row's status; `openspec list --json` reflects the in-progress status from the folder's presence

#### Scenario: An opted-in row reaching archived is the completion signal

- **WHEN** an opted-in product/architecture-scope change with an existing `docs/roadmap.md` row completes and is archived
- **THEN** its row is flipped to `archived` with links to the archive folder and promoted spec(s)

### Requirement: One Change Equals One Coherent Spec Delta

Within an explicitly opted-in OpenSpec change, a single change SHALL correspond to exactly one coherent spec delta. If implementing that OpenSpec change reveals a second distinct spec-level concern, or a design decision not captured in its `design.md`, the change SHALL NOT be expanded in flight to absorb it; a separate OpenSpec change SHALL be proposed for that concern. This requirement SHALL NOT impose an OpenSpec change on direct work that has not opted in.

#### Scenario: A second concern is discovered in an opted-in change

- **WHEN** implementing an explicitly opted-in OpenSpec change surfaces a distinct spec-level concern not covered by that change's delta spec
- **THEN** the in-flight change is not expanded to cover it and a separate OpenSpec change is proposed for that concern

#### Scenario: Direct work is not converted into an OpenSpec change

- **WHEN** direct work reveals a distinct concern and the developer has not opted into OpenSpec
- **THEN** this requirement does not require the agent to create an OpenSpec change

### Requirement: Explore Precedes Propose For Complex Or Ambiguous Changes

Within an explicitly opted-in OpenSpec lifecycle, a complex or ambiguous change SHALL go through `/opsx:explore` before `/opsx:propose`. Complexity or ambiguity includes cross-cutting impact, a new architectural pattern or external dependency, security/performance/migration complexity, or open design decisions not settled by the stated scope. An explicitly opted-in change that is simple and already unambiguously scoped MAY proceed directly to `/opsx:propose`. This requirement SHALL NOT cause an agent to select OpenSpec for direct work.

#### Scenario: An opted-in complex change is explored

- **WHEN** an explicitly opted-in OpenSpec change involves cross-cutting impact, a new architectural pattern or external dependency, security/performance/migration complexity, or unresolved design questions
- **THEN** `/opsx:explore` is run before `/opsx:propose`

#### Scenario: An opted-in simple change skips explore

- **WHEN** an explicitly opted-in OpenSpec change is narrowly and unambiguously scoped
- **THEN** `/opsx:propose` may run without `/opsx:explore`

#### Scenario: A direct complex change does not imply OpenSpec

- **WHEN** a developer asks for a complex direct implementation without selecting OpenSpec
- **THEN** the agent does not activate OpenSpec solely because of its complexity

### Requirement: Propose PR Contains Only Change Artifacts and Must Merge Before Implementation

For an explicitly opted-in OpenSpec change, its propose PR SHALL contain only the artifacts belonging to that change under `openspec/changes/<name>/` (`proposal.md`, `design.md`, `tasks.md`, and any delta specs under `openspec/changes/<name>/specs/`). It SHALL NOT contain application code, test files, CI configuration, `CLAUDE.md`, `AGENTS.md`, README files, or modifications to canonical specs under `openspec/specs/`. That propose PR SHALL be merged before implementation work on the opted-in change begins. This requirement does not apply to direct work that has not opted into OpenSpec.

#### Scenario: An opted-in propose PR is isolated

- **WHEN** a PR for an explicitly opted-in OpenSpec proposal modifies only files under `openspec/changes/<name>/`
- **THEN** it is a valid propose PR

#### Scenario: Direct work may begin without a propose PR

- **WHEN** a developer implements work without opting into OpenSpec
- **THEN** this requirement does not require a propose PR before implementation

### Requirement: Implementation PR Contains Only the Change's Declared Scope

For an explicitly opted-in OpenSpec change, its implementation PR SHALL contain only the files that implement the change's declared proposal scope. It SHALL NOT contain task checkoffs, documentation, `CLAUDE.md`, `AGENTS.md`, or files under `openspec/`; those belong in the opted-in change's finalization PR. This requirement does not impose PR role separation on work that has not opted into OpenSpec.

#### Scenario: An opted-in implementation PR is scoped

- **WHEN** an implementation PR belongs to an explicitly opted-in OpenSpec change
- **THEN** it contains only the files in that change's declared implementation scope

#### Scenario: A direct PR is not assigned an OpenSpec role

- **WHEN** a PR is created for work that did not opt into OpenSpec
- **THEN** this requirement does not classify it as an OpenSpec implementation PR or require a finalization PR

### Requirement: Finalization PR Bundles Archive, Documentation, and Roadmap Status

After the implementation PR for an explicitly opted-in OpenSpec change is merged, an agent SHALL use one finalization PR to mark completed tasks, promote delta specs, archive the change folder, update permanent documentation or agent instructions that reflect the shipped change, and update the roadmap row when the opted-in change has one. This finalization PR SHALL contain no application code or tests. This requirement does not require finalization work for direct changes that did not opt into OpenSpec.

#### Scenario: An opted-in change is finalized together

- **WHEN** the implementation PR for an explicitly opted-in OpenSpec change is merged
- **THEN** its archive, task checkoffs, required documentation, and applicable roadmap update are delivered together in one finalization PR

#### Scenario: A direct PR has no OpenSpec finalization requirement

- **WHEN** a PR delivers work that did not opt into OpenSpec
- **THEN** no OpenSpec archive or finalization PR is required

### Requirement: Claude Code Workflow Guidance Is Delivered Via An Auto-Triggered Skill

Claude Code agents SHALL receive pull-request quality guidance through a dedicated skill under `.claude/skills/`, rather than through full prose duplicated in always-loaded project instructions. The skill SHALL activate when a developer asks to open, update, hand off, or merge a pull request, and when the agent creates a pull request itself. It SHALL cover applicable local tests, required CI checks, review comments, branch freshness, commit conventions, and explicit merge authorization. It SHALL NOT activate OpenSpec or require an OpenSpec lifecycle.

#### Scenario: Agent-created PR activates quality workflow

- **WHEN** an agent creates a pull request as part of completing work
- **THEN** the pull-request quality skill is applied before the PR is handed off

#### Scenario: PR request activates quality workflow

- **WHEN** a developer asks to open, update, hand off, or merge a pull request
- **THEN** the pull-request quality skill is applied

#### Scenario: Direct implementation does not activate OpenSpec

- **WHEN** a developer requests direct implementation without a PR action or explicit OpenSpec request
- **THEN** the pull-request quality skill does not activate an OpenSpec lifecycle

### Requirement: Roadmap-Item Lifecycle Sequencing Is Encoded As A Skill

The full OpenSpec lifecycle SHALL be encoded in a dedicated skill distinct from the per-step vendored OpenSpec skills. That lifecycle skill SHALL activate only when a developer explicitly requests OpenSpec, invokes an `/opsx:*` command, or explicitly continues a named active OpenSpec change. Once activated, it SHALL sequence explore when warranted, propose, propose-PR merge, apply, and archive/finalization. It SHALL not activate from a generic task, backlog lookup, implementation request, or perceived change complexity.

#### Scenario: Explicit lifecycle request activates the skill

- **WHEN** a developer explicitly asks to implement work through the full OpenSpec lifecycle
- **THEN** the lifecycle skill guides the change through its OpenSpec steps

#### Scenario: Generic task lookup does not activate the skill

- **WHEN** a developer asks which task is next or asks to implement a task without selecting OpenSpec
- **THEN** the lifecycle skill does not activate

#### Scenario: Opted-in implementation waits for its propose PR

- **WHEN** the lifecycle skill is guiding an explicitly opted-in change whose propose PR is open but not merged
- **THEN** it does not proceed to `/opsx:apply`
