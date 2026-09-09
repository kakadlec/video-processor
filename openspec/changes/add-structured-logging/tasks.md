## 1. Inventory before touching anything

- [ ] 1.1 Enumerate every non-test call site to be migrated and commit the list to the PR description: `log.Print*` (151), `fmt.Print*` (4, in `cmd/identity-api/main.go`, `cmd/notification-api/main.go`, and two pt-BR banners in `cmd/video-api/main.go`), and `log.Fatal*` (13). The count is the completion check for groups 2–4; without it "all of them" is unverifiable.
- [ ] 1.2 Enumerate every test that reads log output. Three are known — `cmd/worker/worker_test.go` and `internal/video/infrastructure/messaging/relay_test.go` (both via `log.SetOutput`) and `internal/notification/application/deliver_notification_test.go` (via the injected `*log.Logger`). Confirm by search that these are the only three; a missed one fails at run time, not at build time.
- [ ] 1.3 Confirm the premise of decision 8 before acting on it: search the repository for `gin.SetMode` and `GIN_MODE` and record that neither exists, so all three HTTP services do currently run in debug mode. If either turns up, the release-mode task changes shape.

## 2. The shared package and its enforcement tests

- [ ] 2.1 Create `internal/platform/logging`: JSON handler construction, severity parsing, destination (stdout), and the service-identity binding. No gin import, no bounded-context import.
- [ ] 2.2 Severity parsing refuses an unparseable value with an error rather than falling back to the default, and the absent case yields informational. Cover both.
- [ ] 2.3 Copy the placement test `internal/platform/rabbitmq/dependency_test.go` carries into this package. It is per-directory — it parses `.` — so a new platform package is unenforced without its own copy, and this one is exactly where an `internal/video/...` import would be tempting.
- [ ] 2.4 Add the disclosure test: AST-walk every non-test `.go` file under `cmd/` and `internal/`, and fail on any argument a log call passes after its message that is not a call to `slog.String`, `slog.Int`, `slog.Int64`, `slog.Bool`, `slog.Duration` or `slog.Time`. This rejects `slog.Any`, `slog.Value`, `LogValuer` implementations, and the alternating key-and-value form — but **not** the ordinary `Info`/`Warn`/`Error` functions, which take typed attributes in their variadic. `slog.LogAttrs` is permitted, not required. It fails at this point, which is the point — it goes red before the migration and green after.
- [ ] 2.4a Extend the same walk to hold the other source-level scenario: no call to `log.Print*`, `log.Fatal*` or `log.Panic*`, and no `fmt.Print`/`Printf`/`Println` or `fmt.Fprint*` writing to standard output or standard error. `fmt.Errorf` and `fmt.Sprintf` produce values and are untouched. One walk, two rules — this is what makes group 4's completion checkable rather than asserted.
- [ ] 2.4b The walk must skip `ENOENT` rather than returning `WalkDir`'s callback error verbatim. `go test ./...` runs packages in parallel and `internal/video/infrastructure/ffmpeg`'s tests create and delete `temp/<jobID>` under their own package directory, which is inside the tree this walker reads — the exact flake fixed in #251 for the two existing walkers. Make it fail loudly if it parses no file at all, so skipping cannot turn into passing vacuously.
- [ ] 2.5 Verify the disclosure test actually catches the case it exists for: temporarily add a call site passing a `NotificationPreference`, confirm the test names the file and line, then remove it. A source-level test that has never been seen failing is a test of nothing.
- [ ] 2.6 Add a test asserting the JSON handler's output shape for one record — timestamp, level, message, service — so the shape the rest of the change assumes is pinned once rather than in every package.

## 3. The `internal/` packages

Migrate one package per commit, converting its tests in the same commit, so no intermediate state has a package logging through two mechanisms.

- [ ] 3.1 `internal/video/application`.
- [ ] 3.2 `internal/video/infrastructure/cache` — its three `video: cache: ...` prefixes become fields.
- [ ] 3.3 `internal/video/infrastructure/messaging`, and convert `relay_test.go`'s `log.SetOutput` capture to read the structured record. Match on an attribute, not on a message substring — that is what makes the assertion survive the next rewording.
- [ ] 3.4 `internal/notification/application`. Change `NewDeliverNotification`'s `logger *log.Logger` parameter to `*slog.Logger`, keeping nil-means-default. Convert `deliver_notification_test.go`'s two capture sites.
- [ ] 3.5 `internal/notification/infrastructure/messaging`.
- [ ] 3.6 After each: confirm no `"log"` import remains in that package's non-test sources.

## 4. The composition roots

