# development-workflow Specification

## Purpose
Defines how changes land on `main` for this repository: required CI gates (tests, SAST, dependency vulnerability scanning), the PR-only branch protection workflow, Conventional Commit conventions, and the automated release process. Contributors and AI agents working in this repo should treat these as binding constraints, not suggestions.
## Requirements
### Requirement: Automated Test Gate
Every push to `main` and every pull request SHALL run the full test suite (`go test ./...`) in CI, with `ffmpeg` available in the CI environment and a PostgreSQL service available and reachable via `IDENTITY_POSTGRES_TEST_DSN`. The CI test job SHALL fail if any test fails. Tests that depend on PostgreSQL SHALL NOT be allowed to silently skip in the CI environment.

#### Scenario: CI fails on a failing test
- **WHEN** a commit is pushed where a test fails
- **THEN** the CI test job fails and is visibly reported on the commit or pull request

#### Scenario: CI passes when all tests pass
- **WHEN** a commit is pushed where every test passes
- **THEN** the CI test job succeeds

#### Scenario: PostgreSQL-backed tests run for real in CI, not skip
- **WHEN** the CI test job runs `go test ./...`
- **THEN** `IDENTITY_POSTGRES_TEST_DSN` is set to a reachable PostgreSQL service provisioned by CI, and `internal/identity/infrastructure/postgres`'s adapter tests execute against it rather than skipping

### Requirement: Local Full-Stack Development Service
The repository SHALL provide a documented single command that starts the application together with PostgreSQL, with identity enabled, so a contributor can exercise registration, login, and bearer-protected video-processing routes locally without hand-configuring environment variables or manually wiring network access to the database. `docker-compose.yml` SHALL be the sole documented entry point for Docker-based **local development** workflows — there SHALL NOT be a separately-documented plain `docker build`/`docker run` alternative for local development. Container deployment (`docs/operations.md`) is a distinct concern and is unaffected by this requirement.

#### Scenario: Contributor starts the full stack with one command
- **WHEN** a contributor runs the documented `docker compose up --build` command
- **THEN** the application container builds from the repository's `Dockerfile`, starts only after PostgreSQL's healthcheck reports healthy, and serves `/api/auth/register` and `/api/auth/login` without any additional configuration

### Requirement: Local PostgreSQL Development Service
The repository SHALL provide a `docker-compose.yml` at its root that starts a local PostgreSQL service matching the version used in CI, so any contributor can run the full test suite — including PostgreSQL-backed adapter tests — locally with a single documented command, without hand-provisioning a database and without manually exporting a database connection string.

#### Scenario: Contributor runs the full suite locally
- **WHEN** a contributor runs the documented `docker compose run --build --rm app-test go test ./... -v` command
- **THEN** the command runs `go test ./...` inside a container built from the repository's `Dockerfile`'s test stage (the only stage with both the Go toolchain and `ffmpeg`), against the compose-provisioned PostgreSQL service, with `IDENTITY_POSTGRES_TEST_DSN` already configured — exercising the PostgreSQL-backed adapter tests without the contributor exporting anything or installing Go/ffmpeg locally

#### Scenario: Local and CI databases stay aligned
- **WHEN** the PostgreSQL image version is changed in `docker-compose.yml`
- **THEN** the corresponding service image in `.github/workflows/ci.yml` is updated to match in the same change

### Requirement: SAST Gate
Every push to `main` and every pull request SHALL run a static application security testing scan (`gosec`) against the codebase in CI. The CI SAST job SHALL fail if the scan reports any finding. As of this change, the codebase has zero outstanding `gosec` findings; the SAST job is expected to be green.

#### Scenario: CI fails on a SAST finding
- **WHEN** `gosec` reports one or more findings against the code
- **THEN** the CI SAST job fails and is visibly reported

#### Scenario: CI passes when the scan is clean
- **WHEN** `gosec` reports zero findings against the code
- **THEN** the CI SAST job succeeds

#### Scenario: Suppression is a last resort, checked against the rule's documented fix pattern first
- **WHEN** a `gosec` finding is reported
- **THEN** the rule's own documentation SHALL be checked for a validation pattern gosec recognizes as resolving the finding, and that pattern SHALL be applied and verified (re-running `gosec`) before falling back to suppression

