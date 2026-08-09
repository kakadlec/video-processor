## ADDED Requirements

### Requirement: Explore Precedes Propose For Complex Or Ambiguous Changes

Among changes that are not exempt from the OpenSpec flow under the existing trivial-edit criteria (typo fixes, comment tweaks, dependency bumps), those that are complex or ambiguous SHALL go through `/opsx:explore` before `/opsx:propose`. A change qualifies as complex or ambiguous under the same criteria this project already uses to decide whether a change needs a `design.md`: cross-cutting impact across multiple modules/services, a new architectural pattern or external dependency, security/performance/migration complexity, or open design decisions not already settled by the Change Backlog row's description. Changes that are simple and already unambiguously scoped by their Change Backlog row description MAY go straight to `/opsx:propose` without an explore step. This is a judgment call made when picking up the next backlog row, not a mechanically checked gate — when genuinely unsure, `/opsx:explore` SHALL be run.

#### Scenario: A complex or ambiguous change is proposed

- **WHEN** the next `not-started` row picked from `docs/roadmap.md`'s Change Backlog involves cross-cutting impact, a new architectural pattern or external dependency, security/performance/migration complexity, or design questions the row description doesn't already settle
- **THEN** `/opsx:explore` is run on it before `/opsx:propose`

#### Scenario: A simple, already-scoped change skips straight to propose

- **WHEN** the next `not-started` row picked from `docs/roadmap.md`'s Change Backlog is narrowly scoped to a single file or config change with no open design questions (e.g. fixing one stale documentation link, adding one already-fully-specified service to `docker-compose.yml`)
- **THEN** `/opsx:propose` may be run directly, without an `/opsx:explore` step

#### Scenario: A trivial edit remains exempt from the whole flow, explore included

- **WHEN** a change qualifies for the existing trivial-edit exemption from the full OpenSpec flow (typo fix, comment tweak, dependency bump)
- **THEN** it never reaches `/opsx:propose` or `/opsx:explore` at all — this requirement only applies to changes that already go through OpenSpec
