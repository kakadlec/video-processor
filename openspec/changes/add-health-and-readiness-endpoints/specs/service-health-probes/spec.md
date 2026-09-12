## ADDED Requirements

### Requirement: Each HTTP Service Serves a Liveness Probe and a Readiness Probe

Each of this repository's three HTTP services — `cmd/identity-api`, `cmd/video-api`, and `cmd/notification-api` — SHALL serve two probe endpoints: a **liveness** endpoint at `GET /health` and a **readiness** endpoint at `GET /ready`.

The two SHALL differ in *criterion*, not only in path. The liveness endpoint SHALL consult no dependency of any kind. The readiness endpoint SHALL consult that service's readiness dependencies as defined below.

The separation is load-bearing and is the reason there are two endpoints rather than one. The action a runtime takes on a failed liveness answer is to restart the process, and restarting a process cannot repair a dependency. A single endpoint that both consults dependencies and drives restarts turns a bounded dependency outage into an unbounded crash loop: every replica is restarted, each restart re-runs a startup sequence that is specified to fail fast on exactly that dependency, and each therefore exits. The outage then outlives the condition that caused it. Two endpoints exist so that the dependency-consulting one can never become the restart trigger.

The liveness endpoint's reach SHALL be understood narrowly and SHALL NOT be described as more than it is: a `200` from it asserts that the process is scheduled, that it is still accepting connections, and that its global middleware chain still returns. It asserts nothing about any dependency, about any other route, or about whether work is progressing.

The two non-HTTP processes — `cmd/worker` and `cmd/notifier` — SHALL serve neither endpoint and SHALL acquire no HTTP surface for this or any other observability purpose. `container-image` already requires that each expose no port at all, and that requirement is not relaxed here.

#### Scenario: Liveness answers while a dependency is unreachable

- **GIVEN** a running HTTP service whose database has become unreachable since it started
- **WHEN** a caller requests `GET /health`
- **THEN** the service answers `200`, having made no call to any dependency

#### Scenario: The two endpoints disagree

- **GIVEN** a running HTTP service whose readiness dependency has become unreachable since it started
- **WHEN** a caller requests `GET /health` and `GET /ready`
- **THEN** liveness answers `200` and readiness answers `503`

#### Scenario: The non-HTTP processes bind no listener

- **WHEN** a source-level test reads the non-test sources of `cmd/worker` and of `cmd/notifier`
- **THEN** it finds neither an HTTP server construction nor an import of the HTTP framework in either, and fails naming the file and line if it does

### Requirement: A Readiness Dependency Is One Whose Absence Stops the Process Serving

A service's readiness endpoint SHALL consult **only** those dependencies whose absence prevents that service from honouring its own contract, and SHALL NOT consult a dependency the system is specified to survive.

This rule is not introduced here. It is the rule this system already applies to its ingress, which asks only whether its own configuration is loadable and deliberately makes no request through to a backend, on the stated grounds that a component is healthy when it can serve and that tying its health to a backend's would make an unrelated service's restart look like a failure of this one. Stating it once, generally, is what makes the per-service answers below derivable rather than enumerated by taste.

Applied to the three services as they are built:

- **PostgreSQL SHALL be a readiness dependency of all three.** Every route that does anything at all reads or writes the service's own context database, and no route is specified to degrade without it.
- **Object storage SHALL be a readiness dependency of `cmd/video-api` and of no other service.** Upload stores into the bucket and both read routes stat objects in it; neither other service holds a client. The check SHALL establish that **the configured bucket is present**, not merely that the object store answers. All three routes name objects inside one configured bucket, so a reachable server whose bucket has been removed fails every one of them while a reachability check still succeeds — a readiness endpoint that reported `200` under that condition would assert precisely the thing that is false.
- **The cache SHALL NOT be a readiness dependency of any service.** Every feature built on it is specified to fail open — the upload idempotency reservation, the per-user rate limiter, and the job status cache each log and proceed on a cache error. A service with the cache down is slower and unmetered and still correct, so reporting it not ready would remove a serving process from rotation for a condition its own specifications call survivable.
- **The message broker SHALL NOT be a readiness dependency of any service.** `POST /upload` commits the transition to `queued` and its outbox row in one database transaction and answers `202` with the broker unreachable; the outbox relay dispatches when the broker returns. That is the design, not a tolerated defect. Independently, no HTTP process holds a broker connection to check — the relay holds configuration and dials inside its own cycle — and the broker health check this repository provides takes no context and therefore could not satisfy the bounding requirement below.

