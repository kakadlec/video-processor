## MODIFIED Requirements

### Requirement: Package Dependency Rules

The package structure SHALL enforce a strict dependency hierarchy so that domain logic is never coupled to infrastructure concerns.

#### Scenario: Domain packages have no infrastructure imports

- **GIVEN** any Go file under `internal/<context>/domain/`
- **WHEN** its imports are inspected
- **THEN** it SHALL NOT import any package from `internal/<context>/infrastructure/`, any HTTP framework, any database driver, any message broker client, or any cache client

#### Scenario: Application packages depend only on domain interfaces

- **GIVEN** any Go file under `internal/<context>/application/`
- **WHEN** its imports are inspected
- **THEN** it SHALL NOT import any package from `internal/<context>/infrastructure/` directly; it SHALL depend only on repository and port interfaces defined in `internal/<context>/domain/`

#### Scenario: Infrastructure packages implement domain interfaces

- **GIVEN** any Go file under `internal/<context>/infrastructure/`
- **WHEN** it provides a repository or port implementation
- **THEN** the implementation type SHALL satisfy the interface declared in `internal/<context>/domain/`, not define its own contract

#### Scenario: No direct cross-context domain imports

- **GIVEN** any Go file in any bounded context's packages
- **WHEN** it needs to reference a concept from another bounded context
- **THEN** it SHALL NOT import another context's `domain` or `application` packages directly; each bounded context SHALL define and own its own local value object for any identifier that crosses a context boundary (e.g. each of `internal/identity/domain` and `internal/video/domain` defines its own `UserID` type), and translation between a source context's identifier and a consuming context's local type SHALL happen only at the composition root (`cmd/api`, `cmd/worker`, or `main.go` during incremental migration) or via consumed integration events — never via a package shared between the two contexts' `domain` layers

#### Scenario: Composition root is the only DI boundary

- **GIVEN** `cmd/api` or `cmd/worker`
- **WHEN** it initializes the application
- **THEN** it is the only place where `infrastructure` adapters are instantiated and injected into `application` use cases
