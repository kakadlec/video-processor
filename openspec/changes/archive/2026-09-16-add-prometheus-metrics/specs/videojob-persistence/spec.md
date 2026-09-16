## ADDED Requirements

### Requirement: The Repository Exposes Bounded Aggregates of In-Flight Work

`postgres.Repository` SHALL expose read-only aggregates describing work currently in flight, so that a process which is permitted to serve can report on the progress of processes which are not.

**These aggregates SHALL NOT be added to `domain.VideoJobRepository`.** They are methods on the concrete PostgreSQL repository and on nothing else. Widening the domain port would oblige every existing implementation of it — the cache decorator and the application layer's test doubles — to carry methods that exist for one collector in one process, which is the cost the port exists to avoid. The collector that reads them SHALL therefore be built on the **undecorated** repository, the way the download entitlement lookup already is, and that is a compile-time property rather than a convention: the decorator does not implement these methods, so it cannot be passed where they are required. Should a seam ever be wanted, the idiom this repository already uses is a consumer-declared unexported interface in the reading package (`messaging.outboxClaimer` over `postgres.OutboxRepository`), not a widened domain port.

Two aggregates SHALL exist:

- **Jobs per in-flight state**, together with the age of the oldest job in each state, for the states `queued` and `processing` and for no other. `pending` SHALL be excluded, and on a stronger footing than cost: a job created through the job-lifecycle API has no processing trigger and remains `pending` permanently by design, so a count of them climbs monotonically and describes nothing. The terminal states SHALL be excluded because the interesting quantity for a state a job enters once is a rate rather than a level, and a rate is owed by the process that writes the transition.
- **Unpublished outbox events per event type**, together with the age of the oldest unpublished event in each, restricted to the **explicit set of event types the relays claim on**. It SHALL NOT be computed over every unpublished row: creation events are written to the same table and are claimed by no relay, so they keep `published_at` NULL permanently and by design, and an unrestricted aggregate would report a backlog that grows for the life of the deployment while describing nothing that is pending.

  This aggregate is the reason the requirement exists in this shape. Both relays claim from one table filtered on `event_type`, and the relay carrying terminal outcomes runs inside the worker process. Reading this aggregate from the HTTP service's own pool is therefore how the state of a relay in a process that serves nothing becomes observable at all.

**Each aggregate SHALL return one entry for every member of its label set, whether or not any row matches.** A state or event type with no rows SHALL be reported with a count of **zero** and **no age**, not omitted. This is a correctness requirement rather than a convenience, because the collector reading these aggregates distinguishes *nothing is waiting* from *the value could not be computed* by emitting a sample in the first case and none in the second: an aggregate that simply returned no row for an empty state — which is what a bare `GROUP BY` does — would make an idle system indistinguishable from a failed collection at the only place that distinction is made. The age is the one value legitimately absent when the set is empty, since there is no oldest row for it to describe.

Each aggregate SHALL be computed **in the query**, not by filtering a broader read in Go; completing the closed label set with a zero-valued entry is not such a filter and may be done either in the statement or after it. **An index supporting each predicate SHALL exist** — the same requirement, for the same reason, that the sweeper's scan and the relay's claim already carry: these statements run on a timer for the life of the deployment, and a scan whose cost grows with total job history is the failure mode those indexes exist to avoid.

The unpublished-event predicate is already served by an existing partial index. **The `video_jobs` predicates are not, and both halves need one — this change adds two partial indexes rather than one.** The `queued` predicate is served by nothing at all. The `processing` predicate is served for a *filter* by the sweeper's `(id) WHERE status = 'processing'` index, but that index is keyed by `id` to match the sweep's keyset cursor and therefore cannot answer *the age of the oldest* without reading every matching row; a `(created_at) WHERE status = 'processing'` index is what makes that a single ordered row. So `video_jobs` gains a `(created_at)` partial index per in-flight state, each shaped like the sweeper's index beside them. A job enters and leaves each of them on the same edges by which it enters and leaves the sweeper's, so the write cost is a cost of a shape the schema already accepts; the `processing` rows carry two partial index entries rather than one, which is the price of two different questions — a keyset cursor and an oldest-row lookup — that no single key order answers.

Each in-flight state's predicate SHALL be written as a **literal** in the statement rather than supplied as a parameter, because a partial index whose predicate is a literal is matched only when the planner can prove the query's predicate implies it, which it cannot do for a value it does not yet have. The set of states is closed and small, so this costs one statement or one `UNION ALL` branch per state and buys the index match the requirement above demands.

**The count SHALL saturate at 10,000 per entry and the age SHALL NOT saturate.** The asymmetry follows from the costs rather than from taste: a count over a partial index costs work proportional to the number of matching rows, in exactly the case that repeats on every collection interval for as long as the backlog lasts, whereas the oldest-age lookup is a single ordered row from the same index. The bound is stated as a number rather than left to the implementation because the statement's shape, the gauge's meaning and the test that asserts saturation all depend on the same value, and three artifacts choosing it independently is how they stop agreeing. **10,000** sits roughly three orders of magnitude above any healthy value — the local stack runs three workers at prefetch 1, so a healthy `processing` count is at most three and a healthy `queued` count is single digits — while a bounded index-only read of at most 10,000 entries per entry per scrape stays comfortably under a millisecond. A saturated count still reports *at least this many*, and past the bound it is the age, which does not saturate, that carries how bad it is.

Both aggregates SHALL take a context and SHALL be bounded by it. Neither SHALL be exposed through any HTTP route that returns data to a caller: they are not owner-scoped and describe every user's work by construction.

No caching layer SHALL be introduced for either aggregate, in the repository decorator or anywhere else — a value computed at a different moment from the one the caller asked about is the failure the object-storage reachability check already refuses, and it is least visible in a level.

#### Scenario: Only the in-flight states are aggregated

- **GIVEN** jobs in `pending`, `queued`, `processing`, `completed`, and `failed` status
- **WHEN** the per-state aggregate is read
- **THEN** it reports `queued` and `processing` and reports no other state

#### Scenario: An in-flight state with no rows is reported as zero

- **GIVEN** no job in `queued` status and at least one in `processing`
- **WHEN** the per-state aggregate is read
- **THEN** it still returns an entry for `queued`, with a count of zero and no age, rather than omitting it

#### Scenario: The oldest queued job ages while nothing consumes

- **GIVEN** jobs enqueued and no consumer running
- **WHEN** the per-state aggregate is read repeatedly
- **THEN** the `queued` count holds and the oldest-`queued` age increases with each read

#### Scenario: Creation events are excluded from the outbox aggregate

- **GIVEN** an outbox containing unpublished creation events, which no relay claims, alongside unpublished dispatch and terminal events
- **WHEN** the per-event-type aggregate is read
- **THEN** it reports only the event types the relays claim on, each of them present with a zero count when it has no unpublished row, and the creation events appear in no entry

#### Scenario: The count saturates and the age does not

- **GIVEN** more rows in an in-flight state than the count's stated bound of 10,000
- **WHEN** the aggregate is read
- **THEN** the count reports 10,000 and the age reports the true age of the oldest row

#### Scenario: Each predicate is served by an index

- **WHEN** the query plan for each aggregate is inspected against a populated database
- **THEN** each is served by an index over its predicate, each oldest-age lookup reads a single ordered row from it, and none reads the whole table

#### Scenario: The aggregates are not on the domain port

- **WHEN** the domain repository port and its implementations are inspected
- **THEN** neither aggregate appears on it, no existing implementation or test double carries them, and the cache decorator does not implement them
