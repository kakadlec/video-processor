# structured-logging Specification

## Purpose
Defines what every process this repository builds emits as diagnostic output: one machine-readable format on one destination that no configuration selects, the two identity fields that say which service and which of its processes wrote a record, where the logger is built and why no package under `internal/` may build one or read configuration for it, the source-level rules that keep an arbitrary value — and an assembled message — out of a record at all, what an HTTP access record and a recovered-panic record carry and what they deliberately omit, the one field that makes a job followable across every process that handles it, and the per-process severity threshold. Three neighbouring concerns belong to other specifications: what a recorded *reason* on the delivery path may be built from is defined by `notification-webhook-delivery` and `notification-email-delivery`; the signing secret's own non-disclosure by `notification-preferences`; and the processes and replica counts these records are attributed to by `ddd-architecture` and `container-image`.
## Requirements
### Requirement: Every Process Emits Machine-Readable Records

Every process this repository builds — `cmd/identity-api`, `cmd/video-api`, `cmd/notification-api`, `cmd/worker`, and `cmd/notifier` — SHALL emit its diagnostic output as structured records through `log/slog`, and SHALL NOT write diagnostic output through the standard library's package-level `log` functions, through `fmt.Print*`, or through any other unstructured path.

A record SHALL carry, at minimum, a timestamp, a severity level, a message, and the emitting service's identity. Information that identifies what a record is *about* — a job identifier, a lease epoch, a storage key, a delivery identifier, an attempt number, an event type, a channel — SHALL be carried as a named field and SHALL NOT be interpolated into the message.

The message SHALL be a fixed string literal for a given call site: it SHALL NOT be assembled by formatting or concatenating a value into it, so that records from one call site remain groupable after their fields change. This SHALL be enforced by the same source-level walk that enforces the attribute rules below — a formatted message satisfies every rule about attributes while putting an identifier back inside the message, which is the exact defect this capability exists to remove, so an unenforced clause here would let the migration land its own regression.

#### Scenario: A record carrying an identifier

- **WHEN** any process logs an event concerning a specific job, delivery, or stored object
- **THEN** that identifier appears as a named field of the record, and the record's message is the same fixed string for every occurrence of that event

#### Scenario: A message is assembled rather than fixed

- **WHEN** a non-test source file passes anything but a string literal in a log call's message position
- **THEN** the source-level walk fails and names the file and the call site

#### Scenario: No unstructured output path remains

- **WHEN** a source-level test walks the syntax of every non-test `.go` file under `cmd/` and `internal/`
- **THEN** it finds no import of the standard `log` package at all, and no call to `fmt.Print`, `fmt.Printf`, `fmt.Println`, or an `fmt.Fprint*` whose destination is standard output or standard error, and it fails naming the file and line if it does

The ban is on **this repository's own code**, which is the scope the walk covers: every non-test `.go` file under `cmd/` and `internal/`. A dependency may still call the standard `log` package — `net/http` does, for an accept error, a TLS handshake failure or a panic it recovers, on every server that leaves `ErrorLog` nil, as all three of this repository's do — and that output is not an unstructured path here: `slog.SetDefault` also redirects the standard library's default logger through the same handler, so such a line is written to standard output as a JSON record carrying this process's `service` and `instance`. Two properties of that bridge are stated rather than assumed. The record's whole text is its message, with no typed attributes, because the source-level walk cannot reach a call site inside a dependency. And the bridge is pinned at informational severity whatever the line reports, so a process configured above that threshold discards those reports entirely rather than recording them at the severity they deserve.

The `log` rule is an **import** ban rather than a list of function names, deliberately. Six of the calls this capability replaces are already made on a `*log.Logger` **instance** rather than through the package's functions, so a name-based check would miss them today and would keep missing the next one: a `log.New` plus a method call on the result reintroduces the entire unstructured path while every enumeration of `log.Print`, `log.Fatal` and `log.Panic` stays green. Banning the import closes both forms with one rule and needs no maintenance as the standard library grows.

Value-producing calls (`fmt.Errorf`, `fmt.Sprintf`) are unaffected: they build a value, they do not emit output.

#### Scenario: A startup precondition that cannot be met

- **WHEN** a process cannot satisfy a startup precondition that is documented as fatal
- **THEN** it emits a record at error severity naming the precondition and then exits non-zero as an explicit, separate step, rather than through a helper that logs and exits in one call

### Requirement: Every Record Names the Process That Emitted It

Every record SHALL carry a field identifying the service that emitted it **and** a field identifying the individual process instance, both bound once at that process's startup rather than supplied by each call site. A shared `internal/` package logging on behalf of a process SHALL inherit both without being told what process it is running in.

