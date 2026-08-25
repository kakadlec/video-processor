## MODIFIED Requirements

### Requirement: The outputs Directory And Its Ownership Sidecars Are Retired

The system SHALL NOT create, serve, or read an `outputs/` directory. The `/outputs` static route SHALL be removed from the router, and no `.owner` sidecar file SHALL be written, read, or deleted for a result artifact.

The sidecar helpers no longer remain in the codebase for any directory. They survived this capability's change only because `uploads/` still used them; `migrate-upload-storage-to-minio` moves source videos into the bucket as well, deletes the `/uploads` static route, and removes every sidecar symbol. `videojob-source-storage` owns that retirement — this capability now only asserts that no result artifact has ever had one.

#### Scenario: No outputs directory is created at startup

- **WHEN** `cmd/api` starts
- **THEN** it creates `temp/`, and does not create `outputs/`

#### Scenario: The outputs route no longer exists

- **WHEN** an authenticated client requests any path under `/outputs/`
- **THEN** the router has no such route

#### Scenario: A completed job writes no ownership sidecar

- **WHEN** an upload is processed successfully
- **THEN** no `.owner` file is written for the result artifact, and the job's `UserID` is the only record of who owns it

#### Scenario: No sidecar mechanism remains for any artifact class

- **WHEN** the codebase is inspected for the ownership-sidecar helpers, their suffix constant, and the middleware that enforced them
- **THEN** none is present, and no route or handler depends on a sidecar for entitlement
