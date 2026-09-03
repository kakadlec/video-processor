## ADDED Requirements

### Requirement: The Claim Is Scoped to an Explicit Set of Event Types

A relay's claim SHALL filter on an explicit, closed set of `event_type` values — the events that relay exists to publish — and a supporting index SHALL exist for that predicate. A relay publishing a single event type SHALL express that as a one-element set; the set SHALL never be widened to "everything unpublished".

This is not an optimization, and it serves three purposes.

It keeps internal events off the broker. `video_job_outbox` has accumulated an unpublished `video_job.created` row for every job created since Phase 3, and those rows are internal events that must never be dispatched. An unfiltered claim would re-read that backlog on every poll and, with a bounded batch, could starve the rows a relay exists to deliver — publishing nothing while appearing to work.

**It isolates dispatch generations, and that is load-bearing during a rolling deploy.** Every replica's relay reads the same `video_job_outbox` table, so a filter shared across generations would let a new replica's relay claim an old replica's row and publish it into the new generation, and an old replica's relay claim a new replica's row and publish it into the old one — where nothing consumes it and the job waits in `queued` forever. Isolating at the exchange cannot help, because the crossing happens before anything is published, and an already-deployed relay cannot be given a new predicate. The current generation's `event_type` SHALL therefore differ from every previous generation's, and a relay SHALL NOT be given a predicate that matches more than its own.

**It keeps two relays off each other's rows.** More than one relay now runs against this table — job dispatch in `cmd/api`, terminal events in `cmd/worker` (`videojob-terminal-events`). Their sets SHALL be disjoint, so neither claims work the other is responsible for and neither's backlog can starve the other's. Concurrency between replicas of the *same* relay remains safe by row locking, unchanged.

Each event-type string SHALL be a single constant shared between the insert, the claim, and the routing key, so the writer, the reader, and the broker cannot drift apart into a relay that silently matches nothing. Where a relay claims more than one type, **the routing key SHALL be read from the claimed row's own `event_type`** rather than fixed per relay, so a message can only ever be published under the key naming what it actually is. The test pinning that equality SHALL cover every type a relay publishes, not one literal pair.

Ordering within a claim SHALL remain oldest-first by `occurred_at` across the whole set.

#### Scenario: Creation events are never dispatched

- **GIVEN** unpublished `video_job.created` rows in the outbox
- **WHEN** any relay runs
- **THEN** none of them is published, and none of their `published_at` values changes

#### Scenario: A backlog of other event types does not starve dispatch

- **GIVEN** more unpublished `video_job.created` rows than the relay's batch size, and one unpublished dispatch row older or newer than them
- **WHEN** the relay runs
- **THEN** the dispatch row is published

#### Scenario: A relay never claims another generation's dispatch row

- **GIVEN** unpublished dispatch rows written under a previous generation's `event_type`
- **WHEN** the current generation's relay runs
- **THEN** none of them is claimed or published, and none of their `published_at` values changes as a result of that relay

#### Scenario: A relay never claims a row outside its own set

- **GIVEN** unpublished rows of an event type another relay is responsible for
- **WHEN** this relay runs
- **THEN** none of them is claimed or published, and none of their `published_at` values changes as a result of that relay

#### Scenario: A multi-type relay publishes each row under its own event type

- **GIVEN** unpublished rows of two different event types within one relay's set
- **WHEN** the relay publishes them
- **THEN** each message's routing key is byte-identical to the `event_type` of the row it came from

## REMOVED Requirements

### Requirement: The Claim Is Scoped to One Event Type

**Reason**: More than one relay now runs against `video_job_outbox`, and one of them — the terminal-event relay in `cmd/worker` (`videojob-terminal-events`) — is responsible for two event types that must reach one queue in order. A per-relay limit of exactly one event type would force either three relay instances and three broker connections for two logical streams, or a fixed routing key that cannot describe the row it is publishing.

**Migration**: Replaced by "The Claim Is Scoped to an Explicit Set of Event Types", which keeps every guarantee of this requirement — internal events stay unpublished, generations stay isolated, and the event type, the claim predicate, and the routing key stay pinned to one constant per type — and adds disjointness between relays plus a routing key read from the claimed row. A relay publishing one event type is unchanged in behavior: it passes a one-element set, and its routing key equals the single `event_type` it claims, exactly as before.
