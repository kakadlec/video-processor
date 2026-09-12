## ADDED Requirements

### Requirement: A Database That Could Not Be Reached Is Distinguishable From a Row That Could Not Be Interpreted

`internal/video/infrastructure/postgres.Repository` SHALL mark its **driver-level** failures with a distinct domain sentinel, so a caller can tell *the database could not answer* from *the database answered and the answer could not be used*.

A driver-level failure is one where the statement did not complete: the connection could not be established or was lost, the query or exec returned an error, or the row scan itself failed. These SHALL be wrapped so the sentinel is reachable by `errors.Is`, with the original error preserved as the wrapped cause.

A failure to **interpret** a row the adapter successfully read SHALL NOT carry that sentinel. Parsing a stored identifier, user id, filename or storage key, and `RestoreVideoJob` refusing to reconstruct an aggregate from stored values, are properties of the stored data and are permanent on a perfectly healthy database. Marking them as unavailability would tell a caller a row will load later when it never will.

**`sql.ErrNoRows` SHALL continue to be mapped to `domain.ErrVideoJobNotFound` before any wrapping is applied**, and the not-found sentinel SHALL NOT also carry the unavailability sentinel. This ordering is the one detail of this requirement that is easy to get wrong and invisible in a test that only stops the database: a not-found row that carried the unavailability marker would be retried forever by `videojob-worker` instead of dead-lettered.

The contract SHALL hold for the repository's methods as a whole, not only for the methods whose callers currently consult it, so that a reader can tell from the requirement which errors carry the sentinel without auditing call sites. Exactly one caller branches on it today — the claim step `videojob-execution` describes — and that SHALL NOT be read as narrowing the contract.

`internal/video/infrastructure/cache.CachedVideoJobRepository` SHALL pass the sentinel through unchanged on every method it decorates, wrapping or replacing nothing, so that a decorated call and an undecorated one are indistinguishable to a caller testing for it. A decorator that swallowed it would silently restore the defect for whichever call sites read through the cache. Its own cache-store failures SHALL remain best effort and SHALL NOT be reported as repository unavailability: the authoritative store answered, and `videojob-status-cache` requires those failures to be invisible to the caller.

#### Scenario: An unreachable database is reported as unavailability

- **GIVEN** a repository whose database is unreachable
- **WHEN** `FindByID` or `ClaimForProcessing` is called
- **THEN** the returned error carries the unavailability sentinel

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
