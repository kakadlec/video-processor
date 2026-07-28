## Context

Every change so far has been a direct push to `main` (by design, per `add-ci-testing-and-sast`'s explicit non-goal: "no PR-based workflow yet"). The user is now deliberately reversing that. `SAST (gosec)` is currently a required-to-be-green check that's actually red (9 known findings), and the user explicitly chose to require it anyway, accepting that `main` becomes unmergeable until those are triaged.

## Goals / Non-Goals

**Goals:**
- No commit reaches `main` except through a merged PR.
- A PR can only be merged once `Build & Test` and `SAST (gosec)` both pass.
- No bypass — including for the repo admin (the user) and for automated PRs (`release-please`'s own release PR is subject to the same gate).

**Non-Goals:**
- Required review approvals — solo repo, nothing to gain from requiring a second human sign-off on your own PR. Only status checks are required.
- Fixing the 9 `gosec` findings — explicitly out of scope here, per the user's explicit choice to accept the resulting merge freeze rather than fix them as part of this change.
- Changing what CI checks exist — reuses `Build & Test` and `SAST (gosec)` from `add-ci-testing-and-sast` as-is.

## Decisions

**Enable branch protection as the *last* step, after this change's own files are already on `main`.**
Chicken-and-egg problem: if protection were enabled before this change's own commits land, this change's own PR couldn't be merged through the protected path either (SAST is red), forcing an admin bypass on the very first PR — which would undermine the "no bypass" policy from minute one. Instead, this change's commits land via a normal push (the last one that's allowed to), and enabling protection is the final act — from that point forward, literally the next change (including any future work in this repo) must go through a PR.

**`enforce_admins: true` (no bypass for the repo owner).**
The user asked for *all* changes to require a PR — that only means something if it also applies to the admin account doing the pushing (otherwise the rule is advisory, not enforced). Verified this is what `enforce_admins` controls: without it, GitHub lets repo admins push directly despite protection being "on."

**`required_approving_review_count: 0` (PR required, no approval required).**
There's no second person on this repo to approve anything; requiring an approval that only the same person could ever give doesn't add a real gate, just friction. The PR-required + status-checks-required combination is the actual protection being asked for.

**`strict: true` (require branches to be up to date with `main` before merging).**
Without this, a PR could show green checks computed against a stale base and still get merged into a `main` that's since changed underneath it. Standard practice once you have required status checks at all.

**Required checks list: `Build & Test`, `SAST (gosec)` — not `release-please`.**
`release-please`'s own workflow job isn't a quality gate on the code; it's the release-automation bookkeeping itself. Requiring it as a merge gate wouldn't make sense (a PR with no version-relevant commits has nothing for it to check).

**Going forward, changes are made on a feature branch and opened as a PR (`gh pr create`), not pushed to `main` directly.** Documented in `CLAUDE.md` as an operating instruction for whoever (human or Claude Code) makes the next change.

## Risks / Trade-offs

- [`main` is unmergeable until the 9 `gosec` findings are triaged] → Explicit, informed user decision; tracked, not silently bypassed. The next piece of work in this repo is likely triaging those findings, precisely because nothing else can land until then.
- [`release-please`'s automated release PR is now also blocked from merging until SAST is green] → Same as above — it's just another PR against `main`, correctly subject to the same rule.
- [Solo repo + no bypass means if the account doing the work ever loses GitHub access to merge (e.g., automation running as a bot without merge rights), everything stalls] → Not a concern right now (single human account with full repo access); worth revisiting only if collaborators or additional automation are added later.

## Migration Plan

1. Land this change's own files (`CLAUDE.md`, OpenSpec artifacts) via a normal push — the last one allowed before protection is active.
2. Enable branch protection on `main` via the GitHub API (`enforce_admins: true`, required status checks `Build & Test` + `SAST (gosec)`, `required_approving_review_count: 0`, `strict: true`).
3. Verify by attempting to push directly to `main` again and confirming it's rejected.
4. From here on, every change (including any future gosec-finding triage) goes through a feature branch + PR.
