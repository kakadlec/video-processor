## Context

`proposal.md` states why a system whose records nobody aggregates has no numbers. This document settles the shape of the answer. Eight properties of the existing code constrain it, and each was read out of the tree rather than assumed:

- **The two non-HTTP processes are prohibited from serving, in words written to reach this change.** `openspec/specs/service-health-probes/spec.md:17`: the worker and the notifier "SHALL serve neither endpoint and SHALL acquire no HTTP surface **for this or any other observability purpose**". `openspec/specs/container-image/spec.md:55`: each "SHALL expose no port at all". Each package additionally carries an in-package source test asserting it constructs no HTTP server and imports no HTTP framework. Three pins, one of them phrased in advance for a metrics endpoint specifically.
- **Three worker replicas, and `instance` changes on every construction.** `docker-compose.yml:400` (`deploy.replicas: 3`); `internal/platform/logging/logging.go:57-63` builds `<hostname>-<pid>-<counter>`. Any push-based scheme keyed on instance accumulates dead groups forever here.
- **The access record already computes every input a request metric needs, and already solved the unbounded-label problem in its own terms.** `cmd/video-api/logging.go:99-133`: method, matched route, status, duration, size; `recordedMethod` collapses an unrecognized method to a fixed `UNRECOGNIZED` marker (`:63`), and only an *unmatched* request records a path, truncated to 256 bytes (`:56`). The truncation is the part a metric cannot copy.
- **gin can enumerate its own routes.** `(*gin.Engine).Routes()` returns `[]RouteInfo{Method, Path, …}` (`gin@v1.12.0/gin.go:68-76`, `:390`). The route-label domain is therefore readable from the router at startup rather than maintained by hand.
- **Both relays claim from one shared table, filtered on `event_type`.** `internal/video/infrastructure/postgres/schema.sql:76-94`, with a purpose-built partial index `video_job_outbox_unpublished_idx ON (event_type, occurred_at) WHERE published_at IS NULL`. This is the one place a process that is allowed to serve can see the state of a relay that lives in a process that is not.
- **But `video_job.created` rows keep `published_at` NULL permanently and by design** (`schema.sql:82-87`) — no relay claims them. A backlog gauge over *all* unpublished rows would therefore climb forever and mean nothing. The gauge has to be restricted to the relayed event types, which is the same closed set the two relays already filter on.
- **`video_jobs` is indexed for `WHERE status = 'processing'` and for nothing else status-shaped** (`schema.sql:64-68`, plus a `(user_id, created_at DESC, id ASC)` listing index). A `GROUP BY status` gauge would sequentially scan the whole job history on every scrape. The `processing` half of the gauge is free; the `queued` half is not.
- **`notification_deliveries` has no index serving a status aggregate** — primary key `(user_id, event_type, channel, job_id)` and a unique index on `delivery_id` (`internal/notification/infrastructure/postgres/schema.sql`). The equivalent notifier-side gauge is therefore not cheaply available, and is not taken.

One more fact shapes everything below, and it is the same one that shaped the probes: **there is no consumer.** No scraper runs in this repository. `docs/operations.md:5` — "There is no orchestration"; `:17` — no deployment target.

## Goals / Non-Goals

**Goals:**

- Numbers for the questions that today have no answer and no way to get one, chosen so that each has an action attached to it.
- A label rule that makes an identifier in a series structurally impossible rather than discouraged, enforced against the source in the place this repository already enforces such rules.
- A cardinality ceiling that is a number the build fixes, not an estimate.
- Some visibility into the two processes that may not serve, obtained from a process that may — with the residual stated rather than papered over.
- One dependency, costed with measurements rather than adjectives.

**Non-Goals:**

- Any listener, of any kind, on `cmd/worker` or `cmd/notifier`.
- A scraper, alerting rules, dashboards, recording rules, tracing, exemplars, or native histograms.
- A metric for anything an existing metric already answers.
- Any change to a route's behaviour, to a probe's verdict, to startup, or to the record format.

## Decisions

### 1. The three HTTP services serve `/metrics`; the worker and the notifier serve nothing, and that was already decided

