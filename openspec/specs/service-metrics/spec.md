# service-metrics Specification

## Purpose
Defines what this system counts and how it may be counted: which processes expose a metrics endpoint — the three HTTP services on their application port, and the worker and the notifier on a metrics-only listener of their own, with the argument for that listener made rather than cited; that the endpoint is unauthenticated, configured by nothing, and — on the three HTTP services — refused at the ingress and recorded in the access log like any other route, while the metrics-only listener, which no ingress routes, has no access log at all; the label rule, and why it is stricter than the rule logging applies to a record — no identifier in a label at all, because a label value is a series that persists — enforced against the source as a permission with a failing default rather than as a list of forbidden forms; the two variable labels, the matched route and the request method, bounded by the router's own route table so the number of series is a product the build fixes; that a metric family is declared where it is recorded and registered in one explicit registry, never the client library's default; which families exist and the action each informs, with the standing rule that a metric nobody would act on is not added; and how a gauge over persistent state is collected — on scrape, never cached, reporting a failed collection as an error rather than a zero and an empty state as a zero rather than an absence. Neighbouring concerns belong elsewhere: the aggregates those gauges read and the indexes that serve them are defined by `videojob-persistence`; the record format and the probe exemption this endpoint is deliberately not added to by `structured-logging`; and the narrow rule that the metrics-only listener is the worker's and the notifier's only HTTP surface by `service-health-probes` and `container-image`.

## Requirements
### Requirement: Every Application Process Exposes a Metrics Endpoint

Each of this repository's three HTTP services — `cmd/identity-api`, `cmd/video-api`, and `cmd/notification-api` — SHALL expose its collected metrics for scraping at `GET /metrics`, in the Prometheus text exposition format.

The endpoint SHALL be registered on the engine, outside the bearer-authentication group and outside the rate limiter, for the reasons `service-health-probes` already states for the probe endpoints and which apply unchanged: the limiter keys on an authenticated subject that a scrape does not carry and would eventually answer `429`, which a scraper reads as the service being down; one of the three services mounts neither middleware at all, so requiring a token would be a policy on two services and an accident on the third; and requiring a token would make the identity service a dependency of every other service's observability, which is the coupling that distributing verification keys by configuration exists to remove.

The endpoint SHALL be served unconditionally and SHALL NOT be selected, enabled, disabled or reshaped by any environment variable. This follows the record-format rule this system already applies to its logs: one format in every environment, chosen by nothing.

**The two non-HTTP processes — `cmd/worker` and `cmd/notifier` — SHALL each expose `GET /metrics` on a metrics-only listener on port `9102`**, served by the standard library's server with no framework, no probe, no bearer group and no limiter, and SHALL serve nothing else on it. That port SHALL NOT be published to the host and SHALL NOT be routed by the ingress, and the address SHALL be a constant rather than configuration, on the same reasoning that fixes the HTTP services' `8080`. The listener SHALL be bound before the process starts consuming, and a failure to bind it SHALL be fatal at startup, so a missing scrape target is never a silent state that lasts the life of the process. Its errors after startup SHALL be logged and SHALL NOT stop the process, and on shutdown it SHALL be closed within a short bound of its own that does not extend the drain of the work in hand.

The argument for relaxing the prohibition this capability previously held is made here rather than cited. The measurements an operator most needs — how long an extraction takes, how a dispatch was disposed of, how often the recovery sweeper acts, how long a delivery takes against its claim budget — are observable only at sites these two processes alone run, and neither process emits any record on an idle stack, so without an endpoint an idle-but-wedged process is indistinguishable from an idle-and-healthy one from inside it. What the prohibition protected is preserved: neither process is reachable from outside the deployment, neither serves an application route or a probe, and the source-level test each carries still fails on any HTTP server constructed anywhere else in the package and on the HTTP framework anywhere at all (see `service-health-probes`).

No push-based exposition SHALL be introduced instead. A push gateway would avoid the listener and defeat the purpose in this deployment specifically: the worker runs as multiple replicas whose instance identity is resolved per process construction, so every restart of every replica would leave a metric group behind permanently, the count of stale groups growing with the deployment's age rather than with anything real. It would additionally destroy the liveness signal a scrape gives for free and place a single point of failure between the workers and any scrape.

