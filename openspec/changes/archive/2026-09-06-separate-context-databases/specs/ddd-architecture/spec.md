## ADDED Requirements

### Requirement: Context Storage Isolation Is Exhibited, Not Only Permitted

Each bounded context SHALL store its data in a database no other context connects to, in every environment this project runs by default — local development and CI — so that a query crossing a context boundary fails rather than returning rows.

This is the storage counterpart of the Package Dependency Rules requirement above, and exists for the same reason: a boundary nothing executes against is one that has already drifted by the time anyone notices. The import rule is enforced by an AST walk; the storage rule is enforced by there being no database in which two contexts' tables coexist. A cross-context `JOIN` is then a query PostgreSQL refuses, not a query a reviewer has to catch.

The isolation SHALL be a database boundary rather than a naming convention within one database. A schema-qualified name reaches across a PostgreSQL schema freely, so a schema-per-context arrangement would leave enforcement resting on `search_path` discipline — a convention again.

What this requirement does **not** constrain is physical topology. One PostgreSQL server holding three databases satisfies it; so do three servers. `notification-persistence`'s existing statement — that which server a DSN points at is a deployment decision and the code SHALL NOT assume it — stands unchanged, and no context SHALL gain code that depends on the separation being physical.

#### Scenario: The default local stack gives each context its own database

- **WHEN** a contributor starts the documented full stack
- **THEN** the Identity, Video Processing, and Notification connection strings name three different databases, and no database holds tables owned by more than one context

#### Scenario: A cross-context query fails rather than returning rows

- **GIVEN** a context's connection and a table owned by a different context
- **WHEN** a query names that table
- **THEN** it fails as an unknown relation, in local development and in CI alike, rather than succeeding against a shared database

#### Scenario: The code is unchanged by the separation

- **GIVEN** a deployment that points all three connection strings at one database
- **WHEN** each process starts
- **THEN** it starts and operates exactly as before — the separation is an environment decision, and no context reads, asserts, or branches on whether the other contexts are elsewhere