This is the question `docs/roadmap.md`'s row calls open. It is answered *no* for the two non-HTTP processes, and the answer is **quoted rather than reached**: `service-health-probes` requires that they "acquire no HTTP surface for this or any other observability purpose" (`openspec/specs/service-health-probes/spec.md:17`). That clause exists because the change that wrote it anticipated this one. `container-image:55` says the same thing in deployment terms, and two in-package source tests hold the stronger form — a process can expose no port while still listening on one.

Reopening it is therefore not a decision available to this change. It would take a `container-image` delta, a `service-health-probes` delta, and the deletion of two tests archived the same week — and the argument would have to be made against a scraper that does not exist, which is exactly the shape of argument the probe change refused when it declined to build gateway routing "for a consumer that does not exist".

*Alternative considered — a Pushgateway, so the two processes push instead of being scraped.* This is the honest candidate, because pushing needs no listener and violates no requirement. It is rejected on this deployment's own shape. Prometheus's own guidance restricts the Pushgateway to service-level batch jobs, and the three reasons it gives all bind here: it becomes a single point of failure between the workers and any scrape; it removes the `up` signal, which is the one thing a scrape gives for free and the one thing a wedged worker would trip; and, decisively, **grouped metrics persist until they are deleted through its API**. `docker-compose.yml:400` runs three worker replicas and `instance` is resolved per *construction* (`logging.go:57-63`), so every restart of every replica leaves a permanent group behind, and the count of stale groups grows with the deployment's age rather than with anything real. A gateway that has to be garbage-collected by hand is worse than the gap it closes.

*Alternative considered — a non-HTTP exposition for those two: write the text format to a file on an interval, and have something else serve it.* Rejected as the prohibition's letter defeating its spirit while adding a second exposition path, a file whose staleness is invisible, and a new failure mode on the one process that must not stop consuming.

The residual is real and is named in decision 3's scope: **no extraction-duration histogram, no counter over the worker's seven-branch disposition table, no sweeper recovery counter, no delivery-attempt histogram against `MaxClaimHold()`.** Those are the metrics an operator most wants and this change cannot ship. `expose-worker-and-notifier-metrics` is the backlog row that would ship them, and it is written down precisely so the gap is a decision with a cost rather than an omission.

### 2. The endpoint ships with no scraper, and the stack gains no Prometheus server

`/health` shipped with a consumer of nothing at all and the change defended it: the pair is what keeps the criterion legible. The same reasoning is weaker here — a metrics endpoint with no scraper produces no time series, so it buys strictly less than a probe with no orchestrator does — and it is still the right default, for a reason that is about specification rather than taste. `container-image:55` permits a development-only support service to publish a loopback port of its own and says the mail catcher "is the only one". A Prometheus server is a second such service, so adding it needs a delta to that sentence, a `prometheus.yml` naming five targets of which two have nothing to scrape, and a retention decision. That is a change about the local stack, not about what the application exposes, and mixing the two would make this proposal's cardinality argument share a pull request with a volume-retention argument.

**This is flagged in `proposal.md` as the user's call**, with the default taken here and the follow-up named (`add-local-metrics-scraper`). What ships regardless is verifiable without a scraper: the endpoint is exercised by tests and by `wget` against a running container, and the exposition is parsed rather than eyeballed (decision 11).

### 3. What is measured, and the two things deliberately not measured

Grounded in what the code does, ordered by how directly an operator would act on it.

**The fail-open trio — first, because it is the gap that is worst-shaped for a log.**

| Metric | Labels | The action |
|---|---|---|
| `fiapx_rate_limit_decisions_total` | `decision` ∈ `allowed`, `denied`, `failed_open` | a non-zero `failed_open` rate means abuse control is **off right now**. `rate-limiting` calls this a loss of enforcement, not of efficiency. Today it is one `Warn` per request — a flood at exactly the moment nobody can read it. |
| `fiapx_upload_idempotency_reservations_total` | `outcome` ∈ `reserved`, `duplicate`, `failed_open` | `duplicate` is what the feature is for; `failed_open` means deduplication is off and repeat uploads are running `ffmpeg` again. |
| `fiapx_job_status_cache_lookups_total` | `outcome` ∈ `hit`, `miss`, `error` | the hit ratio is the only evidence the cache is worth its complexity, and `error` is Redis health **without** making Redis a readiness dependency, which `service-health-probes` refuses on purpose. |