**Those measurements SHALL exist as families declared in the package that records them:** an extraction-duration histogram labelled by outcome, in the frame-extraction adapter; a counter over the worker's dispatch decision table labelled by outcome and message disposition, covering every branch that table distinguishes including the acknowledgement of a dispatch returned to `queued` for a transient object-storage failure; a counter over the recovery sweeper's actions labelled by action, and a histogram of the fence epoch at which a job is requeued, so how much of the bounded requeue budget recovery spends is visible; a histogram of how long the notifier takes to handle one terminal event, labelled by whether it was handled or deferred; and a gauge publishing the notifier's configured maximum claim hold, so that duration can be read against the budget it must stay under. The delivery histogram measures the handling of a whole event rather than each channel's attempt, because the attempt runs inside the Notification application layer, which SHALL NOT import the metrics package and SHALL NOT be handed an instrument through its constructor for that purpose alone — the same reason a logger is not threaded through a constructor that needs one for no other reason.

#### Scenario: A scrape arrives without credentials

- **WHEN** a caller requests `GET /metrics` on any of the three HTTP services with no `Authorization` header
- **THEN** the service answers `200` with the exposition, rather than `401`

#### Scenario: A scrape is not counted against a rate-limit budget

- **GIVEN** an authenticated user whose rate-limit budget for the current window is exhausted
- **WHEN** `GET /metrics` is requested on the same service
- **THEN** it answers `200` rather than `429`

#### Scenario: The non-HTTP processes expose only their metrics listener

- **GIVEN** a running worker or notifier
- **WHEN** `GET /metrics` is requested on its port `9102` from inside the deployment network
- **THEN** it answers `200` with the exposition, any other path on that port is not found, the same port is unreachable from the host, and no request to it passes through the ingress

#### Scenario: The metrics listener cannot be bound

- **GIVEN** a worker or notifier whose metrics port is already in use
- **WHEN** the process starts
- **THEN** it exits with an error record before it consumes any message, rather than running without a scrape target

#### Scenario: The endpoint is not configurable

- **WHEN** the process environment is inspected for a variable that enables, disables, relocates or reformats the endpoint
- **THEN** none exists, and the endpoint is served in the same format by every process that serves it at all

### Requirement: No Metric Label Carries an Identifier or Any Unbounded Value

No metric name, and no metric label name or label value, SHALL carry a user identifier, a job identifier, a delivery identifier, a storage key, a source key, a content hash, a webhook destination, an e-mail address, a secret, an error message, or a request path.

**This is deliberately stricter than the rule the logging capability applies to a record, and the asymmetry is the decision rather than an oversight.** That capability permits a job, user or delivery identifier as a record field, because bounding the *type* of a value is what makes a record safe. A label is not a record field:

- A record is one line that ages out of whatever holds it. A label value creates a **time series that persists for the retention period and is never reclaimed while it is still being written**. One user identifier in a label is one series per user, for as long as that user is active.
- A record's reader is a person reading a stream; a label's reader is a scraper, and every distinct label combination it has ever observed stays queryable. A label is closer to a database column than to a log line.
- Getting a record wrong discloses something. Getting a label wrong discloses something **and** commits unbounded memory in a process nobody would think to inspect.

The rule SHALL be enforced against the **source**, on one walk over every non-test file under `cmd/` and `internal/`, in the same manner and in the same place as the logging capability's three source rules. No behavioural test can hold it: such a test sees only the call sites that existed when it was written. The walk SHALL hold three rules:

1. **Every label value at a call site SHALL be a string literal, or the result of one of a closed, named set of total constructors.** The rule SHALL be written as a **permission with a failing default**: an argument the walk cannot resolve to one of those two forms fails, *including an argument whose syntactic form the walk does not recognize at all*. Enumerating the ways around it is not sufficient and is not what is required here — a walk that inspects the direct varargs, the map literal and the constant-metric form and silently passes everything else is bypassed by a single line, because `WithLabelValues(values...)` spreads a slice and `With(prometheus.Labels(m))` converts a map, and neither contains a literal or a permitted constructor anywhere for the walk to reject. A spread argument, a `prometheus.Labels` argument that is not a composite literal, and any further form a later version of the client library introduces SHALL therefore all fail by default rather than by enumeration. **Label *names* supplied at a call site are held to the same rule**: a key inside a `prometheus.Labels{…}` literal SHALL be a string literal, since a computed key is a label name chosen at runtime and rule 2 covers only the names written at declaration.
2. **Every metric name, help string, and label-name entry SHALL be a string literal** — not a formatted string, not a concatenation, not a named constant. A formatted metric name satisfies every rule about label values while putting the identifier back into the series, which is the same defect the logging capability's string-literal message rule exists to prevent, one level worse because a name is a series too.
3. **The client library's default registry SHALL NOT be used, named, or reached through an automatic-registration helper.** A process-global default registry is mutable state that any transitively imported package can write a metric family into, with no call site in this repository at all. Registration SHALL be into one explicit registry this repository constructs, so that what the endpoint exposes is answerable by reading this repository.

