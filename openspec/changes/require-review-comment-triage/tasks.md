## 1. AGENTS.md documentation

- [ ] 1.1 Add a "Pull request review comments" section to `AGENTS.md` requiring that review comments (automatic and human) be checked and triaged before a PR-related task is reported complete, with genuine findings fixed and their threads resolved, and non-applicable findings explained rather than silently ignored.
- [ ] 1.2 Document in the same section that this repository has a `copilot_code_review` branch ruleset that auto-reviews each PR once on first open, with `review_on_push` disabled — no automatic re-review on later pushes — and that a fresh review must be requested manually if needed after substantial follow-up changes.

## 2. Verification

- [ ] 2.1 Confirm `AGENTS.md` reads correctly alongside the existing "Merge rule" and PR-sequence sections without duplicating or contradicting them.
- [ ] 2.2 Confirm no application code, tests, or CI configuration were touched — this change is documentation-only.