A readiness check SHALL reuse a health check the service's own startup already performs wherever one answers the same question, rather than introducing a second way to ask it. The database check is such a case.

**Object storage is not, and the exception is required rather than permitted.** Startup's object-storage sequence is a reachability check followed by a create-the-bucket-if-absent call, and it is only the second of those that establishes presence. A readiness endpoint SHALL NOT create anything, so it cannot reuse that sequence, and reusing the first call alone would report ready for a reachable object store with no bucket. The readiness check SHALL therefore be a **read-only presence check**: it SHALL determine whether the configured bucket exists, and SHALL NOT create it, SHALL NOT write any object, and SHALL NOT otherwise modify the object store.

The reachability check startup performs SHALL NOT be redefined to assert presence. Startup checks reachability *before* it creates the bucket, so a presence-asserting form of it would make a first start against an empty object store fail fatally on a bucket the next call was about to create — changing what startup verifies, which this capability does not do.

No readiness check SHALL report from a cached or background-refreshed observation: an answer computed at a different moment from the one the caller asked about is the failure mode the object-storage reachability check was specifically written to avoid.

#### Scenario: Readiness with every readiness dependency reachable

- **GIVEN** a running HTTP service whose database — and, for the video service, whose object storage — is reachable
- **WHEN** a caller requests `GET /ready`
- **THEN** the service answers `200`

#### Scenario: Readiness with the database unreachable

- **GIVEN** a running HTTP service whose own context database has become unreachable
- **WHEN** a caller requests `GET /ready`
- **THEN** the service answers `503`

#### Scenario: Readiness with object storage unreachable

- **GIVEN** the video service, whose database is reachable and whose object storage is not
- **WHEN** a caller requests `GET /ready`
- **THEN** it answers `503`, and the two services that hold no object-storage client answer `200` under the same condition

#### Scenario: Readiness with object storage reachable and the bucket absent

- **GIVEN** the video service, whose object store answers but whose configured bucket has been removed since the service started
- **WHEN** a caller requests `GET /ready`
- **THEN** it answers `503`, and the bucket is not created and no object is written while answering

#### Scenario: Readiness with the cache unreachable

- **GIVEN** a running HTTP service whose cache has become unreachable and whose readiness dependencies are all reachable
- **WHEN** a caller requests `GET /ready`
- **THEN** it answers `200`, and no call is made to the cache while answering

#### Scenario: Readiness with the broker unreachable

- **GIVEN** the video service with the message broker unreachable and its readiness dependencies reachable
- **WHEN** a caller requests `GET /ready`
- **THEN** it answers `200`, and `POST /upload` continues to answer `202`

### Requirement: Every Readiness Check Is Bounded, and Bounded Against the Prober

Every check a readiness endpoint performs SHALL be governed by an explicit timeout, and SHALL also honour cancellation of the request that triggered it so that a caller that disconnects releases the work.

That timeout SHALL be shorter than the timeout of the prober configured against the endpoint. The relationship, not either value alone, is what is being required: if a check may run longer than the prober waits, the prober abandons every request and **every verdict becomes a failure regardless of the dependency's state**. The signal does not merely degrade, it inverts, and it inverts without emitting anything that says so. Because the two values are set in different files, the constraint is stated here rather than left implicit in whichever file happens to hold the prober.

Where a service has more than one readiness dependency, its checks SHALL run concurrently, so that the endpoint's worst-case duration is the longest single check rather than the sum of all of them. Sequential checks would make the endpoint's bound a function of how many dependencies a service happens to hold, which is not the quantity the prober's timeout was chosen against.

#### Scenario: A dependency hangs

- **GIVEN** a readiness dependency that accepts a connection and never answers
- **WHEN** a caller requests `GET /ready`
- **THEN** the endpoint answers `503` within its own configured bound rather than waiting on the dependency

#### Scenario: The caller disconnects

- **GIVEN** a readiness check in progress
- **WHEN** the caller closes the connection before it completes
- **THEN** the check is cancelled rather than running to its own timeout

#### Scenario: A service with two readiness dependencies

- **GIVEN** the video service, whose readiness consults both its database and its object storage
- **WHEN** a caller requests `GET /ready`
- **THEN** the two checks are issued concurrently and the response's worst-case duration is that of the slower check, not of both

### Requirement: Probe Endpoints Are Unauthenticated and Disclose Nothing

Both probe endpoints SHALL be registered outside every authenticated route group and SHALL require no bearer token, and SHALL NOT be subject to the per-user rate limiter.

