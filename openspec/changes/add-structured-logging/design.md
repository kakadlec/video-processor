## Context

151 `log.Print*` call sites outside tests, four `fmt.Println` startup banners, and 13 `log.Fatal` sites, spread over five composition roots and six `internal/` packages. All of them write prose to the standard library's package-level logger. `proposal.md` states why that is a problem; this document settles how it is replaced.

Four properties of the existing code constrain the answer, and each was checked rather than assumed:

- **No package under `internal/` imports gin.** The HTTP framework lives only in `cmd/`. `ddd-architecture` forbids domain and application layers from importing one; the rest is convention, and it is unbroken.
- **`package main` cannot import `package main`.** The auth and rate-limit middlewares are already one thin copy per HTTP root over a shared, framework-free core in `internal/platform/`. That is the established shape for anything a router mounts.
- **The codebase already has exactly one injected logger, and it uses a nil-means-default fallback.** `application.NewDeliverNotification` takes a `*log.Logger` and substitutes `log.Default()` when it is nil, so its tests can capture output. Every other package uses the global directly. The arrangement is "global by default, injected where a test needs to read it" — not an accident to be corrected, and not a pattern to generalize either.
- **Three test files read log output today**: `cmd/worker/worker_test.go` (via `log.SetOutput`), `internal/video/infrastructure/messaging/relay_test.go` (same), and `internal/notification/application/deliver_notification_test.go` (via the injected logger). These are the assertions the migration can break without a compile error.

## Goals / Non-Goals

**Goals:**

- One machine-readable record format, emitted by all five processes on one stream.
- Every record identifies the process that emitted it, so three worker replicas are distinguishable.
- The positional information currently encoded in message prefixes (`video: worker: sweep: ...`) becomes fields.
- The HTTP access log and the panic log have the same shape as every other record.
- Passing a domain value to a log call becomes structurally impossible, not merely discouraged.
- No new module dependency.

**Non-Goals:**

- Changing *what* is logged. No record is added or removed; the survey found no line that should not exist.
- Request correlation or trace identifiers. Genuinely useful, genuinely a separate design (it needs a propagation decision across an HTTP hop, a broker hop, and an outbox row), and not required to make the existing records readable.
- Log shipping, aggregation, retention, or any collector. The stream stays stderr-or-stdout under the container runtime.
- `/health`, `/ready`, `/metrics`, the admin listener. Changes 2 and 3 of Phase 8.
- Converting the pt-BR JSON `Message`/`error` values in `cmd/video-api/video.go`. Those are response bodies; this change touches no response.

## Decisions

### 1. `log/slog` with a JSON handler, not zerolog

`docs/operations.md` sanctions either. slog is chosen because it is in the standard library: nothing enters `go.mod`, nothing enters the `govulncheck` surface, and nothing has to be justified to `gosec`. The performance argument that usually favours zerolog does not apply to a system whose logging volume is one record per job phase and one per HTTP request.

*Alternative considered — zerolog:* better ergonomics for chained fields and a faster encoder. Rejected: a dependency added to make 151 already-written call sites marginally nicer to read is a poor trade in a repository that pins its architecture with tests and treats every dependency as surface.

### 2. JSON always, in every environment, with no format switch

There is no `LOG_FORMAT` variable and no text handler.

The reason is not taste. The non-disclosure rule below has to be verified against a handler, and the two handlers reach a value by different paths — a text handler consults `fmt`, where `Secret`'s defences live, and a JSON handler consults `encoding/json`, where `Secret.MarshalJSON` deliberately returns an error that would kill the whole record. Shipping both means the redaction property is verified twice or, more likely, once. One format means one verification.

*Alternative considered — text in development, JSON in production:* the usual arrangement, and the usual consequence is that the format a developer reads all day is not the format that is verified. Rejected on that ground.

### 3. `slog.SetDefault` per composition root; `internal/` packages use the default; the one existing injection point keeps injecting

Each `main` builds a logger with the process's identity already bound (`slog.Default().With("service", "worker")`, and so on) and installs it with `slog.SetDefault`. Every `internal/` package then logs through `slog.Default()` and inherits the service attribute without being told what process it is running in.

This is the current arrangement translated, not a new one. It also happens to be the arrangement that serves the service-identity goal best: with injection, every package would need the logger threaded to it before it could name its own process, and a package that missed the thread would emit unattributed records.

