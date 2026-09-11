## MODIFIED Requirements

### Requirement: HTTP Request and Panic Records Are Records Like Any Other

Every HTTP service SHALL emit its per-request access record and its recovered-panic record through the same logger, in the same format, as every other record it emits. Neither SHALL be produced by the HTTP framework's own logging middleware.

An access record SHALL carry the request method, the matched route, the response status, the request's duration, the response size, and — where the request carries an authenticated subject — that subject.

An access record SHALL NOT carry the request's query string, any request or response header, or any part of either body.

The **request method** is caller-supplied on the request line and is not drawn from a closed set by the transport: an unmatched request may carry an arbitrary token of arbitrary length, and it reaches the access record before any authentication or rate limit. It SHALL therefore be recorded verbatim only when it is a recognized HTTP method, and otherwise replaced by a fixed marker. Bounding the path while logging the method unchecked would leave the record unbounded on the same request, through the field beside it.

The **matched route** is the route template, which is bounded by the router's own definition and is therefore the field that can always be recorded. The **request path** SHALL be recorded only when no route matched, and SHALL be truncated to a fixed bound before it is. The distinction is load-bearing rather than fussy: the access middleware runs for unmatched requests too, so on that path the value is arbitrary caller-supplied text of arbitrary length — the same objection that excludes the query string, which would otherwise be excluded on a rule the path escapes. Retaining a bounded copy for the unmatched case keeps the one diagnostic that case exists to give, which is what was asked for.

Nothing is lost for a matched request: its path adds only the parameter values, and every one of them is already recorded elsewhere by the handler that used it.

**Exactly one class of request SHALL be exempt from the access record: a request that matched one of the two probe route templates `service-health-probes` defines.** The exemption is stated as a closed list of those two route templates and no other, and there SHALL be no general mechanism — no configurable exclusion list, no middleware option, no per-route opt-out — by which any further route can leave the access log. The distinction matters more than the exemption: a general mechanism would let a future route stop being recorded without that ever being reviewed as a change to this capability, and the value of the rule above is that it has no escape hatch.

The exemption is justified by what the excluded records would contain, not by their number alone. A liveness probe consults nothing, so its record's status is `200` and its duration near zero on every occurrence; the record varies in no field and therefore carries no information. A readiness probe's status does vary, but the informative event is a *change* of verdict rather than a verdict, and a change is recorded by `service-health-probes` at the moment it happens, at a severity that reflects it, naming the dependency that failed — which an access record cannot do at all. The exemption therefore does not remove a diagnostic; it replaces a per-request record that says nothing with a per-transition record that says more.

The volume is the reason the choice cannot be deferred rather than a reason on its own: probes arrive at a fixed interval forever, across every HTTP service, and would become the overwhelming majority of every record this system emits — in a system whose entire migration to structured records catalogued 170 emitting call sites. Recording them at a severity below the default threshold was considered and SHALL NOT be used as the mechanism: records invisible at the default setting satisfy this requirement only in letter, and they reappear in bulk exactly when an operator lowers the threshold to investigate something else.

A recovered panic SHALL be recorded at error severity with the panic value and the stack as fields, and SHALL still produce the response the service produced before. This SHALL hold for **every** recovered panic, including one raised while serving a probe route and including one caused by a connection the client has already dropped — a case some framework recovery middleware handles on a separate branch that never reaches the supplied handler, and which would otherwise be the one class of panic recorded nowhere. **The access-record exemption above SHALL NOT extend to the panic record**: what is exempt is the routine per-request record, not the report of a failure.

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

#### Scenario: A probe route is served

- **WHEN** any HTTP service answers a request that matched either of the two probe route templates
- **THEN** it emits no access record for that request

#### Scenario: Every other route still yields an access record

- **WHEN** any HTTP service answers a request that matched a route other than the two probe routes, or a request that matched no route at all
- **THEN** it emits one access record, so that the exemption is bounded to the two named templates rather than to a class a later route can join

#### Scenario: A handler panics

- **WHEN** a handler panics and the recovery middleware runs
- **THEN** an error-severity record carries the panic value and the stack, the client receives the same response as before, and no other output is produced

#### Scenario: A handler panics on a connection the client has dropped

- **WHEN** a panic is recovered for a request whose connection is already broken
- **THEN** it is recorded like any other recovered panic, and no response body is attempted

#### Scenario: A probe handler panics

- **WHEN** a panic is recovered while serving one of the two probe routes
- **THEN** it is recorded like any other recovered panic, the access-record exemption notwithstanding
