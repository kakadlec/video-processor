## ADDED Requirements

### Requirement: The Repository Exposes Bounded Aggregates of In-Flight Work

`postgres.Repository` SHALL expose read-only aggregates describing work currently in flight, so that a process which is permitted to serve can report on the progress of processes which are not.

Two aggregates SHALL exist:

- **Jobs per in-flight state**, together with the age of the oldest job in each state, for the states `queued` and `processing` and for no other. `pending` SHALL be excluded, and on a stronger footing than cost: a job created through the job-lifecycle API has no processing trigger and remains `pending` permanently by design, so a count of them climbs monotonically and describes nothing. The terminal states SHALL be excluded because the interesting quantity for a state a job enters once is a rate rather than a level, and a rate is owed by the process that writes the transition.
- **Unpublished outbox events per event type**, together with the age of the oldest unpublished event in each, restricted to the **explicit set of event types the relays claim on**. It SHALL NOT be computed over every unpublished row: creation events are written to the same table and are claimed by no relay, so they keep `published_at` NULL permanently and by design, and an unrestricted aggregate would report a backlog that grows for the life of the deployment while describing nothing that is pending.

  This aggregate is the reason the requirement exists in this shape. Both relays claim from one table filtered on `event_type`, and the relay carrying terminal outcomes runs inside the worker process. Reading this aggregate from the HTTP service's own pool is therefore how the state of a relay in a process that serves nothing becomes observable at all.

Each aggregate SHALL be computed **in the query**, not by filtering a broader read in Go, and **an index supporting each predicate SHALL exist** — the same requirement, for the same reason, that the sweeper's scan and the relay's claim already carry: these statements run on a timer for the life of the deployment, and a scan whose cost grows with total job history is the failure mode those indexes exist to avoid. The `processing` and unpublished-event predicates are already served by existing partial indexes; the `queued` predicate is not, and a partial index SHALL be added for it, shaped like the one beside it. A job enters and leaves that index on the same edges by which it enters and leaves the `processing` one, so the write cost is a cost of a shape the schema already accepts.

**The count SHALL saturate at a stated bound and the age SHALL NOT.** The asymmetry follows from the costs rather than from taste: a count over a partial index costs work proportional to the number of matching rows, in exactly the case that repeats on every collection interval for as long as the backlog lasts, whereas the oldest-age lookup is a single ordered row from the same index. A saturated count still reports *at least this many*, and the age is the value that distinguishes a busy system from a stopped one.

Both aggregates SHALL take a context and SHALL be bounded by it. Neither SHALL be exposed through any HTTP route that returns data to a caller: they are not owner-scoped and describe every user's work by construction.

`CachedVideoJobRepository` SHALL pass both straight through without caching, as it already does for the other multi-row reads, and no caching layer SHALL be introduced for them elsewhere — a value computed at a different moment from the one the caller asked about is the failure the object-storage reachability check already refuses, and it is least visible in a level.

#### Scenario: Only the in-flight states are aggregated

- **GIVEN** jobs in `pending`, `queued`, `processing`, `completed`, and `failed` status
- **WHEN** the per-state aggregate is read
- **THEN** it reports `queued` and `processing` and reports no other state

#### Scenario: The oldest queued job ages while nothing consumes

- **GIVEN** jobs enqueued and no consumer running
- **WHEN** the per-state aggregate is read repeatedly
- **THEN** the `queued` count holds and the oldest-`queued` age increases with each read

#### Scenario: Creation events are excluded from the outbox aggregate

- **GIVEN** an outbox containing unpublished creation events, which no relay claims, alongside unpublished dispatch and terminal events
- **WHEN** the per-event-type aggregate is read
- **THEN** it reports only the event types the relays claim on, and the creation events appear in no row

#### Scenario: The count saturates and the age does not

- **GIVEN** more rows in an in-flight state than the count's stated bound
- **WHEN** the aggregate is read
- **THEN** the count reports the bound and the age reports the true age of the oldest row

#### Scenario: Each predicate is served by an index

- **WHEN** the query plan for each aggregate is inspected against a populated database
- **THEN** each is served by an index over its predicate, and none reads the whole table