Translating a domain value into a label SHALL therefore be done by an exhaustive branch whose every arm passes a literal, rather than by rendering the value. Rendering a bounded value — a status code, an enumeration's `String()` — SHALL NOT be admitted as an exception: admitting one non-literal because its range happens to be small today is how the rule acquires its first exception, and the next is argued from that precedent rather than from the value.

A runtime sanitizer — stripping, truncating or hashing a label value — SHALL NOT be used as a substitute. Truncation bounds a value's **length** and not the **number of distinct values**, and hashing produces a stable, still-unbounded, and now unreadable series per distinct input.

#### Scenario: A label value is rendered from a value rather than written as a literal

- **WHEN** the source walk reads a call that passes a formatted, converted, concatenated or variable value as a metric label value, in any of the positions a label value can occupy
- **THEN** it fails, naming the file and line, unless that value is the result of one of the named total constructors

#### Scenario: A label value reaches the call site indirectly

- **WHEN** the source walk reads a call that spreads a slice of label values, or that passes a `prometheus.Labels` argument which is not a composite literal, or that writes a computed key into such a literal
- **THEN** it fails, naming the file and line, because the default for an unresolved or unrecognized argument form is rejection rather than silence

#### Scenario: A metric is registered into the library's default registry

- **WHEN** the source walk reads a file that imports the automatic-registration helper, or names the default registerer or gatherer, or calls the package-level registration functions
- **THEN** it fails, naming the file and line

#### Scenario: An identifier reaches the exposition

- **GIVEN** a service that has served requests carrying job identifiers, storage keys and a webhook destination
- **WHEN** the exposition is parsed and every sample's labels are inspected
- **THEN** no label value is an identifier, a key, a hash, an address or a URL

#### Scenario: The walk reads nothing

- **WHEN** the source walk parses no file, because the tree moved or an error skipped every entry
- **THEN** it fails rather than reporting every rule satisfied

### Requirement: The Two Variable Labels Are Bounded by the Router's Own Route Table

Exactly two labels in the metric families this repository declares take a value that is not written as a literal at its call site — the boundary the source walk enforces; the runtime collectors registered alongside them carry the client library's own labels, such as the toolchain version on `go_info`, fixed per build and named as a disclosure under the ingress requirement below — and both come from the same request and are bounded by the same table: **the HTTP route a request matched, and its request method.** Their domain SHALL be the route table the router itself reports, read from the router once at startup after every route is registered. Both are the total constructors the label rule above permits, and they are the only two; the ceiling below is a product over both, so describing the route alone would leave the method outside the contract that bounds it.

The route table SHALL be handed to the shared package in a **representation of this repository's own**, a route and a method per entry, and not in the HTTP framework's type. The framework belongs in the composition roots: nothing under `internal/` imports it today, the platform namespace's own dependency test names that as a rule about this package rather than an accident, and a shared package that named the framework's route type would be the first import of it outside a root — for a value that is two strings. Each composition root therefore translates its own router's table at the point it binds it.

A route value not present in that table SHALL be replaced by a single fixed literal, and a request method not present in it SHALL be replaced by a single fixed literal, reusing the marker the access record already uses for an unrecognized method so that the same fact reads the same in a record and in a series. The resolution SHALL be **fail-closed**: with the table unbound, every route and method resolves to its fallback rather than passing through.

The request path SHALL NOT be used as a label under any circumstance, including the unmatched case. The access record retains a **truncated** path for an unmatched request, deliberately, as the one diagnostic that case gives. A metric SHALL NOT copy that, because truncation bounds a value's length and not the number of distinct values, and an unmatched path is arbitrary caller-supplied text reaching this point before authentication and before the rate limiter — precisely the position from which an unbounded series count is cheapest to create.

The consequence SHALL be that the number of route-labelled series a service can produce is a **product of numbers the build fixes** — the size of the route table plus one, the number of distinct methods plus one, and the number of response classes — rather than a quantity that grows with traffic. That ceiling SHALL be asserted by a test that drives unmatched paths and unrecognized methods and observes that the series count does not grow, rather than argued for in a comment.

#### Scenario: A request matches no route

