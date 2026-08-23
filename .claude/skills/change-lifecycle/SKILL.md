---
name: change-lifecycle
description: Use only when the user explicitly asks to use OpenSpec, invokes an `/opsx:*` command, or explicitly continues a named active OpenSpec change. Guides the complete OpenSpec lifecycle from explore through proposal, implementation, and archive. Do not trigger it for task lookup, direct implementation, a backlog item, or perceived change complexity alone.
---

# OpenSpec lifecycle (FIAP X video-processor)

This skill is an explicitly selected workflow. It orchestrates when to use the vendored `openspec-{explore,propose,apply-change,archive-change}` skills; it does not replace them.

## The sequence

```
explicit OpenSpec request or named active change
        │
        ▼
orient: new, proposed, implementing, or finalizing?
        │ (new)
        ▼
complex or ambiguous? ──yes──▶ /opsx:explore, discuss
        │ no                         │
        │◀────────────────────────────┘
        ▼
/opsx:propose
        │
        ▼
open the propose PR ──▶ wait for it to merge
        │
        ▼
/opsx:apply
        │
        ▼
open and complete the implementation PR
        │
        ▼
finalize documentation/specs/tasks → /opsx:archive → finalization PR
```

## 1. Orient to the current lifecycle state

Before choosing a command, determine whether the selected change is new, proposed, implementing, or ready for finalization. Check its folder under `openspec/changes/` or `openspec/changes/archive/`, `openspec status --change <name>`, relevant files and task state, git history, and associated PRs. Resume from the next incomplete stage; do not recreate a proposal or repeat merged work. If the repository state and the user's description disagree, report the discrepancy before proceeding.

## 2. Decide whether explore is warranted

Use `/opsx:explore` before proposing when a new opted-in change has cross-cutting impact, a new architectural pattern or dependency, security/performance/migration complexity, or unresolved design questions. A simple, already-scoped opted-in change may proceed directly to `/opsx:propose`.

## 3. Propose, then wait for merge

Create the proposal artifacts with `/opsx:propose`. Open a proposal-only PR and wait until it is actually merged before beginning implementation. Approval alone is insufficient.

## 4. Apply

Use `/opsx:apply` to work through `tasks.md`. When tasks separate implementation and finalization work, do only the implementation-scoped tasks before the implementation PR merges. The vendored apply skill checks off tasks as it works; keep implementation-task checkoffs out of the implementation PR and restore them for finalization.

## 5. PR actions

Whenever this lifecycle asks for a PR to be opened, updated, handed off, or merged, invoke `repo-workflow` for that PR's quality and merge requirements.

## 6. Finalize

After the implementation PR merges:

1. Check off completed tasks.
2. Update permanent documentation and agent instructions that reflect the shipped change.
3. Update `docs/roadmap.md` only if the opted-in change has a roadmap row.
4. Run `npx --yes @fission-ai/openspec validate <change-id> --strict --no-interactive` and fix every validation error.
5. Only after strict validation passes, run `/opsx:archive` to promote delta specs and archive the change folder.
6. Open one finalization PR containing those closure artifacts and no application code or tests.
