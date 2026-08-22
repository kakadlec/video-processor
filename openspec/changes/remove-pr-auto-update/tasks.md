## 1. Implementation

- [ ] 1.1 Delete `.github/workflows/auto-update-pr-branches.yml`.
- [ ] 1.2 Set `main` branch protection's required-status-check strictness to `false`, retaining PR-only merging, the three required checks, administrator enforcement, and required-conversation resolution.

## 2. Verification

- [ ] 2.1 Confirm the auto-update workflow is absent from the repository workflow list.
- [ ] 2.2 Confirm the branch-protection API reports `required_status_checks.strict` as `false` and retains the three required checks.
