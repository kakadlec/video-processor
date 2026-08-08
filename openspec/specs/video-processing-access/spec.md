# video-processing-access Specification

## Purpose

Define the access-control boundary between Identity and the existing Video Processing HTTP surface during the incremental migration.

## Requirements

### Requirement: Video processing receives only authenticated user identity

Video Processing SHALL consume an authenticated opaque `UserID` at its application boundary and SHALL NOT import Identity domain or application packages.

#### Scenario: Composition root supplies identity

- **GIVEN** a request passes bearer-token verification
- **WHEN** the composition root invokes a video-processing handler or use case
- **THEN** it supplies the verified `UserID` through the request/application boundary

#### Scenario: Caller cannot select another owner

- **GIVEN** a request contains a caller-controlled user identifier or artifact owner field
- **WHEN** the request is handled
- **THEN** the system ignores or rejects that field and uses only the authenticated `UserID`

### Requirement: Existing synchronous processing remains functional behind access control

Adding authentication SHALL NOT replace the current synchronous processing path with asynchronous infrastructure or remove the existing upload-to-download behavior.

#### Scenario: Authenticated end-to-end flow remains available

- **GIVEN** an authenticated user submits a valid video
- **WHEN** the existing processing flow completes
- **THEN** the user can retrieve the resulting artifact through the protected API using the same authenticated identity
