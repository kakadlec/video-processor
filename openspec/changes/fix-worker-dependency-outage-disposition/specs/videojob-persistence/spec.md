## ADDED Requirements

### Requirement: A Repository Error Carries the Unavailability Sentinel Only When It Establishes That the Server Could Not Answer

`internal/video/infrastructure/postgres.Repository` SHALL mark with a distinct domain sentinel exactly those failures whose own evidence establishes that **the server did not, or could not, answer the statement**, so a caller can tell *the database could not answer* from *the database answered and the answer could not be used*. Marked errors SHALL be wrapped so the sentinel is reachable by `errors.Is`, with the original error preserved as the wrapped cause.

**The classification SHALL be made from the error's own evidence, and SHALL NOT be derived from which driver method returned it.** Marking whatever a `Scan`, `Query` or `Exec` call returned is a *call-site* classification and not an availability one: each of those calls reports permanent failures through the same return value as transient ones. A `Scan` yields a lost connection and a value-conversion or destination-count failure alike; a `Query` or `Exec` yields a lost connection and a server-answered refusal alike. Marking one of the permanent kinds would tell `videojob-worker` to retry a message that can never succeed, which at a prefetch of one blocks every replica against healthy work — the exact hazard the narrow sentinel exists to prevent, reintroduced by the classification meant to enable it.

**It SHALL be a permission list, not a deny-list.** An error carrying none of the enumerated evidence SHALL NOT be marked. A failure mode nobody has classified therefore behaves as it does today rather than entering a retry loop nobody reasoned about, and a driver upgrade introducing a new error cannot silently widen the retryable set.

The evidence that establishes unavailability is: the connection could not be established; the connection was lost while the statement was in flight; the connection was already closed or is otherwise unusable; or the server itself answered that it cannot serve the request now — resource exhaustion, administrative shutdown, or a refusal to accept connections. The concrete error values and SQLSTATE classes carrying that evidence are a property of the pinned driver rather than of this requirement, and the implementation SHALL verify them against that driver **and against `database/sql`'s own pooling**, which sits between the driver and the caller, rather than assuming they survive it. Evidence that cannot be confirmed to reach a caller SHALL stay out of the list.

**The sentinel SHALL NOT be specified, or read, as a promise that the statement had no effect.** A connection lost in flight leaves the outcome unknown: the statement may have committed. What the sentinel asserts is that *this caller could not learn the outcome*. `videojob-execution` and `videojob-worker` are written against that weaker and true guarantee, and an implementer SHALL NOT strengthen it — in particular SHALL NOT restrict the marking to failures provably raised before the statement was sent, which would exclude most real outages and is a stronger property than any caller needs.

The following SHALL NOT carry the sentinel:

- **An error in which the server answered and refused** — an undefined table or column, an insufficient privilege, a constraint violation, a syntax error. These are permanent on a running database, and the likeliest of them here is a schema a migration left half-applied, which under a call-site rule would mark every call unavailable at once.
- **A failure produced while interpreting a row that was received** — a value conversion, or a scan whose destination count does not match the selected column list. Both are permanent, and the second is reachable through exactly the `SELECT`/`Scan` ordering mistake this adapter already warns about in a comment of its own.
- **A failure to reconstruct the aggregate from stored values** — parsing a stored identifier, user id, filename or storage key, and `RestoreVideoJob` refusing a stored status outside the closed set. These are properties of the stored data and are permanent on a perfectly healthy database. Marking them as unavailability would tell a caller a row will load later when it never will.

**`sql.ErrNoRows` SHALL continue to be mapped to `domain.ErrVideoJobNotFound` before any marking is applied**, and the not-found sentinel SHALL NOT also carry the unavailability sentinel. This ordering is the one detail of this requirement that is easy to get wrong and invisible in a test that only stops the database: a not-found row that carried the unavailability marker would be retried forever by `videojob-worker` instead of dead-lettered.

The contract SHALL hold for the repository's methods as a whole, not only for the methods whose callers currently consult it, so that a reader can tell from the requirement which errors carry the sentinel without auditing call sites. Exactly one caller branches on it today — the claim step `videojob-execution` describes — and that SHALL NOT be read as narrowing the contract.

`internal/video/infrastructure/cache.CachedVideoJobRepository` SHALL pass the sentinel through unchanged on every method it decorates, wrapping or replacing nothing, so that a decorated call and an undecorated one are indistinguishable to a caller testing for it. A decorator that swallowed it would silently restore the defect for whichever call sites read through the cache. Its own cache-store failures SHALL remain best effort and SHALL NOT be reported as repository unavailability: the authoritative store answered, and `videojob-status-cache` requires those failures to be invisible to the caller.

#### Scenario: An unreachable database is reported as unavailability

- **GIVEN** a repository whose database is unreachable
- **WHEN** `FindByID` or `ClaimForProcessing` is called
- **THEN** the returned error carries the unavailability sentinel

#### Scenario: A connection lost in flight is reported as unavailability without asserting the statement had no effect

- **GIVEN** a `ClaimForProcessing` whose connection is lost after the statement is sent
- **WHEN** the caller receives the error
- **THEN** it carries the unavailability sentinel, and the caller SHALL NOT infer from it that the `queued → processing` transition did not commit

#### Scenario: An error the server answered is not reported as unavailability

- **GIVEN** a reachable database whose `video_jobs` table no longer carries a column the adapter selects
- **WHEN** `FindByID` is called
- **THEN** it returns the server's own refusal, and that error does **not** carry the unavailability sentinel, because a retry against a running server cannot change it

#### Scenario: A row that was received but could not be scanned is not reported as unavailability

- **GIVEN** a statement that returns a row whose values `database/sql` cannot convert into the adapter's scan destinations
- **WHEN** the adapter scans it
- **THEN** the returned error does **not** carry the unavailability sentinel, because the server answered and the failure is in interpreting what it sent

#### Scenario: A row that cannot be reconstructed is not reported as unavailability

- **GIVEN** a reachable database holding a `video_jobs` row the aggregate refuses to reconstruct
- **WHEN** `FindByID` is called for it
- **THEN** it returns an error that does **not** carry the unavailability sentinel

#### Scenario: A missing row is not reported as unavailability

- **GIVEN** a reachable database and an identifier no row matches
- **WHEN** `FindByID` is called
- **THEN** it returns `domain.ErrVideoJobNotFound`, and that error does not carry the unavailability sentinel

#### Scenario: The cache decorator does not mask the sentinel

- **GIVEN** the caching repository decorating a repository whose database is unreachable
- **WHEN** a decorated method is called
- **THEN** the error it returns carries the unavailability sentinel, identically to the undecorated repository's

#### Scenario: A cache-store failure is not reported as unavailability

- **GIVEN** a reachable database and an unreachable cache store
- **WHEN** a decorated read or write is performed
- **THEN** it succeeds against the authoritative store and returns no error carrying the unavailability sentinel