- [ ] 4.1 `cmd/identity-api`, `cmd/video-api`, `cmd/notification-api`, `cmd/worker`, `cmd/notifier`: at the top of `main`, in this order — parse `LOG_LEVEL`, build the logger from `internal/platform/logging` with the service identity bound, `slog.SetDefault`, then read the rest of the configuration. Every other configuration failure is then reportable as a structured record; a `LOG_LEVEL` that will not parse is the single exception and goes to standard error before exiting.
- [ ] 4.2 Convert the four `fmt.Println` banners to records. They become English: the language policy makes a rewritten string English, and these are the subject of the change rather than a nearby line. The pt-BR JSON `Message`/`error` values in `cmd/video-api/video.go` are response bodies and are not touched by any task here.
- [ ] 4.3 Convert the 13 `log.Fatal*` sites to an error-severity record followed by an explicit `os.Exit(1)`. Check each one for a deferred function that `log.Fatal` was already skipping and note any in the PR description — this change does not fix them, but it makes them visible.
- [ ] 4.4 Convert the remaining `log.Print*` sites in each root. The hierarchical prefixes (`video: worker: sweep: ...`, `notification: notifier: ...`) become fields; the message becomes the fixed remainder.
- [ ] 4.5 `cmd/worker`: convert `worker_test.go`'s `log.SetOutput` capture. Mark any capture-based test as not parallel-safe — the process-wide default is shared state, exactly as `log.SetOutput` was.
- [ ] 4.6 Confirm the group is complete by running the 2.4a walk, not by reading imports: it goes green here, and the count it reports matches the 1.1 inventory.

## 5. The HTTP middleware

- [ ] 5.1 Add `logging.go` to each of `cmd/identity-api`, `cmd/video-api`, `cmd/notification-api`: the gin access-log middleware and the recovery handler. Three copies, deliberately — `cmd/identity-api` is in scope here although it mounts neither auth nor the limiter.
- [ ] 5.2 The access record carries method, matched route, path, status, duration, size, and the authenticated subject where one exists. It carries no query string, no header, and no body. Add a test per service that sends a request with a query string and asserts no part of it reaches the record.
- [ ] 5.3 Replace `gin.Default()` with `gin.New()` plus that middleware and `gin.CustomRecovery`, in all three roots. Assert the recovered-panic response is byte-identical to what the service returned before — the record changes, the response does not.
- [ ] 5.4 Set `gin.SetMode(gin.ReleaseMode)` explicitly. Then run the full suite: release mode changes some of gin's own error rendering, and any test asserting on it fails here rather than in production.
- [ ] 5.5 Give each copy its own behavioural tests, which is what the existing auth and rate-limit copies actually carry — they have no cross-copy drift test, and claiming that precedent would be wrong. Each root's tests drive its own middleware through `httptest`, so a copy that drifts fails in its own package.

## 6. Quality gates

- [ ] 6.1 `go vet ./...` clean, and `go build` all five binaries.
- [ ] 6.2 `docker compose run --build --rm app-test go test ./... -v` passes in full. The diff carries Go module inputs, so this is the change's gate and not a formality. `--build` is not optional.
- [ ] 6.3 Confirm `go.mod` and `go.sum` are unchanged — the change adds no dependency, and a diff in either means something other than `log/slog` was reached for.
- [ ] 6.4 `gosec ./...` and `govulncheck ./...` clean.
- [ ] 6.5 Start the stack with `docker compose up --build`, run one upload end to end, and read the collected output: confirm every record is JSON, that a worker replica is identifiable, and that the job id can be selected on as a field across video-api, worker and notifier. This is the only check that the change achieved its purpose rather than merely compiling.
- [ ] 6.6 `git diff --check`, and confirm the four required checks are green on the PR.

## 7. Finalization (after the implementation PR merges — not part of it)

- [ ] 7.1 Check off the implementation tasks above.
- [ ] 7.2 Update `docs/operations.md`: replace the "Observability — Planned (Phase 8)" section's logging sentence with what shipped, and document `LOG_LEVEL` alongside the other optional variables. Remove the stale claim in that same section that Phase 8 also carries `docker-compose.yml` — the roadmap already records that as delivered.
- [ ] 7.3 Update `docs/architecture.md` and `docs/flows.md:201`, which says Phase 8 "is next and is not yet decomposed".
- [ ] 7.4 Update `CLAUDE.md`: how a process logs, where the logger is built, the no-arbitrary-value rule and the test that holds it, and that `gin.New()` plus explicit release mode replaced `gin.Default()`.
- [ ] 7.5 Add all three Phase 8 rows to `docs/roadmap.md`'s Change Backlog — this one archived with links, `add-health-and-readiness-endpoints` and `add-prometheus-metrics` `not-started` — and replace the "### Phase 8 — not yet decomposed" section, which becomes false here. Update the Phase Summary row and the "Current State" heading accordingly.
- [ ] 7.6 `npx --yes @fission-ai/openspec validate add-structured-logging --strict --no-interactive`, fix every error, then archive.