- **WHEN** requests arrive at many distinct paths that match no route, and with several unrecognized methods
- **THEN** every one is counted under the single fallback route value and the single fallback method value, and the number of series does not grow with the number of distinct paths

#### Scenario: A matched route carries a path parameter

- **WHEN** a request matches a route whose template contains a path parameter
- **THEN** the label carries the route template and not the parameter's value

#### Scenario: The route table was never bound

- **GIVEN** a process in which the route table was not bound at startup
- **WHEN** a request is recorded
- **THEN** it resolves to the fallback route and method values rather than passing the request's own values through

#### Scenario: The shared package does not name the HTTP framework

- **WHEN** the shared metrics package's own dependency test reads its imports
- **THEN** it finds no import of the HTTP framework, and the route table reaches the package as a representation this repository declares

### Requirement: A Metric Family Is Declared Where It Is Recorded

Each metric family SHALL be declared in the package that records it and SHALL register itself into the one process-wide registry. A shared package SHALL NOT be handed a registry through a constructor for the sole purpose of recording, for the same reason a logger is not threaded through one: that is how a bounded context acquires an opinion about its process's observability.

The shared package holding the registry, the exposition handler and the label constructors SHALL contain **no metric family belonging to a bounded context**, because the platform namespace is confined to connection and lifecycle plumbing and may not carry a context's nouns.

Three consequences follow and SHALL be accepted rather than worked around:

- The per-request families are declared once per HTTP composition root, because one composition root cannot import another. This is a bounded duplication of the same kind the bearer-authentication, rate-limit, access-log and probe middlewares already carry, and each copy SHALL carry its own tests. The source rules above are enforced across every copy by one walk, which is what keeps the duplication from becoming three independent vocabularies.
- A family declared in a package that more than one process links SHALL be registered and recorded in every one of them, and each process's endpoint SHALL serve every family that process registered, including a family whose instrumented path that process never exercises. Such a family is served with no series, which costs nothing a scraper stores and keeps registration unconditional rather than a per-process decision.
- A test that reads what a family recorded manipulates process-global state and SHALL NOT be parallel — the same cost the logging capability already names and accepts for its own process-global default.

#### Scenario: A context's family is placed in the shared platform package

- **WHEN** the platform package's dependency test reads its sources
- **THEN** it fails if a metric family named for a bounded context is declared there

#### Scenario: A shared package records in whichever process exercises it

- **GIVEN** a package linked by more than one process
- **WHEN** one of those processes exercises the instrumented path
- **THEN** the sample is recorded in that process's registry and served by that process's own endpoint, and every other process linking the package serves the family without that sample, which is expected rather than a defect

### Requirement: A Metric Exists Only for a Question Someone Would Act On

A metric SHALL be added only where its value would change what an operator does. A metric that restates something another metric already reports SHALL NOT be added, because two numbers that must agree eventually stop agreeing and there is then no way to tell which one is wrong.

The following families SHALL exist, and each is named with the decision it informs:

- **Per-request count, labelled by method, matched route and response class**, and **per-request duration as a histogram**, labelled by method and matched route, on all three HTTP services. The response **class** rather than the numeric status, because rendering the code into a label is exactly the non-literal value the label rule refuses, and the exact code is already in that request's access record.
- **Rate-limit decisions**, labelled by outcome, including a distinct outcome for the fail-open path. This is the family with the strongest claim: the rate limiter is specified to allow the request when its backing store fails, which is a loss of *enforcement* and not of efficiency, and it presently produces one warning record per request — the wrong shape for a question whose answer is a total. The budget is one budget per user across the whole system, so this family is summed across the services that mount a limiter rather than read per service.
- **Upload idempotency reservations**, labelled by outcome, covering **every** branch the upload handler distinguishes at that call site and not only the ones the feature is named for — a reservation taken; a duplicate answered with the existing job; the **conflict** the handler answers `409` with when a reservation it did not win never resolves within its bounded wait; and the fail-open path where the reservation could not be made at all and deduplication is silently off. The conflict outcome is enumerated rather than folded into the duplicate one because the two are different events with different actions: a duplicate is the feature working, and a conflict is a request refused because another request holds a reservation it has not finished — ordinarily rare, and a rising rate of it means a holder is crashing between reserving and finalizing. Omitting it would also make the family fail to account for the decisions actually taken, so the sum of its outcomes would be less than the number of uploads that reached the reservation, with nothing saying where the difference went.
- **Job status cache lookups**, labelled by outcome — hit, miss, and error. The hit ratio is the only evidence the cache earns its complexity, and the error rate reports the health of a dependency that `service-health-probes` deliberately refuses to make a readiness dependency.
- **Aggregates of in-flight work**, as gauges: jobs per in-flight state and the age of the oldest in each, and unpublished outbox events per event type and the age of the oldest in each. These are the requirement below.
- **The runtime's own collectors** — goroutines, heap, garbage collection, file descriptors, process resources — registered explicitly into this repository's registry rather than obtained by using the library's default one. They are the only available signal for a class of failure nothing else here can see, in a system that runs relays, a sweeper, consumers and a lease heartbeat as goroutines.