#### Scenario: Findings that remain after that are fixed or explicitly suppressed, never silenced globally
- **WHEN** a specific `gosec` finding is judged a false positive or an accepted risk with no recognized fix pattern
- **THEN** it SHALL be suppressed with a bare inline `#nosec G<rule-id>` comment (no restated prose in-line; rationale belongs in the commit message/PR description), not by disabling the SAST job or excluding whole files/rules project-wide

### Requirement: Dependency Vulnerability Hygiene
The system's Go module dependencies SHALL NOT have open Dependabot vulnerability alerts. When a new alert is opened against a direct or transitive dependency, it SHALL be resolved by upgrading the implicated module (directly, or by upgrading the direct dependency that pulls it in transitively) to a patched version, not by ignoring or dismissing the alert.

#### Scenario: A dependency vulnerability alert is resolved by upgrading
- **WHEN** Dependabot opens an alert against a module in `go.mod`/`go.sum`
- **THEN** the module (or the direct dependency pulling it in transitively) is upgraded to a version that resolves the advisory, and `go.sum` no longer resolves to the vulnerable version

#### Scenario: Dependency upgrade preserves existing behavior
- **WHEN** a dependency is upgraded to resolve a vulnerability alert
- **THEN** `go test ./...` continues to pass without modification to test expectations

### Requirement: Pinned And Cached CI Tool Versions
The `gosec` and `govulncheck` tools used by the SAST and vulnerability-scan CI jobs SHALL be installed at specific pinned released versions (not `@latest`), and the CI workflow SHALL cache each tool's installed binary keyed on the tool's pinned version so that unchanged versions do not require reinstalling the tool from source on every run.

#### Scenario: Tool install is skipped on an unchanged pinned version
- **WHEN** the `sast` or `vulncheck` CI job runs and the cache for the currently pinned tool version already exists
- **THEN** the job restores the cached tool binary instead of rerunning `go install`, and still runs the tool's scan (`gosec ./...` / `govulncheck ./...`) exactly as before

#### Scenario: A version bump invalidates the cache
- **WHEN** the pinned version of `gosec` or `govulncheck` in the workflow is changed
- **THEN** the cache key changes accordingly, causing a fresh `go install` of the new pinned version on the next run

### Requirement: Change Completion Requires A Passing Test Run
A change whose diff includes one or more Go module input files (`.go` source files, `go.mod`, or `go.sum`) SHALL NOT be considered complete until `go test ./...` has been run and passes, in addition to any applicable CI checks. A change whose diff is limited to non-build inputs — documentation, OpenSpec artifacts, or agent-configuration/skill files — with no Go module input files, is exempt from this requirement.

#### Scenario: A Go change is not marked done without a passing local test run
- **WHEN** a change whose diff includes one or more `.go` files, `go.mod`, or `go.sum` is made
- **THEN** `go test ./...` is run and passes before the change is reported as complete

#### Scenario: A dependency-only change still requires a passing test run
- **WHEN** a change modifies `go.mod` or `go.sum` without touching any `.go` file (for example, a dependency version bump)
- **THEN** `go test ./...` is run and passes before the change is reported as complete — the non-build exemption does not apply

#### Scenario: A non-build change is exempt from the local test-run requirement
- **WHEN** a change's diff includes no `.go` files, `go.mod`, or `go.sum`
- **THEN** the change may be reported as complete without running `go test ./...`, and this requirement does not apply

