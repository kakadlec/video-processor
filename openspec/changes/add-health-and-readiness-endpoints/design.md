## Context

`proposal.md` states why an unprobeable process is a problem. This document settles the shape of the answer. Six properties of the existing code constrain it, and each was read out of the tree rather than assumed:

- **Startup is the only place a dependency is ever verified, and it is fatal.** `cmd/identity-api/identity.go:68-81`, `cmd/video-api/video.go:150-211`, `cmd/notification-api/notification.go:76-87`. Nothing re-checks. This is what makes a readiness endpoint worth anything at all: it is not a second opinion on `t=0`, it is the only opinion on `t>0`.
- **The database check already exists and is already wired; the object-storage one does not.** `db.PingContext` is context-taking and documented as the caller's job (`internal/video/infrastructure/postgres/db.go:10-13`). `storage.Ping` issues a real `BucketExists` round trip — chosen over `IsOnline` because that reports a cached observation — but **discards the boolean** (`internal/video/infrastructure/storage/client.go:32-36`), so it answers *is the server reachable*, not *does the bucket exist*. Startup does not notice, because it calls `EnsureBucket` on the next line. A readiness endpoint has no next line, and that gap is what decision 9 closes. `platformredis.Ping` and `platformrabbitmq.Ping` exist with **zero production callers**.
- **`platformrabbitmq.Ping(conn)` takes a live connection and no context** (`internal/platform/rabbitmq/client.go:38-51`). No HTTP process holds an AMQP connection: the relay stores a `Config` and dials inside its loop, closing per cycle (`internal/video/infrastructure/messaging/relay.go:63-68`, `:115`, `:134`). So consulting the broker from a probe is not a judgement call this design makes — it is unavailable, and would be unbounded if it were.
- **The JWT verifier is network-free.** PEM from environment, no JWKS (`cmd/video-api/auth.go:37-43`). There is nothing there a readiness check could consult, which is the property `split-api-by-bounded-context` bought deliberately.
- **The router shape is fixed and has a precedent for an ungrouped route.** All three `setupRouter`s mount the global middleware pair first, then CORS, then ungrouped routes, then the authenticated group (`cmd/video-api/main.go:185-215`, `cmd/notification-api/main.go:169-200`, `cmd/identity-api/main.go:116-150`). `/`, `/styles.css` and `/app.js` already sit outside the group. The documented invariant is about the *pair and its order* — the limiter keys on the subject the auth middleware establishes — not about every route being inside it.
- **The access log has no exclusion mechanism and is pinned twice.** `accessLogMiddleware` is mounted on the engine deliberately, "it has to see the requests that matched no route", and emits one `Info` unconditionally. `openspec/specs/structured-logging/spec.md:125` and `:144` state the obligation; `logging_test.go` in each of the three roots holds it, and commit `390e4ce` exists for that purpose.

One more fact shapes everything below: **there is no orchestrator.** `docs/operations.md:5` — "There is no orchestration"; `:17` — "This project has no deployment target". Compose can mark a container unhealthy, and acts on that only through `depends_on: condition: service_healthy`, which today targets backing services alone.

## Goals / Non-Goals

**Goals:**

- A liveness answer and a readiness answer that differ in *criterion*, not merely in path, so neither is dead weight.
- A readiness criterion derived from a rule this repository already stated, applied uniformly, with the per-service answers falling out of it rather than being enumerated by taste.
- Every check bounded, with the bound related to the prober's bound rather than chosen independently of it.
- A probe response that discloses nothing an unauthenticated caller should not have, while the *reason* stays recoverable by someone who can read the log stream.
- The two non-HTTP processes answered — not omitted — and the cost of that answer stated.
- No new dependency and no change to startup, with the one new check implementation the existing ones cannot express confined to a single read-only adapter operation.

**Non-Goals:**