An authentication-outcome family SHALL NOT be added: the per-request family, on the login route, with a client-error response class, already is that number.

The histogram's buckets SHALL be chosen from what the routes actually do rather than taken from the library's defaults. At least one route streams the request body to object storage, so its handler duration includes the client's own transfer; a bucket set whose highest boundary is below that route's ordinary duration places every real request in the overflow bucket and reports nothing while appearing healthy.

#### Scenario: The rate limiter fails open

- **GIVEN** a service whose rate-limit store is erroring
- **WHEN** authenticated requests are served and allowed through
- **THEN** the fail-open outcome is counted, distinctly from an allowed decision and from a denied one

#### Scenario: A duplicate upload

- **GIVEN** a user who uploads identical bytes twice
- **WHEN** the second request is answered with the first request's job
- **THEN** the duplicate outcome is counted, and the reservation outcome is not

#### Scenario: A reservation that never resolves

- **GIVEN** a user whose identical content is held by a reservation that neither finalizes nor clears
- **WHEN** a second request's bounded wait elapses and it is refused
- **THEN** the conflict outcome is counted, distinctly from a duplicate and from a fail-open

#### Scenario: A second metric for an answered question

- **WHEN** a family is proposed that reports what an existing family already reports
- **THEN** it is not added, and the existing family is named as the reason

### Requirement: A Collected Gauge Queries On Scrape and Never Reports a Value It Does Not Have

A gauge whose value is derived from persistent state SHALL be produced by a collector that runs its query **at scrape time**, bounded by a timeout of its own, against the connection pool the exposing process already holds.

It SHALL NOT report a cached, background-refreshed, or last-known value. An answer computed at a different moment from the one the caller asked about is the failure this system already refuses in its object-storage reachability check, and a gauge is the place where it is least visible.

**On a failure to compute a value, the collector SHALL emit no sample for that family and SHALL report the failure to the scraper**, so that the scrape itself is seen to have failed. It SHALL NOT emit zero. A queued-work gauge reading zero means *nothing is waiting*, which is the most reassuring statement this endpoint can make; emitting it because a query timed out converts a database failure into a healthy-looking dashboard.

**The converse SHALL hold with equal force: on a successful collection the count gauge SHALL carry a sample for every member of its fixed label set, zero-valued where nothing matched.** The two rules are one decision read in both directions, and neither works without the other. Absence is how this collector reports failure, so absence must not also be how it reports emptiness: a `queued` count that simply disappears when the queue drains is indistinguishable from one that disappeared because the database was unreachable, and a series that vanishes and returns is also a series no range query can read across. The **age** gauge is the one value legitimately absent on a successful collection, because an empty state has no oldest row whose age could be reported — so a scrape of a healthy idle system carries every count at zero and no age at all, and that combination is itself the signature of *idle* rather than *broken*.

A count SHALL saturate at the bound the persistence requirement states (10,000) rather than being computed exactly without limit, while an oldest-age gauge SHALL NOT saturate. That requirement owns both the number and the cost argument behind the asymmetry, and this one does not restate either — what belongs here is only what the asymmetry means to a reader of the exposition: a saturated count still reports *at least this many*, and past the bound it is the age, which does not saturate, that distinguishes a busy system from a stopped one.

The collector's bound SHALL be strictly below the timeout of whatever scrapes it. The relationship is stated because only one direction is safe: with the scraper's timeout the larger, a slow dependency produces a failed family the scraper records; with the collector's bound the larger, the scraper abandons every request and the endpoint appears wholly unavailable for a reason that has nothing to do with the endpoint.

A gauge over state shared by every replica of a service SHALL be understood as reported **per replica**: the same fact appears once per instance, and aggregating it across instances by summation overstates it. This SHALL be documented where the gauge is documented, because it is invisible until the service is scaled.

#### Scenario: The query fails

- **GIVEN** a collector whose database is unreachable
- **WHEN** the endpoint is scraped
- **THEN** no sample is emitted for that family, the scrape is reported as failed, and no zero value appears