The rate-limit counter is recorded in each of the two roots that mount a limiter. Summing it across both is the system view, because the budget is one budget per user across the whole system — `ratelimit:<userID>`, deliberately not namespaced per service.

**The HTTP surface — because the access record computes it all already.**

| Metric | Labels |
|---|---|
| `fiapx_http_requests_total` (counter) | `method`, `route`, `status_class` |
| `fiapx_http_request_duration_seconds` (histogram) | `method`, `route` |

Buckets are explicit and reach 60s rather than the client library's default ceiling of 10s. `POST /upload` streams the request body into the bucket, so its handler duration includes the client's own transfer — that is a property of the route rather than a defect, and a histogram whose top bucket is 10s would put every real upload in `+Inf` and report nothing.

**The pipeline, seen from the one process allowed to serve** — four gauges, decisions 8 and 9.

**Not measured, and each refusal is the interesting part:**

- *An authentication-outcome counter on `cmd/identity-api`.* `POST /api/auth/login` with a `4xx` status class already **is** that number. A second counter for one event is how two numbers that must agree stop agreeing, and the request counter is the one that cannot drift from what the router did.
- *Anything recorded only by `cmd/worker` or `cmd/notifier`.* Not because instrumenting them is hard — the shared packages they run are instrumented, and those counters do increment in those processes — but because a metric no process exposes has no value now, and proposing one would put decision 1's answer in contradiction with this section. What those processes record today is an unscraped register; the follow-up change adds a handler, not instrumentation.
- *A `notification_deliveries` aggregate.* No index serves it (see Context), and this change adds exactly one index, for the one gauge that closes a blind spot nothing else can see. A second index to serve a second gauge is the point at which "add an index per metric" becomes the policy, and the notifier's stall is better answered by instrumenting the notifier — which is the follow-up row.

### 4. A label value is a string literal, or the result of one of two named total constructors

This is the answer to the question `docs/roadmap.md` frames as "the same non-disclosure question logging answered". It is answered with the same instrument and a stricter rule.

`internal/platform/metrics/disclosure_test.go` walks every non-test file under `cmd/` and `internal/`, the way `internal/platform/logging/disclosure_test.go` does, and holds three rules:

1. **Every label value is a string literal, or a call to `metrics.Route(…)` or `metrics.Method(…)`.** This covers `WithLabelValues` arguments, the values of a `prometheus.Labels{…}` composite literal, and the trailing label values of `MustNewConstMetric`. The two permitted constructors are the exact analogue of logging's `slog.String/Int/Int64/Bool/Duration/Time` allow-list: a closed set of functions, each of which is **total** — it returns a member of a bounded set for every input, including inputs it does not recognize.
2. **Every metric name, help string, and label-name list entry is a string literal** — not `fmt.Sprintf`, not a `const`, not a variable. Logging's rule (3) exists because a formatted *message* satisfies every attribute rule while putting the identifier straight back into the record; a formatted metric *name* is the same defect one level worse, because a name is a series too.
3. **`promauto` is never imported, and `prometheus.DefaultRegisterer`, `DefaultGatherer`, and the package-level `Register`/`MustRegister` are never named.** The client library's default registry is process-global mutable state that **any transitively imported package can write into**, with no call site in this repository at all. That is the arbitrary-value hole in registry form: it is exactly `slog.Any`, except the value is a whole metric family somebody else chose. An explicit registry is the only way to be able to say what this endpoint exposes by reading this repository.

Translation from a domain value to a label therefore happens in a **total `switch` whose every branch passes a literal** — the same exhaustive-composition idiom `cmd/notifier` already uses over `domain.AllChannels()`, and greppable in a way a runtime allow-list is not.

**Where this is stricter than `structured-logging`, and why.** That capability permits `job_id`, `user_id`, `delivery_id` and `storage_key` in a record, because bounding the *type* is what makes a record safe. This capability permits **none of them in a label**, and the asymmetry is not an oversight:

- A record is one line, written once, that ages out of whatever holds it. A label value creates a **time series that persists for the retention period and is never reclaimed while it is still being written**. One user id in a label is one series per user, forever.
- A record's reader is a person reading a stream. A metrics endpoint's reader is a scraper, and every distinct label combination it has ever seen stays queryable — so a label is closer to a database column than to a log line.
- The consequence of getting logging wrong is a disclosure. The consequence of getting labels wrong is a disclosure **and** an unbounded resource commitment in a process nobody would think to look at.

*Alternative considered — "a string literal or a package-level `const`".* Attractive because it reads as more flexible. Rejected because it is **not syntactically checkable**: deciding whether `domain.StatusCompleted` is a constant needs type information across packages, and the logging walk deliberately loads none (it uses `types.ExprString` to render, never to resolve). A rule the walk cannot actually check is a comment.

*Alternative considered — sanitize label values at runtime instead: strip, hash or truncate.* Rejected for the reason the proposal gives about the 256-byte path truncation: a bound on *length* is not a bound on *count*. Hashing is worse — it produces a stable, unbounded, and now unreadable series per distinct input.

### 5. The route label's domain is the router's own route table

`metrics.BindRoutes(engine.Routes())` runs once in each `setupRouter`, after every route is registered. `metrics.Route(path)` returns `path` when the route table contains it and the fixed literal `<unmatched>` otherwise; `metrics.Method(m)` returns `m` when it is one of the table's methods and the fixed literal `UNRECOGNIZED` otherwise — reusing the access record's own spelling so the two fields read the same in a log line and in a series.

The middleware passes `c.FullPath()`, which for a matched request is already a member of the table by construction. The lookup is therefore belt-and-braces, and that is the point: it makes the bound **structural** rather than a property of where the value came from, so a later call site that passes something else cannot widen the domain.

The ceiling follows arithmetically instead of being estimated: `(len(engine.Routes()) + 1) × (distinct methods + 1) × (5 status classes)` for the counter, and the same route-and-method product times (buckets + 2) for the histogram. For `cmd/video-api` — nine application routes plus three operational ones — that is on the order of 200 series, and the equivalent for the other two services is smaller. The whole stack is a few hundred series plus the Go and process collectors, which is a number worth writing down because "a few hundred" and "unbounded" are the only two answers a metrics design ever has.

*Alternative considered — label by `c.Request.URL.Path`.* This is what most off-the-shelf gin/Prometheus middlewares do, and it is the canonical cardinality bomb: one series per distinct path, from an unauthenticated caller, before any rate limit. Named explicitly here because the tempting fix during implementation is to reach for such a middleware, and its default is exactly this.

*Alternative considered — drop the route label entirely.* Bounded, and useless: "the service served 40,000 requests" answers nothing anybody would act on.

### 6. `status_class`, not `status`

The counter carries `2xx`/`3xx`/`4xx`/`5xx`/`other` — five literals from a total switch — rather than the numeric code. Two reasons, and the second is the load-bearing one:

- The exact code is already in the access record for the same request, which is the artefact to read when a specific response is being investigated.
- Rendering a code into a label means `strconv.Itoa(status)` at the call site, which is **exactly the door rule 1 closes**. Admitting one non-literal label value because the value happens to be bounded today is how the rule acquires its first exception, and the next one is argued from the precedent rather than from the type.

*Alternative considered — a third permitted constructor, `metrics.Status(int)`, totalizing the code the way `Route` totalizes the path.* Coherent, and rejected on value: it would multiply every route's series by the number of distinct codes the handlers return, to answer a question the status class already answers and the log answers exactly.

### 7. A family is declared where it is recorded; the registry is one process-wide value, in `internal/platform/metrics`

`internal/platform/metrics` holds the **mechanism** — the registry, the handler, the two label constructors, the route-table binding, and the AST walk. It holds **no metric family belonging to a bounded context**, for the reason `internal/platform/rabbitmq` may hold no context name: `ddd-architecture` confines the platform namespace to plumbing, and a `fiapx_video_jobs_in_state` declared there would put a Video Processing noun in a package whose own dependency test forbids it.

Every family is therefore declared in the package that records it, and registers itself into the one process-wide registry — the precise analogue of `structured-logging`'s settled posture, where the platform package builds the handler and every record is emitted through `slog.Default()` where the event happens. Three consequences are accepted rather than hidden:

