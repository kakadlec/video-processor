## Why

The repository's always-loaded guidance currently classifies most work as mandatory OpenSpec work. That makes an explicit request for a direct implementation conflict with repository instructions, even when the developer deliberately chose not to use a spec-driven methodology. OpenSpec should remain available for developers who opt into it, while code-quality and pull-request safeguards continue to apply consistently.

## What Changes

- Remove mandatory OpenSpec classification and lifecycle requirements from always-loaded agent guidance and general development documentation.
- Make the OpenSpec lifecycle and its three-PR sequencing apply only after a developer explicitly requests OpenSpec or invokes an OpenSpec command.
- Narrow `change-lifecycle` activation to explicit OpenSpec requests, commands, or work already inside an OpenSpec change.
- Refocus `repo-workflow` on pull-request quality: it must activate when a developer asks for a PR or when the agent creates one, without activating or requiring OpenSpec.
- Retain applicable local tests, CI security gates, review checks, branch protection, and explicit merge authorization independently of methodology.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `development-workflow`: make OpenSpec optional and explicitly scoped to opted-in changes; preserve methodology-independent quality and PR safeguards.

## Impact

Affected guidance and configuration: `AGENTS.md`, `CLAUDE.md`, `docs/development.md`, `.claude/skills/repo-workflow/SKILL.md`, `.claude/skills/change-lifecycle/SKILL.md`, and the `development-workflow` canonical specification. No application API, runtime behavior, or dependencies change.
