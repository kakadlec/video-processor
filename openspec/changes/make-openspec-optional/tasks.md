## 1. Implementation PR — Claude Code skill configuration

- [ ] 1.1 Update `.claude/skills/change-lifecycle/SKILL.md` so it activates only for an explicit OpenSpec request, `/opsx:*` command, or named active OpenSpec change; preserve the complete lifecycle once activated.
- [ ] 1.2 Update `.claude/skills/repo-workflow/SKILL.md` to activate for user-requested and agent-created PRs, retain PR quality gates, and remove all OpenSpec lifecycle, role-separation, and validation requirements.
- [ ] 1.3 Verify the two skill trigger descriptions do not classify direct work, task lookup, task implementation, or change complexity as an implicit OpenSpec request.

## 2. Finalization PR — permanent guidance and canonical specification

- [ ] 2.1 Update `AGENTS.md` to contain only universally applicable quality, security, test, PR, and merge constraints; remove mandatory OpenSpec classification and any instruction to consult OpenSpec for ordinary work.
- [ ] 2.2 Update `CLAUDE.md` to retain project context and universal quality requirements while presenting OpenSpec only as an explicitly selected workflow.
- [ ] 2.3 Update `docs/development.md` to separate universal quality/PR guidance from optional OpenSpec guidance and remove mandatory OpenSpec/three-PR language for direct work.
- [ ] 2.4 Promote the `development-workflow` delta so OpenSpec lifecycle requirements are explicitly opt-in and PR-quality skill triggers cover both requested and agent-created PRs.
- [ ] 2.5 Confirm no `docs/roadmap.md` Change Backlog row is added because this is a workflow/process-only OpenSpec change.
- [ ] 2.6 Check off completed tasks, archive this change, and keep all finalization artifacts in one PR with no application code or tests.

## 3. Validation

- [ ] 3.1 Run `git diff --check` for each PR before handoff.
- [ ] 3.2 Run `npx --yes @fission-ai/openspec validate make-openspec-optional --strict --no-interactive` before archive.
- [ ] 3.3 Confirm the final agent guidance, development documentation, skills, and promoted specification agree that OpenSpec is explicit opt-in and PR quality is methodology-independent.
