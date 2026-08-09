## Context

`CLAUDE.md` already mandates the OpenSpec `propose → apply → archive` cycle for every non-trivial change, with a narrow, enumerated exemption for trivial edits (typo/comment/dependency-only). Separately, this repo has an `/opsx:explore` skill available but nothing in `CLAUDE.md` or `docs/roadmap.md` requires using it. The `implement-identity-authentication-from-scratch` change (see `docs/roadmap.md`'s Change Backlog notes) shipped as one change and only surfaced a missed spec and an undocumented design decision while being archived — exactly the kind of scoping gap a short explore pass before `proposal.md` is meant to catch early, when it's still cheap to split or redirect.

## Goals / Non-Goals

**Goals:**
- Make `/opsx:explore` a required step before `/opsx:propose`, but only for changes complex or ambiguous enough to benefit from it — not for every change that goes through OpenSpec.
- Give the simple/complex distinction a concrete anchor instead of a vague "use judgment," so it doesn't collapse into either "explore everything" or "explore nothing" in practice.
- Document the rule in the same place the equivalent PR-splitting rules already live (`openspec/specs/development-workflow/spec.md`), so it's discoverable the same way.

**Non-Goals:**
- No CI or tooling enforcement. Like the existing Propose/Implementation/Finalization PR requirements in `development-workflow`, this is a documented process norm, not a mechanically checked gate — there's no reliable automated signal that an explore conversation happened, and none for "was this actually complex."
- No change to the existing trivial-edit exemption that skips OpenSpec entirely (typo/comment/dependency bump) — that boundary is untouched; this change only subdivides the "does go through OpenSpec" side of it.
- No retroactive requirement to go back and explore already-proposed or already-archived changes.

## Decisions

- **Explore is required for complex/ambiguous changes, optional for simple ones — a second, narrower distinction than the existing trivial-edit exemption.** Rejected first pass: treating "goes through OpenSpec at all" and "needs explore" as the same binary test. The user explicitly corrected this — simple, already-obviously-scoped OpenSpec changes (a one-line stale-link fix, a single well-specified docker-compose service) don't benefit from an extra exploration pass; the value is in catching scope/design gaps on changes where they're likely to exist.
- **The simple/complex line reuses this project's own `design.md` inclusion criteria** ("cross-cutting change or new architectural pattern; new external dependency or significant data model changes; security, performance, or migration complexity; ambiguity that benefits from technical decisions before coding" — see `openspec instructions design`) rather than inventing a fresh test. Considered: a size-based heuristic (line count, file count). Rejected — size doesn't track risk; a one-line change to auth middleware is riskier than a 200-line new static HTML page. The existing design.md criteria already separate "needs real technical decisions" from "doesn't," which is precisely the distinction explore-before-propose is meant to serve, and reusing it avoids maintaining two similar-but-different rubrics.
- **Documented in `development-workflow`'s `spec.md`, as a new requirement, not folded into an existing one.** The existing PR-splitting requirements (`Propose PR Contains Only Change Artifacts...`, etc.) describe what happens once a change enters the pipeline; this is about what happens immediately before, so it reads more clearly as its own requirement with its own scenarios.
- **No new artifact or CLI enforcement.** `openspec status`/`openspec instructions` have no concept of "was explore run" or "is this complex," and adding one is out of scope for a docs-only process change — matches how the existing PR-splitting rules are enforced today (reviewer/agent discipline, not tooling).

## Risks / Trade-offs

- [Risk] "Complex or ambiguous" is a judgment call, not a mechanical test — an agent or contributor could talk themselves into "this is simple" for something that isn't, the same way a prior change in this repo was wrongly exempted from OpenSpec entirely using self-invented reasoning. → Mitigation: anchor the judgment call to the existing, already-used design.md criteria rather than a fresh one, and default to exploring when genuinely unsure (mirrors this repo's existing "when in doubt, propose first" norm, now "when in doubt, explore first").
- [Risk] A documented-but-unenforced rule can silently erode over time. → Mitigation: none beyond documentation; this mirrors the existing enforcement model for the rest of the OpenSpec process in this repo.
