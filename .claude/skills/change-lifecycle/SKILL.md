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
        │ (merged, after explicit user authorization)
        ▼
   /opsx:archive → finalization PR (tasks/spec/docs/roadmap together)
```

## Step 1: decide if `/opsx:explore` is warranted

Same criteria as `openspec/specs/development-workflow/spec.md`'s "Explore Precedes Propose For Complex Or Ambiguous Changes" requirement: cross-cutting impact across multiple modules/services, a new architectural pattern or external dependency, security/performance/migration complexity, or open design questions the row's description doesn't already settle. A row that's already narrowly and unambiguously scoped may skip straight to `/opsx:propose`. This is a judgment call made when picking up the row, not a mechanical gate — when genuinely unsure, explore.

## Step 2: `/opsx:propose`, then wait for the merge — not the approval

An approved-but-still-open propose PR does **not** authorize starting implementation. `/opsx:apply` (and any hand-edit toward implementation) waits until the propose PR is actually **merged**. This is easy to get backwards under time pressure — don't.

## Step 3: `/opsx:apply`

Work through `tasks.md`. If tasks are grouped by which PR they belong to (implementation vs. finalization — check the task group headers), only do the implementation-scoped groups now; finalization-scoped groups (docs, `CLAUDE.md`, `AGENTS.md`, archive, roadmap status) wait until after the implementation PR merges.

## Step 4: done-verification gate

At every point where a task, a PR, or the whole change needs to be reported done, invoke `repo-workflow`'s "Definition of done" checklist rather than re-deriving it here. This includes before opening the implementation PR and before opening the finalization PR.

## Step 5: `/opsx:archive` / finalization

Bundles task checkoffs, spec promotion, the archive move, permanent-doc updates, and the `docs/roadmap.md` Change Backlog status flip (if the change has a row — see that document's own convention on whether every change needs one) into a single finalization PR, per `repo-workflow`'s PR-sequence section.

## What this skill does not decide

Whether a given change needs a `docs/roadmap.md` Change Backlog row at all is `docs/roadmap.md`'s own convention to interpret, not this skill's — don't assume every change needs one just because this sequence mentions the backlog.