`application.NewDeliverNotification`'s `*log.Logger` parameter becomes a `*slog.Logger` with the same nil-means-default semantics. It is the only injection point and it stays one, because its tests are the reason it exists.

*Alternative considered — inject a `*slog.Logger` into every constructor:* purer, and testable without touching a global. Rejected: it changes six packages' constructor signatures and every test that builds one, for a change whose subject is the shape of a record. The cost is real and named in Risks.

### 4. A gin-free `internal/platform/logging`, with a thin gin middleware copied into each of the three HTTP roots

`internal/platform/logging` owns handler construction, level parsing, and the attribute helpers. It imports no HTTP framework and no bounded context — it ships its own copy of the placement test `internal/platform/rabbitmq` already carries, because that test is per-directory and the rule is otherwise unenforced for a new platform package.

Each of `cmd/identity-api`, `cmd/video-api` and `cmd/notification-api` gets its own `logging.go` holding the gin middleware and the recovery handler. Three copies, not two: `cmd/identity-api` mounts no auth and no limiter but does serve requests, so it is in scope for this middleware where it is out of scope for the other pair.

*Alternative considered — put the gin middleware in `internal/platform/logging`:* one copy instead of three, and nothing in `ddd-architecture` forbids it, since the prohibition on importing an HTTP framework binds the domain and application layers. Rejected because no package under `internal/` imports gin today and this is a poor first exception: the middleware is twenty lines of framework glue, which is exactly the shape the existing two-copy precedent already accepted.

### 5. The access log records the matched route and the raw path, never the query string

The middleware records method, gin's matched route template, the request path, status, latency, response size, and the authenticated subject where the request carries one. It does **not** record the query string, any header, or any body.

A path segment in this system is a storage key at worst, and `docs/operations.md` already instructs that the `StorageKey` be logged. A query string is unbounded caller-supplied text, and this repository has one documented case of a credential legitimately living in one — a webhook destination's query — which is exactly why `notification-webhook-delivery` forbids logging a `*url.Error`. Excluding it costs nothing and closes the class.

### 6. No `slog.Any`, anywhere — attributes are built only from typed scalar constructors, pinned by AST

This is the mechanism behind the non-disclosure rule, and it is stronger than redaction.

`domain.Secret` defends itself through `fmt`: `String`, `GoString` and `Format` render `notification.Secret{REDACTED}`, and `MarshalJSON` returns an error so that a response struct holding one fails at encode time rather than leaking. Under a JSON handler that deliberate error does not redact a field — it discards the record. And the structs that hold a `Secret` as a plain field (`PreferenceIntent`, `NotificationPreference`) are protected today only by the fact that `fmt` cannot call methods on a field; `encoding/json` reaches fields directly.

Rather than teach the handler about these types, the change removes the way they could arrive. The rule, stated as the AST check enforces it: **every argument a log call passes after its message is a call to `slog.String`, `slog.Int`, `slog.Int64`, `slog.Bool`, `slog.Duration` or `slog.Time`.** That forbids `slog.Any`, `slog.Value` and `LogValuer`, and it equally forbids `slog`'s loosely-typed alternating form — `slog.Info("msg", "job_id", id)` — whose value position takes any type and is therefore the identical hole. It does **not** require `slog.LogAttrs`: the ordinary `Info`/`Warn`/`Error` calls accept `slog.Attr` values in their variadic directly, so `slog.Info("job completed", slog.String("job_id", id), slog.Int("frames", n))` satisfies the rule. This matters at 151 call sites — the constraint is on the arguments, not on which function is called, so no site pays the `LogAttrs` ceremony. A test in `internal/platform/logging` AST-walks `cmd/` and `internal/` and fails on any argument that is not one of those calls — the same source-level idiom as `TestNoQueryOutsideFindDeliverableSelectsTheSecret`, `TestTheHTTPCompositionRootDoesNotLoadTheSecret`, and `TestOnlyTheIdentityServiceConstructsATokenIssuer`, the last of which already scans other roots' sources from a test in one of them.

What this does **not** cover, stated so it is not assumed: an error's own text. `slog.String("error", err.Error())` is permitted and necessary, and a raw transport error passed there would still render a `*url.Error`'s full URL. That rule is separately specified (`notification-webhook-delivery`, `notification-email-delivery`) and separately enforced by behavioural tests over the delivery path, which this change leaves in place. The AST check closes the domain-value half; the existing tests close the transport-error half.

