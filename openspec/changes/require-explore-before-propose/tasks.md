## 1. Implementation

- [ ] 1.1 `CLAUDE.md`: update the "Workflow:" bullet under "Development process: OpenSpec is mandatory" to describe `/opsx:explore` → `/opsx:propose` → `/opsx:apply` → `/opsx:archive` for complex/ambiguous changes, keeping direct `/opsx:propose` available for simple, already-scoped ones.
- [ ] 1.2 `CLAUDE.md`: add the simple-vs-complex criterion (reusing the existing `design.md`-inclusion criteria: cross-cutting impact, new architectural pattern/dependency, security/performance/migration complexity, or open design questions) as its own bullet, distinct from — and layered on top of — the existing trivial-edit exemption that skips OpenSpec entirely.
- [ ] 1.3 `docs/roadmap.md`: update the "How to use this" bullet under Change Backlog ("Before running `/opsx:propose`, pick the next `not-started` row...") to note that complex/ambiguous rows get `/opsx:explore` first, while simple, already-scoped rows may go straight to `/opsx:propose`.

## 2. Verification

- [ ] 2.1 Re-read both edited sections end to end to confirm the two exemptions (trivial-edit skips OpenSpec entirely; simple-but-OpenSpec-worthy skips only explore) read as clearly distinct, not merged into one.
- [ ] 2.2 Grep `CLAUDE.md` and `docs/roadmap.md` for `propose` to confirm no other passage describes the workflow's starting point in a way that now contradicts the explore-for-complex-changes rule.