- The HTTP families are declared **once per HTTP root**, a fifth deliberate copy beside `auth.go`, `ratelimit.go`, `logging.go` and `probes.go`, because `package main` cannot import `package main`. The duplication is bounded at three and each copy carries its own tests, exactly as the existing four do — and it strengthens the case for rule 1, which is enforced across all copies by one walk rather than by three reviews.
- `internal/video/infrastructure/cache` records into the registry in **every process that links it**, including `cmd/worker`. Those counters are incremented and never served. The cost is an atomic add on a path that already makes a Redis round trip, and the benefit is that the follow-up change adds a handler rather than re-instrumenting anything.
- A test that reads what a family recorded swaps process-global state and cannot be parallel — the same cost `structured-logging` named for `slog.SetDefault` and accepted in the same terms.

*Alternative considered — pass a registry through constructors.* Rejected on the rule `CLAUDE.md` already states for loggers: do not thread one through a constructor that needs it for no other reason, because that is how a bounded context acquires an opinion about its process's observability. The one exception logging carved out — `internal/notification/application`, which accepts a logger because its own tests needed one — does not apply, because nothing in that package records a metric in this change.

### 8. A collected gauge queries on scrape, never caches, and never reports a number it does not have

The four pipeline gauges are a `prometheus.Collector` whose `Collect` runs the two aggregate statements, bounded by a context timeout of its own, against the video pool `cmd/video-api` already holds.

**On a query failure it emits no value for that family and reports the failure to the scraper** as an invalid metric, which surfaces the scrape as failed. It does **not** emit `0`. That distinction is the whole decision: `fiapx_video_jobs_in_state{state="queued"} 0` means *nothing is waiting*, which is the single most reassuring statement this endpoint can make, and emitting it because a query timed out would turn a database problem into a green dashboard. A stale-but-remembered value is refused for the reason `storage.Ping`'s doc comment already gives — it answers for a different moment than the caller asked about.

**The count saturates; the age does not.** A count over a partial index costs one index entry per matching row, which is nothing when healthy and is proportional to the backlog when broken — and the backlog is exactly the case that repeats every scrape interval forever. The count is therefore taken over a bounded subquery and saturates at a stated limit, while the oldest-age gauge is a one-row index lookup that never saturates. A saturated count still says *at least this many*, and the age says how bad it is, which is the number an operator acts on: a `queued` count of 40 means nothing on its own, and an oldest-`queued` age of eleven minutes means the workers are gone.

### 9. One new partial index, and two states rather than five

`fiapx_video_jobs_in_state` carries `queued` and `processing` and no other state. Each exclusion has a reason:

- `processing` is free: `video_jobs_processing_id_idx` already exists for the sweeper, with a schema comment justifying it in exactly these terms — "an unindexed scan would re-read the entire job history, which only ever grows, every interval".
- `queued` is the one that matters and the one nothing serves. It gets a new partial index on `video_jobs (created_at) WHERE status = 'queued'`, shaped like the one beside it. A job enters and leaves this index once, on the same edge it enters and leaves the `processing` one, so the write cost is the same shape as a cost the schema already accepts. **This is the only schema change in the proposal**, and it is taken for the only metric that can distinguish "uploads are being accepted and nothing is consuming them" from "the system is idle" — the exact condition `docs/operations.md:751` records as currently undetectable.
- `pending` is excluded on a stronger footing than cost: `POST /api/video-jobs` creates jobs with no processing trigger that stay `pending` **forever, by design**. A gauge over them climbs monotonically and means nothing, which is the definition of a metric nobody would act on.
- `completed` and `failed` are excluded because a gauge is the wrong instrument for a terminal state — the interesting quantity is a rate, which needs a counter, which needs the process that writes the transition, which is the worker.

The outbox gauges are restricted to the **relayed** event types — the union of the two relays' own explicit claim sets, three values — rather than to every `event_type` present. `video_job.created` rows keep `published_at` NULL permanently and by design (`schema.sql:82-87`), so an unrestricted gauge would climb forever while describing nothing. Restricting it to the same set the relays filter on is also what keeps the label's domain closed.

### 10. `/metrics` is access-logged like any other route, and there is no `structured-logging` delta