#### Scenario: An in-flight state is empty

- **GIVEN** a system with no job in one of the in-flight states and a reachable database
- **WHEN** the endpoint is scraped
- **THEN** the count for that state is present with the value zero, and no age sample is emitted for it

#### Scenario: The backlog exceeds the count's bound

- **GIVEN** more rows in an in-flight state than the count's stated bound of 10,000
- **WHEN** the endpoint is scraped
- **THEN** the count reports 10,000 and the oldest-age gauge reports the true age

#### Scenario: A stopped consumer

- **GIVEN** a system where every consumer of dispatched work has stopped while uploads continue to be accepted
- **WHEN** the endpoint is scraped repeatedly
- **THEN** the oldest-queued-age gauge climbs, while every readiness endpoint continues to answer `200` — which is the divergence this gauge exists to make visible

### Requirement: The Metrics Endpoint Is Not Reachable Through the Ingress

The ingress SHALL refuse `GET /metrics` with the status an unserved application path receives, through an exact-match location that proxies to no upstream, so that the refused request reaches no service, runs no handler, and performs no query.

The requirement is **status-code equivalence with an unserved path plus non-arrival**, and explicitly not a byte-identical body. Collapsing the body difference would require intercepting upstream errors at the ingress, which rewrites the body of **every** upstream response of that status in the system — including the one route that makes its own rejections byte-identical on purpose. That trade is refused here as it was refused for the probe paths.

**What the endpoint still discloses to a caller who reaches it from inside the deployment SHALL be stated rather than glossed.** A readiness endpoint has two controls: the ingress refusal and a body that names no dependency. This endpoint has one, and its body is by construction an inventory — every route template the service serves, the error rate of each, the presence and health of its cache and its limiter, the depth of its work pipeline, and the language runtime's version. That is more than a probe discloses, and the posture has loosened.

It is accepted on the same ground the readiness transition record is: the reader is already inside the deployment network, and is the same reader who could enumerate the routes by asking. What SHALL NOT loosen, and what the label rule above makes structural rather than promised, is that **no individual fact is derivable**: the endpoint reports how the system is behaving and nothing whatever about any one user, job, delivery or destination.

#### Scenario: A scrape through the ingress

- **WHEN** `GET /metrics` is requested on the ingress's published port
- **THEN** it is refused with the same status a genuinely unserved application path receives, and no service receives the request — no access record is emitted and no query is made

#### Scenario: A scrape from inside the network

- **WHEN** `GET /metrics` is requested on a service's own port from within the deployment network
- **THEN** it answers `200` with the exposition

### Requirement: The Metrics Endpoint Is Recorded Like Any Other Route

A request to the metrics endpoint SHALL yield an access record, exactly as a request to any non-probe route does. It SHALL NOT be added to the closed exemption the logging capability grants to the two probe route templates.

The exemption's justification does not transfer. It rests on volume from a prober arriving at a fixed interval forever, and on a replacement record that carries more than the one it removes. Neither holds with the weight the probes carry. The local stack's scraper collects each HTTP service every **15 seconds** — about 5,760 access records a day per service — which is the same order of magnitude as the probe traffic already exempted, so volume alone does not separate the two; but a scrape has no verdict, so there is no transition record to offer in exchange, and extending the closed exemption would remove a record without replacing it with one that says more. There is also a positive reason to record it — it is an unauthenticated request from outside the process whose handler queries a database, so its status and duration are the only evidence that a scrape happened and how long collection took.

That arithmetic was done when the scraper was introduced, and the endpoint stays recorded. It SHALL be revisited if a deployment's scrape volume — a shorter interval or many more replicas — makes these records the majority of what a service emits, and that revision SHALL be a change to `structured-logging`'s closed exemption, never a mechanism local to this endpoint. The metrics listener of the worker and the notifier emits no access record at all: neither process has an access log, and this requirement does not create one.

Conversely, the per-request metric SHALL cover **every** route the router serves, including the probe routes and this endpoint, with no exemption list of its own. Three additional route values cost a handful of series, an exemption list is a mechanism that grows, and metering the probes returns in the metric a signal the access record deliberately gave up.

#### Scenario: A scrape is served

- **WHEN** the metrics endpoint is requested
- **THEN** one access record is emitted for it, in the same format and carrying the same fields as any other served route

#### Scenario: A probe is metered though it is not recorded

- **WHEN** a liveness or readiness probe is served
- **THEN** no access record is emitted for it, and the per-request metric counts it under its own route template