### 7. `LOG_LEVEL`, optional, per service, default `info`

One optional variable per process, parsed by `internal/platform/logging`, defaulting to `info`, with an unparseable value refused at startup rather than silently downgraded.

This is the one piece of configuration read *before* the logger exists, which fixes the startup order in every root: parse the level, build the logger, `slog.SetDefault`, then read everything else. A failure parsing the level is therefore the one startup failure that cannot be reported as a structured record, and is reported on standard error before exiting.

Unlike `RATE_LIMIT_MAX_REQUESTS` and `RATE_LIMIT_WINDOW_SECONDS` — which CLAUDE.md requires to hold the same value everywhere, because the counter is one shared budget keyed on the user — a log level is genuinely per-process state with no shared invariant. Turning up the notifier's verbosity while leaving the API alone is the ordinary use, not a misconfiguration. `docker-compose.yml` leaves it unset.

### 8. `gin.New()` plus explicit release mode

`gin.Default()` is Logger and Recovery; both are replaced. `gin.SetMode(gin.ReleaseMode)` is set explicitly, which the repository does nowhere today — so every service currently runs in debug mode, including in the published image. Recovery becomes `gin.CustomRecovery` logging a record at `error` with the panic value rendered as a string and the stack as a string, then the existing `500`.

### 9. Records go to stdout

Today's output is stderr, via the `log` package's default. All records move to stdout, matching the gateway's own split (`access_log /dev/stdout`) and the convention that stdout carries the application's output while stderr carries the runtime's. Under Docker both are captured, so nothing observable changes locally; it matters to a collector that distinguishes them.

## Risks / Trade-offs

- **A test asserting on a message string breaks at run time, not build time** → The three files are already identified (`cmd/worker/worker_test.go`, `internal/video/infrastructure/messaging/relay_test.go`, `internal/notification/application/deliver_notification_test.go`). `tasks.md` converts each to read the structured record — matching on an attribute rather than a substring, which is what makes the assertion stable against the next rewording.
- **The AST check can be satisfied and still leak, if a call site extracts the wrong scalar** → `slog.String("secret", s.Reveal())` passes the check. Nothing structural stops it, and nothing structural stopped it before either. What the check buys is that leaking now requires naming `Reveal()` at a log call site, which is a deliberate act visible in review, rather than passing a struct that happens to contain one.
- **The process-wide default is shared state in tests** → A test that installs its own default must not run in parallel with one that reads the default's output. This is the same constraint `log.SetOutput` imposes today, in the same two files, so the risk is unchanged rather than introduced. Any new capture-based test is marked as not parallel-safe.
- **`slog` has no `Fatal`, so 13 sites become log-then-`os.Exit(1)`** → Deliberate: the exit becomes visible at the call site rather than hidden inside a helper. The trade is two lines instead of one, thirteen times. It also removes a real hazard — `log.Fatal` runs no deferred function, and making the exit explicit is what lets a future change notice that.
- **The record format becomes a de-facto interface the moment anything reads it** → Nothing reads it today; changes 2 and 3 do not read it either. Naming it here so that the first consumer knows it is taking a dependency on an unversioned shape.
- **`gin.New()` drops gin's access-log format, which is not interchangeable with ours** → Nothing in this repository parses it and nothing outside it is documented to. Accepted.

## Migration Plan

Bottom-up, so that no intermediate commit has a package logging through two mechanisms:

1. `internal/platform/logging` — handler, level parsing, attribute helpers, its placement test, and the AST disclosure test (failing at first, which is the point).
2. The six `internal/` packages that log, one at a time, with their tests converted alongside.
3. The five composition roots: `slog.SetDefault`, the banners, the `log.Fatal` sites.
4. The three HTTP roots' `logging.go`, `gin.New()`, release mode.

**Rollback**: revert the change. It writes no state, migrates no schema, changes no wire format, and no other process consumes its output — so a revert at any point leaves a system identical to today's except for the log lines already emitted.

## Open Questions

None blocking. Two things are deliberately deferred rather than unresolved: request correlation identifiers (a Non-Goal, needing a propagation design across three hops) and whether the record shape should be versioned (worth revisiting when something first reads it, which is not in Phase 8).