Three reasons, in order of force. The limiter keys on the authenticated subject and a probe carries none, so there is nothing to key on; worse, a probe issued at a fixed interval from inside the limiter would eventually exhaust a budget and be answered `429`, which a prober reads as a failure — the limiter manufacturing the outage it is meant to have no part in. The Identity service mounts neither middleware at all, by design, so an authenticated probe would be authenticated on two services and not the third, which is an accident of where the group exists rather than a policy. And requiring a token would make the Identity service a dependency of every other service's health signal — the precise coupling that distributing verification material by configuration rather than by a network call was chosen to remove.

The response bodies SHALL be fixed per verdict. Neither endpoint SHALL disclose **which** dependency is unavailable, nor any error text, host, endpoint address, connection string, bucket name, version, build identifier, host name, or process instance identifier. The status code SHALL carry the whole answer: liveness answers `200`; readiness answers `200` or `503`.

This follows the non-disclosure posture this system already applies wherever a response's detail would be a probe: the destination policy raises one sentinel rather than one per rule and its rejection says only that the destination was refused, because rules that enumerate internal address space would otherwise be readable by resubmission; and the download route answers a byte-identical `404` for every rejection. A readiness body that names the failing dependency publishes this deployment's dependency inventory and, by repetition, the times at which each part of it is degraded. That is an information-disclosure decision, not a formatting one.

Both responses SHALL carry `Cache-Control: no-store`, so that no intermediary answers a probe from a stored verdict.

The reason a verdict changed SHALL remain recoverable — it moves to the log record required below, whose reader is already inside the deployment — rather than being discarded.

#### Scenario: A probe is made with no credentials

- **WHEN** a caller requests `GET /health` or `GET /ready` carrying no `Authorization` header
- **THEN** the service answers the probe rather than `401`, and the request is not counted against any rate-limit budget

#### Scenario: A readiness failure is reported

- **GIVEN** a service with one readiness dependency unavailable
- **WHEN** a caller requests `GET /ready`
- **THEN** the response is `503` with a fixed body that names no dependency, no error, and no address, and carries `Cache-Control: no-store`

#### Scenario: Two different readiness failures

- **GIVEN** the video service, failing readiness once because of its database and once because of its object storage
- **WHEN** a caller requests `GET /ready` under each condition
- **THEN** the two responses are indistinguishable by status code, body, and headers

### Requirement: A Readiness Verdict Is Recorded When It Changes, Not When It Is Asked For

A readiness endpoint SHALL hold its most recent verdict for the life of the process and SHALL emit exactly one log record each time that verdict **changes** — one when it turns from ready to not ready, and one when it returns. It SHALL NOT emit a record per probe.

The record for a transition to not ready SHALL be at warning severity and SHALL name the dependency or dependencies that failed. The record for a return to ready SHALL be at informational severity. Naming the dependency here is what makes the non-disclosure rule above a *relocation* of the diagnostic rather than a destruction of it: the response body cannot say which dependency failed, and the log, whose reader is already inside the deployment, is where that belongs.

These records SHALL obey `structured-logging` unchanged: a fixed string-literal message and typed scalar attributes only. The dependency names SHALL be drawn from a closed set this repository owns and SHALL NOT be assembled from any caller-supplied value.

The verdict a process holds before its first probe SHALL be **ready**, and that is a consequence of the startup contract rather than an arbitrary seed: startup verifies every readiness dependency and is specified to be fatal, so a process that has reached the point of serving a route had all of them reachable a moment earlier. Seeding it any other way — "unknown", or "not ready until proven otherwise" — makes the first successful probe of every process start look like a recovery from a failure that never happened, and emits a record that means nothing on every deploy.

Concurrent probes SHALL NOT be able to produce more than one record for one transition. The verdict SHALL be updated by an atomic compare-and-set, so that two probers observing the same change between them yield one record and not two.

#### Scenario: A dependency fails and recovers

- **GIVEN** a service probed repeatedly while a readiness dependency fails and later recovers
- **WHEN** the probes run
- **THEN** exactly two records are emitted — one at warning severity naming the failed dependency, one at informational severity on recovery — regardless of how many probes were answered in between

#### Scenario: The first probe after a process starts

- **GIVEN** a freshly started service whose readiness dependencies are reachable
- **WHEN** it answers its first readiness probe
- **THEN** it answers `200` and emits no record, because the verdict has not changed from the one its own successful startup established

#### Scenario: Probes with no change of verdict

- **GIVEN** a service whose readiness verdict is unchanged across many probes
- **WHEN** those probes are answered
- **THEN** no readiness record is emitted for any of them

#### Scenario: Two probers observe the same transition

