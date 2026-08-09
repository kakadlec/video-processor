## Why

The project's documented OpenSpec workflow currently reads `/opsx:propose` → `/opsx:apply` → `/opsx:archive`. In practice this has already caused avoidable rework: `implement-identity-authentication-from-scratch` shipped as one mega-change and only surfaced a missed spec (`video-processing-access`) and an undocumented design decision (unconfigured-startup behavior) while it was being archived — after implementation, when the fix was expensive. A cheap `/opsx:explore` pass before writing `proposal.md` is exactly the step that catches this kind of scoping/design gap early, and this project already has that skill available but treats it as optional. Making it mandatory closes that gap for future backlog work.

## What Changes

- `CLAUDE.md`'s "Development process: OpenSpec is mandatory" section: for changes that are complex or ambiguous, the documented workflow becomes `/opsx:explore` → `/opsx:propose` → `/opsx:apply` → `/opsx:archive`. This is a *second*, narrower distinction layered on top of the existing one — the existing trivial-edit exemption (typo/comment/dependency bump) still skips OpenSpec entirely and is untouched. Among changes that *do* go through OpenSpec, simple and already-obviously-scoped ones (e.g. a single-file config change or a one-line stale-link fix, where the Change Backlog row description already leaves nothing to figure out) may go straight to `/opsx:propose`; complex or ambiguous ones (cross-cutting impact, a new architectural pattern or external dependency, security/performance/migration complexity, or real open design questions) require `/opsx:explore` first.
- `docs/roadmap.md`'s "How to use this" guidance under Change Backlog: adds guidance on when to run `/opsx:explore` before picking up a `not-started` row and running `/opsx:propose`.
- `openspec/specs/development-workflow/spec.md`: a new requirement documenting the explore-before-propose rule and its simple-vs-complex distinction, alongside the existing Propose/Implementation/Finalization PR requirements.
- No new CI or tooling enforcement — like the existing PR-splitting requirements, this is judgment applied when picking the next backlog row, not an automated gate.

## Capabilities

### New Capabilities

(none)

### Modified Capabilities

- `development-workflow`: adds a requirement that `/opsx:explore` precedes `/opsx:propose` for changes that are complex or ambiguous, while simple, already well-scoped OpenSpec changes may still go straight to `/opsx:propose`.

## Impact

- Affected files: `CLAUDE.md`, `docs/roadmap.md`, `openspec/specs/development-workflow/spec.md` (via this change's delta). No application code, no CI workflow files, no tests.
- Affects how every future non-trivial Change Backlog row gets proposed going forward; does not require retroactively re-exploring already-proposed or already-shipped changes.