Two fields, not one, because they answer different questions and the second cannot be derived from the first. `docker-compose.yml` runs **three replicas of `cmd/worker`**, and they all carry the same service name: attributing a record to "a worker" does not say which of the three, which is precisely what is needed when one replica misbehaves or when two are seen working on the same job. The instance identifier SHALL be stable for the life of the process, SHALL be resolved once at startup rather than per record, and SHALL identify the **process**, not merely the machine it runs on. A hostname alone does not: under Docker each container has its own, but the documented `go run ./cmd/worker` path runs binaries directly, where two workers on one host share a hostname and would be indistinguishable — the exact case this field exists for. It SHALL therefore combine the hostname with a per-process component (the process identifier, or a value generated at startup), so that two processes of the same binary on one host always differ.

The service identifiers SHALL be a closed, canonical set fixed in one place: `identity-api`, `video-api`, `notification-api`, `worker`, `notifier`. These are the binary names, which are also the deployment's own service names, so a filter written against a record matches what an operator already types. They SHALL NOT carry the `cmd/` prefix, which names where the source lives rather than what is running.

This is what makes one aggregated stream readable: `docker-compose.yml` runs three replicas of `cmd/worker` and five services in total, all writing to the same collected output, and without it a record cannot be attributed to its source.

#### Scenario: A shared package logs

- **WHEN** a package under `internal/` emits a record while running inside the worker process
- **THEN** the record's service field is `worker`, and the same package running inside the video API names `video-api` instead — the value drawn from the canonical set above in both cases

#### Scenario: Concurrent replicas

- **WHEN** more than one replica of the same service runs against one collected stream
- **THEN** every record can be attributed to both the service and the individual replica that produced it, and two records from different replicas are distinguishable by field rather than by inference

#### Scenario: Two processes of one binary on one host

- **WHEN** two processes of the same binary are started directly on one machine, sharing a hostname
- **THEN** their records still carry different instance values

### Requirement: One Record Format In Every Environment

The record format SHALL be JSON, in every environment, and SHALL NOT be selectable by configuration. No alternative human-readable or console format SHALL be offered. Records SHALL be written to **standard output**, and that destination is part of this requirement rather than an implementation detail: this document is what survives the change, so a destination left unconstrained here would let a later edit move the stream back to standard error while every permanent requirement still passed. Standard output also matches the gateway's own split, where the access log goes to standard output and only the runtime's own errors to standard error.

The reason is not presentation. The non-disclosure guarantee below is a property of how a specific encoder reaches a value, and the two candidate encoders reach values by opposite paths — verified against the implementation rather than assumed. A JSON encoder walks a value's exported fields directly, so a domain value that declines to defend itself is serialized verbatim; a text renderer consults the type's own rendering, where `internal/notification/domain.Secret`'s defences live. Conversely, a value the JSON encoder refuses does not discard the record: the attribute becomes an error marker and every sibling field nested inside it is lost, which the text renderer would have shown. The two therefore fail in opposite directions, and a configurable format would mean the guarantee holds under whichever encoder was verified and is untested under the other — in the environment where a developer is least likely to be watching for it.

#### Scenario: The format cannot be switched

- **WHEN** a process starts under any configuration this repository documents
- **THEN** it emits JSON records on standard output, and no configuration value changes either the format or the destination

### Requirement: The Logger Is Configured Once Per Composition Root

Each composition root SHALL construct its logger — format, severity threshold, destination, and service identity — as part of its own startup, and SHALL install it as the process-wide default. No package under `internal/` SHALL construct a logger, read logging configuration, or decide a severity threshold.

Where a package already accepts an injected logger so that its own tests can read what it writes, it SHALL continue to, and SHALL treat a nil logger as the process-wide default. That injected logger SHALL be a structured one: the six records that reach it today are emitted through a standard `*log.Logger` instance, which is exactly the form the import ban above closes. Injection SHALL NOT be introduced anywhere it is not already present: threading a logger through constructors that do not need one would be the mechanism by which a package acquires an opinion about its process's logging, which the first paragraph forbids.

#### Scenario: A shared package needs no configuration

- **WHEN** a package under `internal/` logs
- **THEN** it does so through the process-wide default and reads no environment variable to do it

#### Scenario: A test reads what a use case writes

- **WHEN** a use case that accepts an injected logger is constructed with one in a test
- **THEN** its records go to that logger, and constructing it with nil sends them to the process-wide default instead

### Requirement: No Domain Value Is Passed To a Log Call

A log call site SHALL build every field from a scalar it has extracted itself — a string, an integer, a boolean, a duration, or a time. It SHALL NOT pass a value of arbitrary type to the logger, and SHALL NOT rely on a type rendering itself for the log.

