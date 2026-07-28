## 1. Documentation

- [x] 1.1 Update `CLAUDE.md`: document that changes go through a feature branch + PR (`gh pr create`) from now on, never a direct push to `main`; merge requires `Build & Test` and `SAST (gosec)` both green.

## 2. Land this change

- [x] 2.1 Commit and push this change's own files via a normal push (the last one allowed before protection is active).

## 3. Enable branch protection

- [ ] 3.1 Enable branch protection on `main` via the GitHub API: require PR before merging, required status checks `Build & Test` + `SAST (gosec)` (strict/up-to-date), `enforce_admins: true`, `required_approving_review_count: 0`.
- [ ] 3.2 Verify by attempting a direct push to `main` and confirming GitHub rejects it.
