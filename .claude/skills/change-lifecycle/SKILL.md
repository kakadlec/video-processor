---
name: change-lifecycle
description: Use when starting work on a docs/roadmap.md Change Backlog item or any comparable non-trivial change, when deciding whether /opsx:explore is needed before /opsx:propose, when deciding whether implementation can start yet, or when sequencing a change through explore/propose/apply/archive end to end. Skip for a single trivial edit with no OpenSpec change involved, or for work already mid-sequence where the next step is unambiguous.
---

# Change lifecycle (FIAP X video-processor)

This orchestrates *when* to run each OpenSpec step and what gates sit between them — it does not replace the vendored `openspec-{explore,propose,apply-change,archive-change}` skills, which do the actual work of each step. Use this to decide which step comes next and whether it's safe to proceed; use those skills (or `/opsx:explore`, `/opsx:propose`, `/opsx:apply`, `/opsx:archive`) to execute it.

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
   finalize: task checkoffs → doc updates → roadmap status → THEN
   /opsx:archive → finalization PR (all of the above together)
```

## Step 1: decide if `/opsx:explore` is warranted

Same criteria as `openspec/specs/development-workflow/spec.md`'s "Explore Precedes Propose For Complex Or Ambiguous Changes" requirement: cross-cutting impact across multiple modules/services, a new architectural pattern or external dependency, security/performance/migration complexity, or open design questions the row's description doesn't already settle. A row that's already narrowly and unambiguously scoped may skip straight to `/opsx:propose`. This is a judgment call made when picking up the row, not a mechanical gate — when genuinely unsure, explore.

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
3. Flip the change's `docs/roadmap.md` Change Backlog row to `archived`, if it has one — see that document's own convention on whether every change needs one.
4. Only then run `/opsx:archive` (or the `openspec-archive-change` skill) to promote the delta spec and move the change folder.
5. Open the finalization PR containing all of the above together, per `repo-workflow`'s PR-sequence section.

## What this skill does not decide

Whether a given change needs a `docs/roadmap.md` Change Backlog row at all is `docs/roadmap.md`'s own convention to interpret, not this skill's — don't assume every change needs one just because this sequence mentions the backlog.