Concretely: every argument a log call passes after its message SHALL be a typed attribute constructor, and so SHALL every argument to a call that **binds attributes to a logger** for later records rather than emitting one — the attribute-binding call specifically, not the one that opens a named group, which takes a name and binds no value. The second half is not a refinement of the first: a binding call takes no message, so a rule phrased only in terms of arguments-after-the-message does not reach it — and a domain aggregate bound there would be attached to every subsequent record the logger emits, which is a wider leak than a single call site, arriving silently. The loosely-typed alternating key-and-value form that `slog`'s top-level functions also accept SHALL NOT be used, because its value position takes any type and is therefore the same hole as an arbitrary-value attribute. Requiring typed attributes is a constraint on the arguments, not on which logging function is called: the ordinary `Info`/`Warn`/`Error` calls accept typed attributes directly, so no call site is obliged to use the attribute-only variant.

This SHALL be enforced at the source level, by a test that walks the syntax of every non-test file under `cmd/` and `internal/` and fails on any field constructed from an arbitrary value or passed in the alternating form, whether it is emitted directly or bound to a logger first. The service and instance identities the composition roots bind at startup SHALL themselves be bound as typed attributes, so the rule holds with no exemption for the code that establishes it. A behavioural test cannot hold this claim: it can only observe the call sites that exist when it is written.

The rule is deliberately stronger than redacting known-sensitive types, and for two reasons rather than one.

A value that **does** defend itself is not the problem. `internal/notification/domain.Secret` refuses to be marshalled, and under a JSON encoder that refusal costs the whole attribute — the record is still emitted, with an error marker in that field's place, and every sibling field nested inside it lost with it. That is a silent diagnostic loss, not a leak.

A value that does **not** defend itself is the problem. A JSON encoder walks exported fields directly, with no opportunity for the type to intervene, so any domain value carrying a user-supplied connection target is written out verbatim — a destination URL's query string included, which is the one place this system documents a credential legitimately living. Foreclosing the arbitrary-value path removes both questions instead of answering them per type, and it is the only formulation that covers the types nobody has thought to defend yet.

This rule governs how a *value* reaches a record. It does not govern an error's own text, which remains subject to the existing prohibitions in `notification-webhook-delivery` and `notification-email-delivery`: a recorded reason and every log line on the delivery path are built from a classified error of this system's own, never from a transport error, whose `*url.Error` rendering would carry a destination's query string.

#### Scenario: An aggregate is passed to a log call

- **WHEN** a non-test source file constructs a log field from a value that is not one of the permitted scalar kinds
- **THEN** the source-level test fails and names the file and the call site

#### Scenario: An aggregate is bound to a logger rather than logged

- **WHEN** a non-test source file binds an attribute to a logger for reuse and does so from a value that is not one of the permitted scalar kinds
- **THEN** the source-level test fails, exactly as it does for a value passed to an emitting call

#### Scenario: A preference is logged

- **WHEN** any process logs an event concerning a notification preference
- **THEN** the record carries the `(user, event type, channel)` triple as three separate scalar fields, and carries no representation of the preference aggregate and no representation of its secret

### Requirement: HTTP Request and Panic Records Are Records Like Any Other

Every HTTP service SHALL emit its per-request access record and its recovered-panic record through the same logger, in the same format, as every other record it emits. Neither SHALL be produced by the HTTP framework's own logging middleware.

An access record SHALL carry the request method, the matched route, the response status, the request's duration, the response size, and — where the request carries an authenticated subject — that subject.

An access record SHALL NOT carry the request's query string, any request or response header, or any part of either body.

The **request method** is caller-supplied on the request line and is not drawn from a closed set by the transport: an unmatched request may carry an arbitrary token of arbitrary length, and it reaches the access record before any authentication or rate limit. It SHALL therefore be recorded verbatim only when it is a recognized HTTP method, and otherwise replaced by a fixed marker. Bounding the path while logging the method unchecked would leave the record unbounded on the same request, through the field beside it.

The **matched route** is the route template, which is bounded by the router's own definition and is therefore the field that can always be recorded. The **request path** SHALL be recorded only when no route matched, and SHALL be truncated to a fixed bound before it is. The distinction is load-bearing rather than fussy: the access middleware runs for unmatched requests too, so on that path the value is arbitrary caller-supplied text of arbitrary length — the same objection that excludes the query string, which would otherwise be excluded on a rule the path escapes. Retaining a bounded copy for the unmatched case keeps the one diagnostic that case exists to give, which is what was asked for.

Nothing is lost for a matched request: its path adds only the parameter values, and every one of them is already recorded elsewhere by the handler that used it.

