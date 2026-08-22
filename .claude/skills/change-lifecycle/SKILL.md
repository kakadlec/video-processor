---
name: change-lifecycle
description: Use only when the user explicitly requests the complete OpenSpec lifecycle or invokes this skill. It sequences explore, propose, apply, and archive for users who choose that optional workflow.
---

# Change lifecycle (FIAP X video-processor)

This optional skill orchestrates *when* to run each OpenSpec step and what gates sit between them. It does not replace the vendored `openspec-{explore,propose,apply-change,archive-change}` skills, which do the actual work of each step. Do not apply this sequence unless the user has chosen it; direct changes do not need OpenSpec.

## The sequence

```
pick a Change Backlog row (or comparable non-trivial idea)
        │
        ▼
   complex or ambiguous?  ──yes──▶  /opsx:explore, discuss
        │ no                              │
        │◀─────────────────────────────────┘
        ▼
   /opsx:propose
        │
        ▼
   open the propose PR ──▶ WAIT for it to be merged, not just approved
        │ (merged)
        ▼
   /opsx:apply
        │
        ▼
   before reporting any task or the change done: run repo-workflow's
   definition-of-done checklist (tests if Go changed, PR review
   comments, etc.) — don't restate that checklist here, invoke it
        │
        ▼
   open the implementation PR, scoped to the proposal's declared files only
   (tasks.md checkoffs excluded — see Step 3's override)
        │ (merged, after explicit user authorization)
        ▼
   finalize: task checkoffs → doc updates → roadmap status (if applicable) → THEN
   /opsx:archive → finalization PR (all of the above together)
```

## Orientation: which step is this change actually on?

Not a step and not a gate — just the diagnostic worth reaching for when the sequence below has to be entered somewhere other than the beginning. Which step you owe depends on where the change actually is, and a backlog row's status cell, a task list, and someone's recollection all go stale independently: a change described as "just proposed" can already have its implementation on `main`, and taking the stated position at face value either re-does merged work or skips past a gate. The cheap signals, when you want them: whether the change folder sits under `openspec/changes/` or `openspec/changes/archive/`, whether the delta is already promoted into `openspec/specs/`, `git log --oneline origin/main -- <the change's files>`, and `gh pr list --state all --search <change-id>`. If the repo and the request disagree, saying so beats quietly picking one.

## Step 1: decide if `/opsx:explore` is warranted

Same criteria as `openspec/specs/development-workflow/spec.md`'s "Explore Precedes Propose For Complex Or Ambiguous Changes" requirement: cross-cutting impact across multiple modules/services, a new architectural pattern or external dependency, security/performance/migration complexity, or open design questions not already settled by the change's own scoping description — a `docs/roadmap.md` Change Backlog row's description for product/architecture-scope work, or the idea's own stated scope for a workflow/process-only change that has no such row (see `docs/roadmap.md`'s scoping rule). Already-unambiguously-scoped work may skip straight to `/opsx:propose`, row or no row. This is a judgment call made when picking up the work, not a mechanical gate — when genuinely unsure, explore.

One open question is easy to read past because both sides sound authoritative: the scoping description and the permanent docs/canonical specs describing the same future state can disagree about mechanism. A row that names one implementation while `docs/` or `openspec/specs/` describes another has not settled that decision — it has recorded two of them, and picking the one you happen to read second is a design decision made by accident. Treat that contradiction as an open design question and explore, rather than reconciling it silently in the proposal.

## Step 2: `/opsx:propose`, then wait for the merge — not the approval

An approved-but-still-open propose PR does **not** authorize starting implementation. `/opsx:apply` (and any hand-edit toward implementation) waits until the propose PR is actually **merged**. This is easy to get backwards under time pressure — don't.

## Step 3: `/opsx:apply`

Work through `tasks.md`. If tasks are grouped by which PR they belong to (implementation vs. finalization — check the task group headers), only do the implementation-scoped groups now; finalization-scoped groups (docs, `CLAUDE.md`, `AGENTS.md`, archive, roadmap status) wait until after the implementation PR merges.

**Override for this repo**: the vendored `openspec-apply-change` skill checks off each task's `- [ ]` → `- [x]` in `tasks.md` immediately as it completes it — that conflicts with this repo's rule that `tasks.md` checkoffs belong only in the finalization PR. When applying implementation-scoped tasks, keep `tasks.md`'s checkoff edits out of the implementation PR's commit/diff (leave them uncommitted, or revert just that file before committing) and re-apply the same checkoffs during finalization instead. Do not let checked implementation-task boxes ride into the implementation PR.

## Step 4: done-verification gate

At every point where a task, a PR, or the whole change needs to be reported done, invoke `repo-workflow`'s "Definition of done" checklist rather than re-deriving it here. This includes before opening the implementation PR and before opening the finalization PR.

## Step 5: finalization

The vendored `/opsx:archive` skill only assesses/syncs delta specs and moves the change folder — it does **not** check off tasks, edit permanent docs, or flip the `docs/roadmap.md` status, and it will let you archive even with incomplete tasks (warns, doesn't block). Invoking it as the first action of this step can leave the rest of finalization undone. Do the work in this order instead:

1. Check off all completed tasks in `tasks.md` (including any implementation-scoped ones deferred per Step 3's override).
2. Update permanent docs (`README.md`, `docs/`, `CLAUDE.md`, `AGENTS.md`) that need to reflect the shipped change.
3. If the change has a `docs/roadmap.md` Change Backlog row (only product/architecture-scope changes do — workflow/process-only changes never gain one, see `docs/roadmap.md`), flip it to `archived`, with links to the archive folder and promoted spec(s). No row means this step is skipped, not missed.
4. Only then run `/opsx:archive` (or the `openspec-archive-change` skill) to promote the delta spec and move the change folder.
5. Open the finalization PR containing all of the above together, per `repo-workflow`'s PR-sequence section.
