## 1. Database provisioning (`docker/postgres-init/`, implementation PR)

- [ ] 1.1 Extend the init SQL so a fresh volume gets six databases: `identity`/`identity_test` (the first already exists as the server's `POSTGRES_DB`), `video`/`video_test`, and `notification`/`notification_test`. Keep the existing file's comment style — it explains *why* the test database is separate, and that reasoning now applies per context rather than once.
- [ ] 1.2 Confirm the script is idempotent enough for its actual contract: `docker-entrypoint-initdb.d` runs once on an empty data directory, so plain `CREATE DATABASE` is correct and `IF NOT EXISTS` is not available for databases anyway. Do not attempt to make it re-runnable — that is what task 3.5's documented recovery path is for.

## 2. Wiring (`docker-compose.yml` and `.github/workflows/ci.yml`, implementation PR)

- [ ] 2.1 `app`: point `IDENTITY_POSTGRES_DSN`/`IDENTITY_POSTGRES_TEST_DSN` at `identity`/`identity_test`, `VIDEO_*` at `video`/`video_test`, `NOTIFICATION_*` at `notification`/`notification_test`. Update the comments that currently explain the values are *intentionally identical* — that rationale is what this change reverses, and leaving it would read as a deliberate choice.
- [ ] 2.2 `app-test`: the same six, kept byte-identical to `app`'s where the file says they mirror each other.
- [ ] 2.3 `worker`: `VIDEO_POSTGRES_DSN` → `video`. `notifier`: `NOTIFICATION_POSTGRES_DSN` → `notification`. Both services' comments claim the value is identical to `app`'s Video/Notification configuration; that stays true and needs no edit beyond the value.
- [ ] 2.4 `.github/workflows/ci.yml`: provision the three test databases against the CI PostgreSQL service and point `IDENTITY_POSTGRES_TEST_DSN`, `VIDEO_POSTGRES_TEST_DSN`, and `NOTIFICATION_POSTGRES_TEST_DSN` at their own. The service is created fresh per run, so this is the step that turns a cross-context query into a red pull request.
- [ ] 2.5 Confirm no variable is added, removed, or renamed anywhere in the diff — only values change. A new variable would mean the code contract moved, which this change explicitly does not do.

## 3. Verification (implementation PR)

No Go module input in the diff, so `development-workflow`'s "A non-build change is exempt from the local test-run requirement" applies. But the suite is run anyway here, because it is the instrument this change exists to install: a green suite against three separate databases is the proof that no cross-context query exists today.

- [ ] 3.1 From a **clean** PostgreSQL volume, `docker compose up -d --build` starts every service, and each of the three `Migrate` calls creates its tables in its own database. Confirm with `\dt` in each of the three runtime databases: five tables total — `identity_users` in `identity`; `video_jobs` and `video_job_outbox` in `video`; `notification_preferences` and `notification_deliveries` in `notification` — none appearing twice.
- [ ] 3.2 `docker compose run --build --rm app-test go test ./... -v` passes in full. Any failure here is a real cross-context dependency this change just exposed — report it as a finding, do not work around it by re-converging a DSN.
- [ ] 3.3 Prove the boundary is live rather than assumed: from the `video` database, a query naming `identity_users` fails as an unknown relation. A separation nobody tried to cross is not yet evidence.
- [ ] 3.4 Run an upload end to end on the separated stack — register, log in, upload, poll to `completed`, download. The three contexts have to cooperate across three databases for that to work, and it is the shortest path that exercises all three.
- [ ] 3.5 Reproduce the existing-volume failure deliberately: start the stack on a volume that predates the change, capture the exact startup error, then run the four `CREATE DATABASE` statements against the live container and confirm the stack recovers. Recover the two test databases in the same pass and re-run `docker compose run --rm app-test go test ./...`, because a recovery that only restores the runtime pair leaves the documented test command broken in a way that surfaces later and looks unrelated. The documented recovery path has to be the one that was actually executed, not the one that seemed likely to work.

## 4. Finalization (separate PR, per repo-workflow)

- [ ] 4.1 Promote this change's two deltas — `ddd-architecture` (`ADDED`) and `development-workflow` (`MODIFIED` on two requirements) — into `openspec/specs/`, then archive the change folder.
- [ ] 4.2 `docs/operations.md`: the environment-variable table and the deployment examples show the three DSNs pointing at one database. Update them, and add the existing-volume recovery statements from task 3.5 where an operator would look for them.
- [ ] 4.3 `docs/development.md`: the exported-variable block for running the binaries directly points all three at `identity`.
- [ ] 4.4 `README.md`: the "Database Schema and Infrastructure Resources" table gains the database each `schema.sql` lands in — the section exists precisely so an evaluator can find this, so it is the wrong place to leave stale.
- [ ] 4.5 `CLAUDE.md`: the notification bullet says the three pools are "one pool per bounded context, whatever single server `docker-compose.yml` happens to point all three at". Still true about the server, now false about the database — a clause, not a rewrite.
- [ ] 4.6 Add this change's `docs/roadmap.md` Change Backlog row, flipped to `archived` with links to the archive folder and the promoted specs.
- [ ] 4.7 Run `npx --yes @fission-ai/openspec validate separate-context-databases --strict --no-interactive` and fix every error before archiving.