A recovered panic SHALL be recorded at error severity with the panic value and the stack as fields, and SHALL still produce the response the service produced before. This SHALL hold for **every** recovered panic, including one caused by a connection the client has already dropped — a case some framework recovery middleware handles on a separate branch that never reaches the supplied handler, and which would otherwise be the one class of panic recorded nowhere.

No recovered panic SHALL produce output outside the record. A framework's own recovery middleware that writes its stack block to a writer of its own before delegating SHALL NOT be used, because that block is unstructured output the format requirement above forbids, and it is emitted whether or not the delegate also records the panic.

#### Scenario: A request is served

- **WHEN** any HTTP service answers a request that matched a route
- **THEN** it emits one access record in the same format as its other records, naming the method, matched route, status, duration, size, and the authenticated subject when there is one — and not the request path

#### Scenario: A request matches no route

- **WHEN** a request arrives for a path no route matches, of any length, carrying a method that is not a recognized HTTP method
- **THEN** the access record carries the request path truncated to the fixed bound and a fixed marker in place of the method, and the record's size is bounded regardless of the request's

#### Scenario: A request carries a query string

- **WHEN** a request arrives with a query string
- **THEN** no part of it appears in the access record

#### Scenario: A handler panics

- **WHEN** a handler panics and the recovery middleware runs
- **THEN** an error-severity record carries the panic value and the stack, the client receives the same response as before, and no other output is produced

#### Scenario: A handler panics on a connection the client has dropped

- **WHEN** a panic is recovered for a request whose connection is already broken
- **THEN** it is recorded like any other recovered panic, and no response body is attempted

### Requirement: A Job Is Followable Across the Processes That Handle It

A job's identifier SHALL appear as a field in at least one record emitted by every process that acts on it: the service that accepted it, the worker that ran it, and the notifier that announced its outcome. Selecting on that one field SHALL therefore reconstruct the job's path through the system from the collected stream alone.

This holds for the worker and the notifier as the code already stands. It does **not** hold for the accepting service: on a successful upload every existing log call in that handler is on an error path, and the identifier of the job just created reaches the client in the response body and appears in no record. One record is therefore added — the accepted job, with its identifier and its source key — and it is the only **application-level job-flow** record this capability adds. It is emitted where a request queues a new job; a duplicate upload, which is answered with the same `202` naming the job the first request created, emits none of its own, and needs none, because that first request already recorded the identifier. The capability's other new records carry no job identifier and are specified elsewhere in this document: one bootstrap refusal per composition root for an unparseable severity, and one access record and one recovered-panic record per HTTP service.

It is added rather than the requirement narrowed because a service that accepts work and says nothing about it is the observability gap this capability exists to close, and because without it the end-to-end claim cannot be checked at all: the two processes that do log the identifier both learn it from a message, so a job that is accepted and never dispatched leaves no trace of having been accepted.

#### Scenario: A job is followed end to end

- **WHEN** an upload is accepted, processed, and announced, and the collected stream is filtered on the job's identifier
- **THEN** records from the accepting service, the worker, and the notifier are all returned

#### Scenario: An accepted job that is never dispatched

- **WHEN** a job is accepted but no worker ever consumes it
- **THEN** the accepting service's record still shows the job was accepted, and names it

### Requirement: Severity Threshold Is Per-Process and Optional

Each process SHALL accept an optional configuration value setting its own minimum severity, defaulting to informational when it is absent. A value that cannot be parsed SHALL be refused at startup rather than silently replaced by the default.

Refusing it SHALL itself be reported as a structured record. The severity is the one setting that must be read before the logger it configures exists, and the tempting shortcut — write the complaint to standard error and exit — would put the one failure a starting operator most needs to see outside the format everything else uses. A process in that state SHALL instead build a logger at the default severity, carrying the same service and instance identity as a normal one, emit the refusal through it, and exit non-zero.

The threshold is deliberately per-process and carries no cross-service consistency obligation, unlike the rate-limit configuration, which `rate-limiting` requires to hold one value everywhere because it governs one shared per-user budget. Raising one process's verbosity while leaving the others alone is the ordinary use of this value, not a misconfiguration.

#### Scenario: The value is absent

- **WHEN** a process starts with no severity configured
- **THEN** it emits informational records and above

#### Scenario: The value is unparseable

- **WHEN** a process starts with a severity value it cannot parse
- **THEN** it emits a structured error-severity record naming the offending value, carrying the same service and instance fields as any other record, and exits non-zero — rather than starting at the default, and rather than complaining in an unstructured form

#### Scenario: Two processes are configured differently

- **WHEN** one process is configured for debug severity and another is left at the default
- **THEN** both are correct, and no check requires them to agree