The probe exemption was justified by volume from a prober arriving at a fixed interval forever, and by a replacement record that says more. Neither holds here. **No scraper exists in this repository**, so the volume is zero today and arguing it would be arguing from a consumer that does not exist — the exact move the probe change refused when it declined to build gateway routing for one. And there is no better replacement record to offer: a scrape has no verdict to transition.

There is also a positive reason. `/metrics` is an unauthenticated request from outside the process whose handler queries the database, so its status and duration are the only evidence that a scrape happened and how long collection took — unlike `/health`, whose record varies in no field.

The exception in `structured-logging` therefore stays closed at exactly two route templates, which was that requirement's stated value. If a scraper is added later at, say, a 15s interval, that is 5,760 records per service per day and the exemption deserves revisiting — **by the change that creates the condition**, with a real interval to put in the arithmetic.

*Alternative considered — exempt `/metrics` from the access record now, so the arithmetic never has to be revisited.* Rejected: it widens a closed list by anticipation, for a volume that is currently zero, and the requirement's own text says the value of the rule is that it has no escape hatch.

The HTTP **metric**, conversely, covers every route the router serves, including `/health`, `/ready` and `/metrics`. No exemption list at all: three more route values is a handful of series, an exemption list is a mechanism that grows, and including the probes gives back in the metric something the access record gave up.

### 11. Unauthenticated, ungrouped, gateway-refused — and what that still discloses

The endpoint mounts on the engine beside `/`, `/health` and `/ready`, outside `requireBearerAuth()` and outside `rateLimitMiddleware`, for the three reasons `service-health-probes` already gives and which apply unchanged: the limiter keys on a subject a scrape does not carry and would eventually answer `429`, which a scraper reads as down; `cmd/identity-api` mounts neither middleware at all, so authenticating would be a policy on two services and an accident on the third; and requiring a token would make Identity a dependency of every other service's observability, which is the coupling configuration-distributed public keys exist to remove.

The gateway gains a third exact-match refusal — `location = /metrics { return 404; }`, no `set $backend`, no `proxy_pass` — beside the two the probes already have, with the same requirement: **status-code equivalence with an unserved path, plus non-arrival**, and explicitly not a byte-identical body, because that would take `proxy_intercept_errors on` and rewrite the body of every upstream `404` including the download route's deliberately identical one.

**The disclosure asymmetry, stated rather than left for review to find.** `/ready` has two controls: the gateway refusal and a body that names no dependency. `/metrics` has one. Even with every label bounded, the body is an inventory: every route template the service serves, the error rate of each, the existence and health of the cache and the limiter, the depth of the job pipeline, and the Go toolchain version through `go_info`. That is more than a probe discloses and the posture has genuinely loosened.

It is accepted on the argument the readiness transition record already uses — **the reader is inside the deployment network**, which is the same reader that can already open a TCP connection to port 8080 and enumerate the routes by asking. What is *not* loosened, and what the label rule makes structural rather than promised: **no individual fact is derivable.** No user, job, delivery, destination, storage key, content hash, or webhook target appears in any label or any metric name, so the endpoint says how the system is behaving and nothing whatever about any one person or any one job.

### 12. The Go and process collectors are registered, and `go_info` is named as part of the cost

`collectors.NewGoCollector()` and `collectors.NewProcessCollector()` go into the explicit registry. They are the cheapest available answer to a class of failure nothing else in this system can see: a goroutine leak (the relays, the sweeper, the consumers and the heartbeat all run goroutines), heap growth, GC pressure, and file-descriptor exhaustion. Their cardinality is fixed, known and small.

They are registered **explicitly into our registry**, which is the difference that matters: the client library's default registry contains them by default, and decision 4's rule 3 refuses that registry. Getting them by naming them is the same set of series and a different property — what the endpoint exposes is readable from this repository.

`go_info{version="…"}` discloses the Go toolchain version. Named here rather than discovered in review: it is a supply-chain hint, it is on the disclosed side of decision 11's line, and it is the one piece of that inventory that says something about the build rather than about the deployment's behaviour.

## Risks / Trade-offs