### Requirement: Conventional Commit Messages
Commit messages on this project SHALL follow the [Conventional Commits](https://www.conventionalcommits.org/) format (`type: description`, e.g. `feat:`, `fix:`, `chore:`, `docs:`, `ci:`, `test:`, `refactor:`), with a `!` after the type or a `BREAKING CHANGE:` footer for breaking changes. This is the signal the automated release process uses to compute the next version.

#### Scenario: Feature commit triggers a minor version bump
- **WHEN** a commit with type `feat:` lands on `main`
- **THEN** the next proposed release version increments the minor component

#### Scenario: Fix commit triggers a patch version bump
- **WHEN** a commit with type `fix:` lands on `main`
- **THEN** the next proposed release version increments the patch component

#### Scenario: Breaking change triggers a major version bump
- **WHEN** a commit includes `!` after its type or a `BREAKING CHANGE:` footer
- **THEN** the next proposed release version increments the major component

### Requirement: Automated Release Pull Request
On every push to `main`, the system SHALL maintain a single up-to-date "Release PR" that aggregates unreleased Conventional Commits and shows the computed next version and changelog. No tag or GitHub Release SHALL be created until this PR is merged by a human.

#### Scenario: Release PR updates on new commits
- **WHEN** a new Conventional Commit is pushed to `main` while a Release PR is already open
- **THEN** the existing Release PR is updated in place to include the new commit and any resulting version change, rather than opening a duplicate PR

#### Scenario: Merging the Release PR cuts a release
- **WHEN** the Release PR is merged
- **THEN** a git tag matching the computed version is created, a GitHub Release with generated release notes is published, and `CHANGELOG.md` is updated

### Requirement: Pull Request Required For All Changes
No commit SHALL land on `main` except through a merged pull request. Direct pushes to `main` SHALL be rejected, including for repository administrators.

#### Scenario: Direct push to main is rejected
- **WHEN** a direct push to `main` is attempted, by any account including an admin
- **THEN** GitHub rejects the push due to branch protection

#### Scenario: Change lands via a merged PR
- **WHEN** a change is ready
- **THEN** it is pushed to a feature branch, opened as a PR against `main`, and merged only once required checks pass

### Requirement: Merge Requires Passing Status Checks
A pull request against `main` SHALL NOT be mergeable unless `Build & Test`, `SAST (gosec)`, and `Vulnerability Scan (govulncheck)` all report success, and the PR branch is up to date with `main`.

#### Scenario: Merge blocked by a failing required check
- **WHEN** a PR has `Build & Test`, `SAST (gosec)`, or `Vulnerability Scan (govulncheck)` failing
- **THEN** GitHub blocks the merge button/API for that PR

#### Scenario: Merge allowed once all checks pass
- **WHEN** a PR has `Build & Test`, `SAST (gosec)`, and `Vulnerability Scan (govulncheck)` all passing and is up to date with `main`
- **THEN** the PR is mergeable

#### Scenario: Automated release PRs are subject to the same gate
- **WHEN** `release-please` opens its own release PR against `main`
- **THEN** that PR is mergeable only under the same conditions as any other PR — no special bypass

### Requirement: Vulnerability Scan Gate
Every push to `main` and every pull request SHALL run [`govulncheck`](https://go.dev/security/vulncheck) against the module, its dependencies, and the Go toolchain/stdlib. The CI job SHALL fail if any reachable, known vulnerability is found. Where branch protection requires status checks for merging (see the pull-request-workflow requirements), this check SHALL be included alongside the test and SAST gates.

#### Scenario: CI fails on a reachable vulnerability
- **WHEN** `govulncheck` reports a vulnerability that the code actually reaches
- **THEN** the CI vulnerability-scan job fails and is visibly reported

#### Scenario: CI passes when no reachable vulnerability is found
- **WHEN** `govulncheck` finds no vulnerability reachable from the code, even if unreachable vulnerabilities exist elsewhere in the dependency graph
- **THEN** the CI vulnerability-scan job succeeds

### Requirement: Automated Dependency Update Proposals
The repository SHALL use Dependabot to automatically propose updates for Go modules, GitHub Actions, and the Docker base image on a recurring schedule, and SHALL have Dependabot security alerts and automated security updates enabled for vulnerability-driven updates outside that schedule.

#### Scenario: Scheduled dependency update PR
- **WHEN** a newer version of a tracked Go module, GitHub Action, or Docker base image is available
- **THEN** Dependabot opens a pull request proposing the update on its configured schedule

#### Scenario: Security update PR outside the schedule
- **WHEN** a dependency has a known security vulnerability
- **THEN** Dependabot opens a pull request proposing the fix independent of the regular schedule

#### Scenario: Dependency update PRs are subject to the same merge gate
- **WHEN** Dependabot opens a pull request against `main`
- **THEN** that pull request is mergeable only once it satisfies the same required status checks as any other pull request

### Requirement: Missing Test Prerequisites Must Cause a Non-Zero Exit
When a hard runtime prerequisite for the integration test suite is absent from the environment, `go test ./...` SHALL exit with a non-zero exit code and a clear, actionable error message identifying the missing prerequisite and referring to the documented fallback. Exiting with code 0 when no tests ran is not acceptable — code 0 is indistinguishable from all tests passing and constitutes a false green.

#### Scenario: ffmpeg absent causes go test to exit non-zero
- **WHEN** `go test ./...` is run in an environment where `ffmpeg` is not on `PATH`
- **THEN** the process exits with a non-zero exit code and prints an English message identifying the missing prerequisite and pointing to the Docker fallback documented in `CLAUDE.md`

#### Scenario: ffmpeg present leaves test behavior unchanged
- **WHEN** `go test ./...` is run in an environment where `ffmpeg` is on `PATH`
- **THEN** the suite runs and exits with the same outcome as before this change — this requirement adds no new test cases and changes no passing behavior

### Requirement: OpenSpec Lifecycle Is Explicitly Opt-In

OpenSpec lifecycle requirements in this specification SHALL apply only when a developer explicitly requests OpenSpec, invokes an `/opsx:*` command, or explicitly continues a named active OpenSpec change. A request to implement, fix, investigate, or answer a task SHALL NOT by itself activate OpenSpec, regardless of the request's size, type, or perceived complexity.

#### Scenario: Direct implementation remains direct

- **WHEN** a developer asks an agent to implement or fix a change without explicitly selecting OpenSpec
- **THEN** the agent SHALL perform the work without proposing, requiring, or refusing work for an OpenSpec lifecycle

#### Scenario: Explicit OpenSpec request activates the lifecycle

- **WHEN** a developer asks to use OpenSpec, requests the OpenSpec lifecycle, invokes an `/opsx:*` command, or names an active OpenSpec change to continue
- **THEN** the agent SHALL apply the OpenSpec lifecycle requirements in this specification

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

#### Scenario: An undocumented design decision is discovered in an opted-in change

- **WHEN** implementing an explicitly opted-in OpenSpec change requires a design decision that was not captured in that change's `design.md`
- **THEN** the in-flight change is not expanded to absorb it and a separate OpenSpec change is proposed to make and document that decision

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

#### Scenario: An opted-in propose PR containing implementation is invalid

- **WHEN** a PR for an explicitly opted-in OpenSpec change contains both proposal artifacts and application code or tests
- **THEN** it is not a valid propose PR and SHALL be split before implementation proceeds

#### Scenario: Opted-in implementation waits for the propose PR merge

- **WHEN** the propose PR for an explicitly opted-in OpenSpec change is open but not merged
- **THEN** no implementation work for that change begins

#### Scenario: Direct work may begin without a propose PR

- **WHEN** a developer implements work without opting into OpenSpec
- **THEN** this requirement does not require a propose PR before implementation

### Requirement: Implementation PR Contains Only the Change's Declared Scope

For an explicitly opted-in OpenSpec change, its implementation PR SHALL contain only the files that implement the change's declared proposal scope. It SHALL NOT contain task checkoffs, documentation, `CLAUDE.md`, `AGENTS.md`, or files under `openspec/`; those belong in the opted-in change's finalization PR. This requirement does not impose PR role separation on work that has not opted into OpenSpec.

#### Scenario: An opted-in implementation PR is scoped

- **WHEN** an implementation PR belongs to an explicitly opted-in OpenSpec change
- **THEN** it contains only the files in that change's declared implementation scope

#### Scenario: An opted-in source-and-test implementation PR is valid

- **WHEN** an opted-in feature or behavior change's implementation PR modifies only its declared application source and test files
- **THEN** it is a valid implementation PR

#### Scenario: An opted-in configuration implementation PR is valid

- **WHEN** an opted-in configuration, infrastructure, or CI change's implementation PR modifies only the specific files declared by its proposal
- **THEN** it is a valid implementation PR

#### Scenario: Task checkoffs are excluded from opted-in implementation

- **WHEN** an opted-in implementation PR also modifies `tasks.md` to check off completed work
- **THEN** it is invalid and the checkoffs SHALL be deferred to finalization

#### Scenario: Permanent guidance is excluded from opted-in implementation

- **WHEN** an opted-in implementation PR also modifies permanent documentation, `CLAUDE.md`, or `AGENTS.md`
- **THEN** it is invalid and those updates SHALL be deferred to finalization

#### Scenario: OpenSpec artifacts are excluded from opted-in implementation

- **WHEN** an opted-in implementation PR also modifies files under `openspec/`
- **THEN** it is invalid and those updates SHALL be deferred to finalization

#### Scenario: A direct PR is not assigned an OpenSpec role

- **WHEN** a PR is created for work that did not opt into OpenSpec
- **THEN** this requirement does not classify it as an OpenSpec implementation PR or require a finalization PR

### Requirement: Finalization PR Bundles Archive, Documentation, and Roadmap Status

After the implementation PR for an explicitly opted-in OpenSpec change is merged, an agent SHALL use one finalization PR to mark completed tasks, promote delta specs, archive the change folder, update permanent documentation or agent instructions that reflect the shipped change, and update the roadmap row when the opted-in change has one. This finalization PR SHALL contain no application code or tests. This requirement does not require finalization work for direct changes that did not opt into OpenSpec.

#### Scenario: An opted-in change is finalized together

- **WHEN** the implementation PR for an explicitly opted-in OpenSpec change is merged
- **THEN** its archive, task checkoffs, required documentation, and applicable roadmap update are delivered together in one finalization PR

#### Scenario: Opted-in finalization before implementation merges is premature

- **WHEN** a PR checks off tasks or archives an opted-in OpenSpec change whose implementation PR has not merged
- **THEN** that finalization PR SHALL NOT be merged

#### Scenario: Opted-in finalization containing application code is invalid

- **WHEN** an opted-in OpenSpec finalization PR modifies application source or tests
- **THEN** it is invalid and those implementation changes SHALL be removed

#### Scenario: Documentation is not bundled into opted-in implementation

- **WHEN** permanent documentation or agent instructions must change as a consequence of an opted-in implementation
- **THEN** those files SHALL be delivered in finalization, not in the implementation PR

#### Scenario: An opted-in workflow change has no roadmap row to flip

- **WHEN** finalizing an opted-in workflow/process-only change that has no Change Backlog row
- **THEN** finalization proceeds without a roadmap edit

#### Scenario: A direct PR has no OpenSpec finalization requirement

- **WHEN** a PR delivers work that did not opt into OpenSpec
- **THEN** no OpenSpec archive or finalization PR is required

### Requirement: Merge Requires Explicit User Authorization

An agent SHALL NOT merge any pull request, or take any action that causes a pull request to be merged, based on inferred authorization. Merge authorization requires an explicit instruction from the user in the current session and applies only to the designated PR. Completion signals — including all required CI checks passing, completed tasks, absence of blocking comments, or authorization for another PR — do NOT constitute authorization for a different PR.

#### Scenario: All checks pass but no explicit instruction — agent does not merge

- **WHEN** all required CI checks for a PR are passing
- **THEN** the agent SHALL report the ready state and wait for an explicit merge instruction

#### Scenario: Explicit user instruction authorizes a specific merge

- **WHEN** the user explicitly instructs the agent to merge a named or clearly designated PR in the current session
- **THEN** the agent may merge that PR after verifying its required conditions

#### Scenario: Authorization for one PR does not extend to another

- **WHEN** the user authorizes merging one PR in a sequence
- **THEN** the agent SHALL wait for a separate explicit instruction before merging each subsequent PR

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

#### Scenario: Always-loaded guidance stays concise

- **WHEN** `CLAUDE.md` is loaded into a conversation
- **THEN** it contains project context, universal quality requirements, and pointers rather than the full PR workflow prose

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

#### Scenario: Lifecycle PR actions use the PR quality skill

- **WHEN** the opted-in lifecycle opens, updates, hands off, or merges a PR
- **THEN** it invokes the same `repo-workflow` quality and merge requirements used by direct-work PRs
