## Context

Always-loaded repository guidance currently treats OpenSpec as the default for any work outside a narrowly defined trivial-edit exemption. The same policy is duplicated in the canonical workflow specification, general development documentation, and two auto-triggered skills. The result is that a developer's explicit choice to implement directly is overridden before the OpenSpec-specific tooling is even relevant.

The repository must continue to protect code quality: Go-input changes need a passing local test run before completion, and pull requests remain subject to branch protection, CI security gates, review resolution, and explicit merge authorization.

## Goals / Non-Goals

**Goals:**

- Make OpenSpec an explicit developer choice rather than a classification the agent infers from change size or type.
- Keep the full OpenSpec lifecycle available, including its propose, implementation, and finalization PR separation, once selected.
- Ensure `change-lifecycle` activates only for explicit OpenSpec intent or an existing OpenSpec change.
- Ensure `repo-workflow` activates for every PR the agent is asked to create or creates itself, while remaining independent of OpenSpec.
- Keep quality and security requirements applicable regardless of methodology.

**Non-Goals:**

- Change application behavior, dependencies, CI jobs, branch protection, or merge authorization.
- Remove OpenSpec tooling or relax its lifecycle after a developer has opted into it.
- Require OpenSpec, a PR, or a particular planning methodology for local/direct development.

## Decisions

### 1. Explicit intent is the sole OpenSpec activation mechanism

The lifecycle activates only when the developer explicitly requests OpenSpec or an `/opsx:*` action, or when work is explicitly continuing a named active OpenSpec change. Terms such as "non-trivial", a backlog item, a task, or a request to implement something do not activate it.

This preserves developer choice and prevents an agent from reclassifying a direct request into a mandatory process.

### 2. Quality constraints are separated from methodology

Always-loaded guidance will retain only universal quality requirements and concise pointers. Tests remain mandatory before reporting a Go-module-input change complete. PR-specific checks remain mandatory when a PR exists. OpenSpec-specific material moves behind the explicit activation boundary.

The alternative—keeping the OpenSpec default with a broader list of exemptions—still leaves agents to infer whether a request qualifies and recreates the refusal problem.

### 3. PR workflow is event-based

`repo-workflow` triggers when the developer requests a PR lifecycle action or when the agent decides to create a PR. It validates the PR's functional state, applicable tests, CI checks, review state, branch freshness, and merge authorization. It neither starts nor requires OpenSpec.

This covers direct work that is ultimately delivered as a PR without constraining work that never enters a PR workflow.

### 4. Canonical OpenSpec requirements are scoped, not deleted

The `development-workflow` specification will state that OpenSpec lifecycle requirements apply only after explicit opt-in. Its OpenSpec sequencing and PR-role requirements remain normative within that scope. Methodology-independent requirements (tests, branch protection, CI, merge authorization) remain universal.

This avoids losing the documented OpenSpec process while removing its unintended global force.

## Risks / Trade-offs

- **[Risk] A developer may bypass useful planning for a complex direct change.** → Mitigation: OpenSpec remains available by explicit request; the repository does not infer process choice.
- **[Risk] Duplicated guidance drifts again.** → Mitigation: update all current policy entry points together and make each document's scope explicit.
- **[Risk] A PR is opened without workflow checks.** → Mitigation: make both requested and agent-initiated PR creation an explicit `repo-workflow` trigger.

## Migration Plan

1. Update the OpenSpec proposal artifacts and obtain approval through the currently required propose PR.
2. After that PR merges, update the designated agent guidance, documentation, skills, and canonical spec on a separate implementation PR.
3. After the implementation PR merges, finalize and archive the OpenSpec change under the opted-in lifecycle.

No runtime migration or rollback procedure is needed; the policy can be reverted through a normal PR if required.