- `/metrics`, Prometheus, an exposition format, a second listener on an admin port. That is the phase's third change.
- Any HTTP surface, of any kind, on `cmd/worker` or `cmd/notifier`.
- A startup/warm-up probe (`startupProbe`, or compose's `start_period`) — a third semantic with no consumer, on processes whose startup is fail-fast and therefore has no warm-up window to describe.
- Dependency-failure *recovery*. `/ready` reports; nothing in this change reconnects, retries, or restarts anything.
- Making the probes part of the application's public HTTP surface.

## Decisions

### 1. Two endpoints, `GET /health` and `GET /ready`, and the second is the only one that consults anything

`/health` runs no check, holds no dependency, and answers `200` for as long as the process is scheduled and its router still answers. `/ready` answers `200` or `503` after checking that process's readiness dependencies.

**What `/health` actually detects, stated plainly because a reviewer will ask and the answer should already be here**: that the process exists, that its accept loop is still accepting, and that its global middleware chain still returns. That is close to the whole list. It does not detect a wedged dependency, a starved worker pool, a leaked goroutine, or a handler that hangs on a route other than this one. It is a narrow signal on purpose — the remedy wired to liveness is *restart*, and restart is the correct response to exactly that narrow class and to nothing else.

*Alternative considered — one `/health` that consults dependencies.* Rejected, and this is the decision the whole change turns on. A single endpoint gets both jobs and gets the second one wrong: whatever a runtime restarts on ends up consulting the database, and then a 30-second PostgreSQL failover restarts every replica of every service, each of which comes back up, fails startup's own fatal ping, and exits. The outage becomes a crash loop that outlives it. Two endpoints exist so the dependency-consulting one can never be the restart trigger.

### 2. The readiness criterion is quoted, not invented

`docker-compose.yml:149-152`, on the gateway's own healthcheck:

> Asks nginx whether its own configuration is loadable. Deliberately not a request through to a backend: the gateway is healthy when it can serve, and tying its health to a backend's would make an unrelated service's restart look like an ingress failure.

Generalised to a process: **readiness consults only the dependencies whose absence prevents that process from honouring its own contract. A dependency the system is designed to survive is not a readiness dependency.** Applied:

- **PostgreSQL, every service.** With it down, `identity-api`'s two routes fail (both are bcrypt plus a row), `notification-api`'s two routes fail, and `video-api`'s `POST /upload` `500`s at `createVideoJob` (`cmd/video-api/video.go:698-731`) with `/download` and `/api/status` failing too. Nothing in the system is designed to survive it.
- **MinIO, `video-api` only.** `POST /upload` `500`s at `m.sources.Put` (`video.go:582-592`); `/download` and `/api/status` both need it. And the check has to be *presence*, not merely reachability: all three routes name objects inside one configured bucket, so a reachable server whose bucket has been deleted fails every one of them while a reachability ping still passes. Excluded elsewhere — neither other service holds a client.
- **Redis, nowhere.** Every feature over it is specified to fail open: `upload-idempotency`'s `Reserve` logs and proceeds (`video.go:658-661`), `rate-limiting` allows the request, `videojob-status-cache` degrades to PostgreSQL. With Redis down the system is slower and unmetered and entirely correct. Reporting not-ready would take a serving process out of rotation for a condition its own specifications call survivable — which is the gateway comment's failure mode exactly.
- **RabbitMQ, nowhere.** Two independent reasons, and the second is a hard constraint rather than a preference. *Behavioural*: `EnqueueVideoJob` touches no broker — it commits the `pending → queued` update and the outbox row in one PostgreSQL transaction (`internal/video/infrastructure/postgres/repository.go:479-509`) — so `POST /upload` answers `202` with the broker down and the relay dispatches when it returns. *Structural*: no HTTP process holds a connection to ping, and `Ping` takes no context, so the check could not be bounded per decision 4.

The rule also settles the cases nobody asked about: the JWT verifier (network-free, nothing to consult) and the embedded frontend (`go:embed`, needs nothing — `cmd/video-api/main.go:200-208`).

*Alternative considered — report Redis and the broker as a separate "degraded" state.* A third verdict with no consumer, which the response body would then have to carry, which decision 6 forbids it from carrying. Rejected.

### 3. The probes are unauthenticated, ungrouped, and disclose nothing

They mount on the engine, beside `/`, `/styles.css` and `/app.js` — outside `requireBearerAuth()` and outside `rateLimitMiddleware`. Three reasons, in order of force:

1. **The limiter has nothing to key on.** It keys `ratelimit:<userID>` off the subject the auth middleware establishes; a probe carries no subject. And a probe at a fixed interval *inside* the limiter would eventually exhaust a budget and answer `429`, which a prober reads as unhealthy — the limiter manufacturing an outage.
2. **`cmd/identity-api` mounts neither middleware at all**, by construction. An authenticated probe would therefore be authenticated on two services and not the third, which is not a policy, it is an accident of where the group happens to exist.
3. **It would make Identity a dependency of every other service's health signal** — the exact coupling `IDENTITY_JWT_PUBLIC_KEYS`-by-configuration was chosen to remove.

**What the response must therefore never disclose.** This repository has a settled posture for a response whose detail is a probe: `ErrDestinationRefused` is one sentinel, not one per rule, and the `400` says only that the destination was refused, because rules that enumerate internal address space would otherwise be readable by resubmission; `GET /download/:filename` returns a byte-identical `404` for every rejection for the same reason. The probe bodies follow it. A readiness body naming *which* dependency is down is an information-disclosure decision, not a formatting one: it tells an unauthenticated caller this deployment's dependency inventory and, through timing, when each one is degraded. So:

- `/health`: `200`, fixed body, no fields that vary.
- `/ready`: `200` or `503`, fixed body per verdict. **No dependency name, no error text, no host, endpoint, DSN, bucket, version, build identifier, hostname or instance id.** The status code carries the whole answer.
- Both carry `Cache-Control: no-store`, following `GET /download/:filename`, so no intermediary can answer a probe from a stored verdict.

Which dependency failed goes to the **log**, where the reader is already inside the deployment. That is decision 5.

### 4. Every check is bounded, checks run concurrently, and the bound is stated against the prober's

Each check runs under a context derived from the request's — so a disconnected prober cancels it — with a **timeout shorter than the prober's own timeout**. The relationship is the point: if the check's bound exceeds the prober's, the prober gives up first and *every* verdict is a failure regardless of the dependency's state. The signal inverts, and it inverts silently.

Where a service has more than one check (`video-api`: PostgreSQL and MinIO), they run **concurrently**, so the endpoint's worst case is the maximum of the bounds and not their sum. Sequential checks make the bound a function of how many dependencies a service happens to hold, which is the wrong variable.

**No verdict caching.** A probe that answers from a stored observation is precisely what `storage.Ping`'s doc comment rejects — it "would answer for a different moment than the caller asked about". Accepting the round trip per probe is the cost of that decision, and at the interval chosen in decision 7 it is a few pings a minute.

### 5. The probe routes are excluded from the access record, and `/ready` logs verdict transitions instead

At any realistic interval the access record for a probe is information-free by construction: `/health` consults nothing, so its status is always `200` and its duration always near zero; three services probed every 10s is 25,920 records a day, against a system whose entire logging change catalogued **170** emitting call sites. The probe traffic would be the overwhelming majority of the log stream and would say nothing.

For `/ready` the informative part is a *change* of status, not a status. So the exclusion is paired with a replacement that is strictly better than what it removes:

- `/ready` holds the last verdict in the process and emits **one record on each transition** — not-ready at `warn` when the verdict turns, back to ready at `info` when it recovers. Transitions, not probes: `O(1)` per outage rather than `O(interval)`.
- **The transition record names the failing dependency**, which the response body deliberately cannot. Non-disclosure moves the detail; it does not destroy it.
- The record obeys `structured-logging`'s source rules unchanged: fixed string-literal message, typed scalar attributes only. The failing dependencies are joined from a **closed set of names this code owns** — never caller-supplied text.
- The verdict is held as an atomic value and swapped compare-and-set, so concurrent probes produce exactly one record per transition rather than one per prober.

This is a **`structured-logging` delta**, not an implementation detail, and the proposal says so: the spec currently obliges every request to yield an access record and offers no exclusion mechanism (`spec.md:125`, `:144`), and each of the three roots carries a test pinning it. The delta names **exactly the two probe route templates** as the exception. A general mechanism — a configurable skip list, a middleware option — is refused: it would let any future route leave the access log without spec review, and the value of the current requirement is that it has no escape hatch.

*Alternative considered — record the probes anyway, at `debug`.* Attractive because it needs no spec delta. Rejected: `LOG_LEVEL` defaults to `info`, so the records are invisible by default and the requirement is satisfied only in letter; and the moment an operator raises a service to `debug` to investigate something else, the probe traffic buries what they are looking for. A record whose correct setting is "never read" is not a record.

*Alternative considered — no compose healthcheck, so no volume, so no delta.* Also viable, and it was weighed. Rejected because it leaves the endpoints entirely unexercised in the only environment this repository has, which is how a probe endpoint rots.

### 6. The probes are internal-only, and making that true takes a gateway change

Today `/health` and `/ready` on `video-api` would be reachable from outside for free, because they fall into `location /` (`docker/nginx/nginx.conf:107`), while the same paths on `identity-api` and `notification-api` would be unreachable — they would fall through to `video-api`. That asymmetry is an artifact of the default route, not a decision.

Two exact-match blocks on the gateway — `location = /health` and `location = /ready`, each `return 404` — make the answer uniform: **no probe is reachable through the gateway, on any service.** `404` rather than `403` or `444` follows the repository's existing `404` idiom, and it costs nothing at request time.

**What "indistinguishable" can mean here, and what it cannot.** These two refusals and the `404` an unknown application path receives are *not* byte-identical, and this design does not pretend otherwise: the first is generated by nginx itself, while the second is gin's own `404 page not found` handed back through the proxy, so they differ in body, in `Content-Type` and in `Content-Length`. The only way to collapse them at the gateway is `proxy_intercept_errors on` (it is off by default and unset today), and that directive intercepts **every** upstream `404` in the system and replaces its body — including `GET /download/:filename`'s, which this repository makes byte-identical across every rejection deliberately, and `GET /api/video-jobs/:id`'s. Buying body-equivalence on two probe paths by rewriting the body of the one route whose contract depends on its own is a bad trade, and it would make a routing directive load-bearing for an authorization decision two contexts away.

The requirement is therefore **status-code equivalence plus non-arrival**: a probe path requested through the gateway answers `404`, the same status an unserved path answers, and the request reaches no service. Both halves are directly checkable against the running gateway, the second because these blocks carry no `proxy_pass` at all.

**The residual, stated rather than glossed.** A caller who compares bodies can tell that the gateway names these two paths specially. What that discloses is that this deployment has probe endpoints at two conventional names — not a verdict, not which dependency is unavailable, not the dependency inventory. Every one of those is withheld at the service itself by decision 3 and none of them travels through the gateway on any path. The control that matters is that no probe is *answered* through the ingress; indistinguishability of the refusal is a weaker property, worth having where it is free and not worth `proxy_intercept_errors`.

The prober does not need the gateway: a compose healthcheck runs *inside the container* and talks to `127.0.0.1:8080`, the same port the service already listens on, on no published port at all. So `container-image`'s and `development-workflow`'s requirement that the three application services publish no host port is untouched.

`development-workflow`'s "the contributor SHALL still reach every route on one host port" is also untouched, and the reason is worth stating rather than assuming: the probes are not part of the application's HTTP surface. They serve no user-facing behaviour, appear in no flow, and are consumed by the runtime. That is the same category `development-workflow` already carves out for a service that "serves no application route".

*Alternative considered — three new `location` blocks, one per service, each with its own `set $backend`.* Would make the probes externally reachable and uniform. Rejected: it builds routing for a consumer that does not exist, it puts a paths-per-service map into the gateway that would have to be maintained alongside the real one, and it makes the non-disclosure rule in decision 3 the *only* control instead of the second one.

### 7. `docker-compose.yml` gains three healthchecks, against `/ready`, and nothing depends on them

Each of `identity-api`, `video-api` and `notification-api` gets a block shaped like the gateway's: `interval: 10s`, `timeout: 5s`, `retries: 5`, no `start_period`. 10s rather than the backing services' 5s because the gateway is the closest analogue in this file — the one service probed over its own surface rather than with a vendor's purpose-built diagnostic — and because, probes being excluded from the access log, the interval is no longer a logging-volume decision and does not have to buy anything back.

The command is a `wget` GET against `http://127.0.0.1:8080/ready` that fails on a non-2xx status. `curl` is **not** in the runtime image (`alpine:3.24` plus `ffmpeg`); `sh`, `wget` and `nc` are. One trap is named here so it is verified rather than discovered: busybox `wget --spider` may issue a `HEAD`, and gin does not answer a `HEAD` on a `GET`-only route, which would make every probe report unhealthy for a reason that has nothing to do with the service. The chosen flags must be confirmed to issue a `GET`.

`/ready` rather than `/health` is the healthcheck target because a container that is up but cannot serve is the thing an operator wants `docker compose ps` to show; liveness would report healthy for a process whose database vanished, which is the whole distinction restated.

**Nothing gains `depends_on: condition: service_healthy` against these services.** The gateway's `depends_on` on the three APIs stays the bare list form it is today (`docker-compose.yml:144-147`). Making the gateway wait for `video-api` to be *ready* would tie ingress startup to MinIO's — the coupling the gateway's own healthcheck comment refuses, reintroduced through the back door. And nginx already handles a backend that is not up: `set $backend` defers resolution to request time, so an early request is a `502` that recovers by itself.

So the verdict's consumer is `docker compose ps`, `docker inspect`, and a person. **`/health`'s consumer today is nothing at all** — no runtime here restarts on liveness. It is specified and served now anyway, because the pair is what keeps the criterion legible: a lone `/ready` invites whatever arrives next to restart on it, which is decision 1's failure.

### 8. `cmd/worker` and `cmd/notifier` get no HTTP surface, and that is already a requirement

The roadmap row leaves this open. It is answered *no*, and the strongest reason is not a doc comment:

**`container-image` already requires it.** `openspec/specs/container-image/spec.md:55` — "the worker and the notifier SHALL each expose no port at all" — with scenarios at `:83` and `:88`. Giving either a listener is therefore not a decision available to this change: it needs a `container-image` delta, and that delta's argument would have to be made against an orchestrator that does not exist. `Dockerfile:59-62`'s `EXPOSE` comment ("each is reached only through the broker") and both package docs (`cmd/worker/main.go:6-8`, `cmd/notifier/main.go:7-10`) say the same thing; neither process imports gin or `net/http` server machinery today.

A readiness verdict nothing consumes is documentation, not behaviour — and for these two there is not even a compose healthcheck to consume it, because the reason to have one is weaker: a consumer that cannot reach its broker is not "not ready", it is idle, and the broker's own healthcheck already reports the broker.

**What this costs, and it is not nothing.** Neither process gets a liveness signal, and **no existing record fires on an idle stack for either**: the worker's sweeper runs every 60s but every record in `sweeper.go` is conditional on finding something; the relay logs lifecycle only; both consumers log only on failure, so a healthy connected consumer is silent. An idle-but-wedged worker is today indistinguishable from an idle-and-healthy one, and this change leaves it that way. Two things bound the damage rather than remove it: both consumers register a `consumerTag` (`video-worker`, `notification-notifier`) explicitly so they are visible in RabbitMQ's own management listings, and a command-based compose healthcheck remains available without any spec change if one is ever wanted, since `sh`, `wget` and `nc` are all in the runtime image.

Because the answer is a *claim about source*, it is pinned the way this repository pins such claims (`TestOnlyTheIdentityServiceConstructsATokenIssuer`): `cmd/worker` and `cmd/notifier` each carry a source-level test over **their own** package's non-test files asserting that neither constructs an HTTP server nor imports gin. In-package rather than a cross-root scan, so the failure lands in the package that introduced the listener.

### 9. `setupVideo` returns a purpose-built object-storage readiness check, not the raw client

The readiness check is built from the handles `main` holds. For the database that is enough already: `setupIdentity` and `setupNotification` return their `*sql.DB`, and `db.PingContext` needs nothing else. Object storage is the case that does not work, and it fails twice over.

**First, `main` holds neither handle the check needs.** `setupVideo` returns `(*videoModule, *sql.DB, *redis.Client, *videomessaging.Relay, error)`. It constructs, pings and bucket-checks a `*minio.Client` (`video.go:191-211`) and returns it to nobody, and the bucket name never leaves the function at all — it lives in the local `minioConfig` variable. Every object-storage call in this repository takes `(ctx, client, bucket)`, so the check cannot be assembled from what the composition root has.

**Second, the call it would make does not answer the question.** `storage.Ping` runs `BucketExists` and **discards the boolean** (`internal/video/infrastructure/storage/client.go:32-36`): a reachable server whose bucket has been deleted returns nil. Startup is unaffected only because `EnsureBucket` runs on the next line and would recreate it. A readiness endpoint has no next line and must never create anything, so `Ping` alone would hold `/ready` at `200` while upload, status and download all failed.

Both are answered by one decision. **`setupVideo` gains a single return value: a readiness check that has already captured the client and the bucket**, with the same `func(context.Context) error` shape every check in this change has, and that check performs a **bounded, read-only bucket-presence** call rather than `Ping`.

The new storage operation is `EnsureBucket`'s first half without its second — `BucketExists`, with the boolean read instead of dropped, and no `MakeBucket`. It is read-only by construction, not by convention, and it is bounded by the caller's context like every other call in that package.

**`Ping` is left exactly as it is**, and the reason is concrete rather than stylistic. Startup pings *before* it ensures. A `Ping` that asserted presence would make every first boot against an empty object store fatal, on a bucket `EnsureBucket` was about to create one line later — it would silently change what startup checks, in a change whose stated premise is that startup is not touched. Two callers want two different questions, so there are two operations.

*Why a check rather than the two values.* Returning `(*minio.Client, string)` would work and is the smaller diff, and it is still the wrong seam: it puts a raw driver handle and a bucket name into a composition root that has no other use for either, and it makes every future caller re-derive what "is object storage ready" means. Returning the check keeps that definition in the package that owns the bucket, and gives `main` a value it can only use one way. It is also the shape this repository already reaches for — `setupRouter(auth, video, limiter)` is handed its collaborators rather than the configuration they were built from, and `logger(component)` returns a configured logger rather than the logging configuration.

*Alternative considered — put the client on `videoModule`.* Rejected. `videoModule` holds use cases and domain ports — `sources`, `results` — and a `*minio.Client` on it would be the first raw driver handle there. Readiness is a composition-root concern for the same reason the shutdown sequence is.

*Alternative considered — check object storage through the `SourceStorage` port instead.* Would avoid the signature change. **Rejected, and this rejection is unchanged by the decision above**: the port has no health operation, adding one puts a probe concern into a domain interface, and `Stat` of a key that does not exist is a `404` that means *healthy* — a check whose success condition is an error is a check waiting to be "fixed". The presence check adopted here is not that alternative arriving by another door: it sits in the infrastructure adapter beside `Ping` and `EnsureBucket`, where a bucket-level operation already lives, and the domain port is untouched.

## Risks / Trade-offs

- **`/ready` becomes a traffic source against the dependency it checks, at the probe interval, forever** → 10s per service, one PostgreSQL ping each and one MinIO `BucketExists` for `video-api`. Well under a request per second in aggregate. Named because caching is the obvious mitigation and decision 4 refuses it on `storage.Ping`'s own stated grounds.
- **The probe's own bound and the prober's are set in different files and can drift apart** → If the check's bound ever exceeds the compose `timeout`, every verdict becomes failure and the signal inverts silently. The ordering is stated in the spec, not only in the compose file, so the constraint survives someone tuning one side.
- **Excluding a route from the access log is a mechanism that could grow** → The delta names two route templates and refuses a general mechanism. The three pinned tests are updated to assert the *exception's* boundary, not merely its existence: a request to any other route still yields an access record.
- **A transition record can be noisy under a flapping dependency** → One record per transition, so a dependency flapping every probe produces two records per interval rather than one. That is a faithful report of a flapping dependency and not a defect; it is also bounded by the interval and therefore small.
- **`/health` has no consumer in this repository** → Accepted and named in decision 7. The alternative — ship `/ready` alone now — costs more later, because the endpoint a future runtime restarts on would then be the one that consults the database.
- **The worker and the notifier remain unprobeable** → Accepted, argued in decision 8, and now visible rather than implicit: the roadmap's open question becomes an answer with a stated cost, and the source-level test keeps the answer from eroding by accident.
- **A probe endpoint is an unauthenticated endpoint on every service** → Mitigated twice: the gateway refuses both paths, and the bodies are fixed per verdict so that a caller who does reach one learns only that the service is or is not ready. What it still reveals, to a caller already inside the deployment network, is that the service exists — which a connection to port 8080 reveals anyway.

## Migration Plan

Additive throughout; no intermediate state is broken.

1. The read-only bucket-presence operation in the storage adapter; then the handlers and the readiness check in each of the three roots, with `setupVideo`'s new return value.
2. The access-log exception and the transition record, with the three pinned tests updated in the same commit as the middleware they pin.
3. The gateway's two refusal blocks.
4. `docker-compose.yml`'s three healthchecks, verified against a stack with a dependency deliberately stopped.
5. The two source-level tests in `cmd/worker` and `cmd/notifier`.

**Rollback**: revert. The change adds two routes, writes no state, migrates no schema, changes no wire format and no existing response, and nothing in the system reads its verdict — so a revert leaves a system identical to today's.

## Open Questions

None blocking. Two are deliberately deferred rather than unresolved:

- **Whether the worker and the notifier eventually get a command-based compose healthcheck.** Deferred because there is nothing useful for such a command to assert today that the broker's own healthcheck does not already report, and inventing one (a heartbeat file, a liveness key in Redis) is a design with its own failure modes. `docs/operations.md`'s gap note is the honest record until then.
- **Whether `/ready` should ever gain a warm-up or startup semantic.** It cannot mean anything while startup is fail-fast: a process that cannot reach a dependency does not start. If startup ever becomes tolerant, this is the first thing to revisit.