- **GIVEN** two concurrent readiness requests that both observe the verdict changing
- **WHEN** both complete
- **THEN** one record is emitted, not two

### Requirement: The Probes Are Not Part of the Application's Public HTTP Surface

Neither probe endpoint SHALL be reachable through the application's ingress, on any service.

Without an explicit rule the outcome would be accidental and asymmetric: the ingress routes by path prefix and sends everything it does not otherwise name to the video service, so a probe path on that one service would be publicly reachable while the identical path on the other two would not — a distinction nobody chose. The ingress SHALL therefore refuse both probe paths by exact match, the refusal SHALL carry the **same status code** the ingress produces for a path the application does not serve, and the refused request SHALL reach no service.

**The equivalence required is of the status code, and the reason it is not of the whole response is a constraint rather than a preference.** The ingress generates its own refusal, while an unserved application path is refused by the service behind it and passed back through, so the two bodies differ. Making them byte-identical would require the ingress to intercept and replace the bodies of upstream error responses, which it does not do today and which would apply to **every** upstream `404` in the system — including the download route's, which this repository makes byte-identical across all of its own rejections deliberately, and which would then be rewritten by a routing directive belonging to an unrelated concern. That trade is refused.

What remains distinguishable is that the ingress names these two paths. That discloses the existence of probe endpoints at conventional names and nothing further: no verdict, no failing dependency, and no part of the dependency inventory, each of which is withheld by the non-disclosure requirement above and none of which is reachable through the ingress by any path. The property that carries the weight is that no probe is **answered** through the ingress.

A prober SHALL reach a probe endpoint on the service's own listening port from inside the deployment rather than through the ingress. This SHALL NOT cause any application service to publish a host port, which `container-image` and `development-workflow` both forbid and this capability does not relax.

The requirement that a contributor reaches every route of the application on one host port is unaffected, because a probe endpoint is not a route of the application: it serves no user-facing behaviour, appears in no documented flow, and is consumed by the runtime. That is the same distinction already drawn for a service that serves no application route.

#### Scenario: A probe path is requested through the ingress

- **WHEN** a client requests the liveness or readiness path through the application's ingress
- **THEN** the ingress answers the same status code it answers for a path the application does not serve, and the request reaches no service — no probe handler runs and no readiness dependency is consulted

#### Scenario: A prober inside the deployment

- **WHEN** a prober running alongside a service requests its readiness endpoint on the service's own listening port
- **THEN** the service answers, and no host port is published by that service

### Requirement: The Local Stack Probes Readiness, and Nothing Depends On the Verdict

The local development stack SHALL configure a readiness-based health check for each of the three HTTP services, so that the stack reports whether each service can serve rather than only that its process has not exited.

The check SHALL target the **readiness** endpoint rather than the liveness endpoint: a container that is running but cannot serve is what an operator needs to see, and a liveness-based check would report healthy for a process whose database has vanished — which is the distinction between the two endpoints restated at the point where it has a consequence.

The check command SHALL issue a `GET` and SHALL fail on a non-success status. It SHALL use only tooling already present in the runtime image and SHALL NOT require a new package to be installed into it.

**No service SHALL be made to wait on another service's readiness.** No dependency condition SHALL be declared against these health checks. Making the ingress wait for a backend to be *ready* would tie ingress startup to that backend's own dependencies, which is the coupling the ingress's own health check exists to refuse; and the ingress already tolerates a backend that is not yet up, because it resolves each backend's address per request rather than at configuration load.

The consumer of these verdicts is therefore an operator inspecting the stack, and that SHALL be stated rather than implied: this system has no orchestrator, so a readiness verdict is presently read by a person and by nothing else. The liveness endpoint has no consumer at all in this repository and is served regardless, because the pair is what keeps the criterion legible — a lone readiness endpoint invites whatever arrives next to restart on it, which is the failure the first requirement exists to prevent.

#### Scenario: The stack reports a service that cannot serve

- **GIVEN** the local stack running with one HTTP service's readiness dependency stopped
- **WHEN** an operator inspects the stack's status
- **THEN** that service is reported unhealthy and the others are reported healthy

#### Scenario: A backing dependency restarts

- **GIVEN** the local stack running and healthy
- **WHEN** a readiness dependency is stopped and started again
- **THEN** the affected service reports unhealthy and then healthy again without being restarted, and no other service's state changes

#### Scenario: No service waits on another's readiness

- **WHEN** the stack is started with the documented single command
- **THEN** no service's start is gated on another application service reporting healthy, and the stack comes up as it did before these health checks existed
