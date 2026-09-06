## Context

Three bounded contexts, three connection strings, three pools, three `Migrate` calls, three `schema.sql` files with no foreign key between them — and, in every environment this project actually runs, one database called `identity` holding all five tables. `docker-compose.yml` sets six DSNs to it; `.github/workflows/ci.yml` sets three.

The code contract is already right and already written down. `notification-persistence` says a separate variable and a separate pool are what make a context's persistence its own, that pointing them at one server is a permitted deployment decision, and that the code must not be what assumes it. Nothing here changes that contract.

What changes is that the boundary becomes executable. Today a `JOIN` across two contexts' tables is a valid query that passes its test; the rule against it lives only in review attention.

```
                       today                          after
  IDENTITY_POSTGRES_DSN  ─┐                  ─▶ identity
  VIDEO_POSTGRES_DSN     ─┼─▶ identity          ─▶ video
  NOTIFICATION_..._DSN   ─┘   (5 tables)        ─▶ notification
                                                   one server, three databases
```

## Goals / Non-Goals

**Goals:**

- Make a cross-context query fail rather than pass, in local development and in CI, so the boundary is enforced by execution rather than by review.
- Have the arrangement an evaluator reads in `docker-compose.yml` match the isolation the architecture claims.
- Change no variable name, no required-ness, and no startup behaviour — the deployed configuration contract is untouched.
- Leave a developer with an existing volume a documented two-statement recovery, not a volume wipe.

**Non-Goals:**

- Three PostgreSQL *servers*. The boundary being enforced is the database, and a second server proves nothing a second database does not while costing containers on a developer machine.
- Any change to Go code, to `Migrate`, or to the DSN-reading contract. All three adapters already do the right thing.
- Schema-per-context inside one database as an alternative shape (see decision 2).
- Anything about splitting `cmd/api`. That is `split-api-by-bounded-context`, a separate change that neither requires nor is required by this one.

## Decisions

### 1. Separate databases on one server, not separate servers

The property worth having is that a query cannot cross a context boundary. A separate database delivers exactly that: PostgreSQL has no cross-database query without an extension (`dblink`/`postgres_fdw`, neither installed nor wanted here), so the violation fails at parse time rather than returning rows.

A second and third server would add process isolation, failure isolation, and independent tuning — none of which this project is making a claim about, and all of which cost a container each locally and in CI. The change buys the guarantee it is after at the cheapest point on the curve, and `notification-persistence` already licenses the server being shared.

### 2. A database per context, not a schema per context

A PostgreSQL schema is the other candidate boundary, and it is a weaker one for this purpose: a query with a qualified name reaches across schemas freely, so `SELECT ... FROM identity.identity_users` from a Video connection would still work, and enforcement would fall back to `search_path` discipline — a convention again, which is what this change exists to stop relying on. A database is the boundary the engine refuses to cross.

The cost is that each context's `Migrate` now runs against an empty database rather than one already carrying another context's tables. That is already how those functions behave: each creates only what it owns, with `IF NOT EXISTS`, and `internal/notification`'s takes an advisory lock so concurrent first-time creates serialize.

### 3. The init script is not enough on its own, and the change says so

`docker-entrypoint-initdb.d` runs only when the data directory is empty. Every developer with an existing `postgres_data` volume — and CI is not one, since it provisions a fresh service each run — would take the change, run `docker compose up`, and get a startup failure naming a database that does not exist. The obvious fix, `docker compose down -v`, is the one this repository's own compose comments warn against: it removes `minio_data` and `rabbitmq_data` too, destroying completed results and queued messages.

Such a volume holds two of the six databases — `identity`, from the server's `POSTGRES_DB`, and `identity_test`, from today's init script — so the recovery is four `CREATE DATABASE` statements: `video`, `video_test`, `notification`, `notification_test`. Both test databases belong in it. Recovering only the runtime pair restarts the stack and leaves the documented local test command pointing at databases that do not exist, which is a worse failure than the first one because it appears later and looks unrelated.

So the recovery path ships with the change as those four statements run against the live container, and it belongs in the finalization documentation rather than being left for each developer to derive from an error message.

### 4. The test databases move with the runtime ones

Today `identity_test` is one database shared by every adapter suite, and `docker-compose.yml`'s comment explains why it exists at all: the adapter tests `TRUNCATE` their tables, so sharing with the running app would wipe real data. That reasoning is per-context, not global — a Video adapter test truncating `video_jobs` has no business being in the same database as Notification's rows either. Three test databases, one per context, keeps the original rationale and extends it to the boundary this change is about.

CI matters more than local here: CI is where a cross-context query would otherwise pass silently on every pull request, since its Postgres service is provisioned fresh and its three `*_TEST_DSN` values currently name one database.

## Risks / Trade-offs

- **A developer's next `docker compose up` fails after pulling this change** → Real, and the honest cost of the change. Mitigated by decision 3: the four `CREATE DATABASE` statements — two runtime, two test — are documented, and the failure names the missing database clearly. Not mitigated by trying to create databases from Go, which would require a connection to a database the process does not own.
- **A latent cross-context query is discovered by this change and fails the suite** → That is the change working, not a regression. There is reason to expect none — no `schema.sql` declares a foreign key and `internal/notification/dependency_rules_test.go` already forbids the import that would make one convenient to write — but the verification step is what settles it, and a finding here is the most valuable outcome this change could have.
- **Someone later re-converges the DSNs to "simplify" the compose file** → The `ddd-architecture` requirement added by this change makes that a spec violation rather than a tidy-up, and the reason is written where the values live.
- **Three databases is a step toward per-context servers nobody asked for** → It is not, and the spec text says so: the requirement is about a query being unable to cross, not about physical topology. One server stays explicitly permitted.
