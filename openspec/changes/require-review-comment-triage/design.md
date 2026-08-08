## Context

`kakadlec/video-processor` uses OpenSpec-driven development with AI coding agents (currently Claude Code) doing most of the implementation. A repository ruleset (`copilot_code_review` rule type, added 2026-08-08) now auto-requests a GitHub Copilot review the first time each pull request opens, with `review_on_push` off — Copilot does not re-review later pushes to an already-reviewed PR. Review comments have so far only been checked when the user explicitly asked in-session; there is no repo-visible rule requiring it, only a private memory scoped to one tool.

## Goals / Non-Goals

**Goals:**
- Make "check PR review comments and triage them before calling a PR-related task done" a rule any agent working in this repo can discover from the repo itself.
- Capture the Copilot auto-review ruleset's actual behavior (first-open only, no re-review on push) so agents don't assume a second push will trigger a fresh automated pass.

**Non-Goals:**
- Configuring or changing the Copilot review ruleset itself (already done directly via `gh api`, outside this change).
- Mandating that every suggested comment be applied verbatim — triage means evaluating each on its merits, same as any other review feedback in this project's existing quality-gate mindset.
- Any change to application code, CI, or the video-processing service's behavior.

## Decisions

- **Where to document it: `AGENTS.md`, not `CLAUDE.md`.** `AGENTS.md` is the repo's tool-agnostic governance file (already used for the PR-sequence and merge-authorization rules); `CLAUDE.md` is scoped to Claude Code specifically. A rule meant to bind "any LLM/agent" belongs in the file already serving that role, alongside the existing "Merge rule" section it will sit next to.
- **Track it as a `development-workflow` requirement, not skip OpenSpec.** This repo's own precedent (e.g. the PR-separation policy, the ffmpeg test-gate requirement) runs process/governance changes through the full propose → apply → archive cycle and records them as canonical requirements under `openspec/specs/development-workflow/`, even when no application code changes. Treating this the same way keeps `AGENTS.md` traceable to a requirement instead of drifting from ad hoc edits.
- **State the triage rule generically (any review source), not Copilot-specific.** The immediate trigger is Copilot's automatic review, but the rule as written applies equally to a human reviewer's comments — narrower Copilot-only wording would need revisiting the day a human reviewer is added or the ruleset changes.

## Risks / Trade-offs

- **Risk:** An agent checks for comments immediately after opening a PR, before Copilot's review has posted, and concludes there's nothing to address. → **Mitigation:** Document that Copilot review can take a short while to appear; a task shouldn't be marked done on the assumption that an immediate empty check means no comments will come.
- **Risk:** Because `review_on_push` is off, an agent might assume later fix-up commits get automatically re-reviewed. → **Mitigation:** `AGENTS.md` explicitly states re-review does not happen automatically on push, and that a manual re-request is needed if a fresh pass is wanted after substantial follow-up changes.