- **The first new runtime dependency in several changes, in a repository that has made a point of not adding them** → One direct module, five genuinely new indirect ones, two already present, protobuf bumped a patch; `govulncheck` clean on that resolution today. The surface is permanently larger and `Vulnerability Scan (govulncheck)` is a required check, so a future advisory against `prometheus/common` becomes this repository's problem. Accepted because there is no way to expose Prometheus exposition without it that is not worse — a hand-rolled text encoder would be untested code producing a format with a specification.
- **Five binaries in one image, all of which link the library and two of which serve nothing** → The measured +8.1 MB on a trivial program overstates the per-binary delta here (protobuf, xxhash and `x/sys` are already linked), but whatever it is, it lands five times. Measured during implementation rather than asserted now.
- **A database query on every scrape, from the service that also serves uploads** → Two statements, both index-served, both context-bounded, the count saturating. No caching, on `storage.Ping`'s stated grounds. The alternative — a background refresher — reintroduces exactly the staleness that decision removed.
- **A gauge over shared state reported per replica** → One `video-api` today, N identical series at N replicas, separated by `instance`. An operator must aggregate with `max by (state)`, not `sum`. Invisible until someone scales that service, so it is written into the capability rather than left in a comment.
- **The label rule is a source-level rule and sees only the call sites that existed when it was written** → True of all three logging rules too, and stated for the same reason: no behavioural test can hold a claim about source, and this is the enforcement that exists rather than the enforcement that would be ideal. The vacuity guard and the `ENOENT` skip that #251 added to the existing walkers are carried over rather than rediscovered.
- **The endpoint discloses an inventory and has one control instead of two** → Decision 11, accepted with the reason stated and the hard line (no individual fact) made structural.
- **The metrics that would matter most are the ones this cannot ship** → Decision 1. The gap is narrowed by four gauges and left otherwise open, with a backlog row naming exactly what closing it would cost.
- **An endpoint with no consumer produces no series and could rot** → Real, and mitigated the way the probes were: tests that parse the exposition rather than eyeball it, plus a documented `wget` against a running container. The stronger mitigation is the scraper, which is the flagged decision.

## Migration Plan

Additive throughout; no intermediate state is broken and no existing response changes.

1. `internal/platform/metrics`: the registry, the handler, the two label constructors, the route-table binding, and the three-rule AST walk. The walk lands with the package, before there is anything for it to catch, so the first family added is judged by it.
2. The HTTP families and the `/metrics` route in each of the three roots, with `BindRoutes` in `setupRouter`.
3. The three counters: the limiter's decision counter in the two roots that mount one, the reservation counter in `cmd/video-api/video.go`, the cache counter in `internal/video/infrastructure/cache`.
4. The new partial index in `internal/video/infrastructure/postgres/schema.sql`, then the two aggregate queries, then the collector, then its registration in `cmd/video-api`.
5. The gateway's third refusal block.
6. Documentation and the roadmap row, in the finalization pass.

**Rollback**: revert. The change adds one route per HTTP service, one index, two read-only queries and a set of counters; it writes no application state, changes no wire format, no response and no shutdown sequence, and nothing in the system reads its output. The index is the only artefact a revert leaves behind, and a `DROP INDEX` is optional rather than required — an unused partial index costs one entry per `queued` job.

## Open Questions

One is flagged for the user and named in `proposal.md`; the other two are deferred with reasons.

- **Does the local stack gain a Prometheus server?** This proposal's default is no, and it is the one decision here that is genuinely the user's rather than the design's, because it trades a `container-image` delta and a second inspection port against an endpoint that otherwise produces no series in this repository. If the answer is yes, it is `add-local-metrics-scraper` and it is small.
- **When do `cmd/worker` and `cmd/notifier` get their own metrics?** Deferred to `expose-worker-and-notifier-metrics`, with the deltas it needs written down. Not resolved here because the argument for it is different from the probe argument that was refused — a scraper is a consumer that can be made to exist, unlike an orchestrator — and that argument deserves its own review rather than a paragraph inside this one.
- **Do the histogram's buckets survive contact with real traffic?** They are chosen from what the routes do (`POST /upload`'s duration tracks the client's transfer) rather than from a library default, and a bucket set is the one thing here that is cheap to change later and impossible to change retroactively for data already collected. Revisit when there is a scraper with history.
