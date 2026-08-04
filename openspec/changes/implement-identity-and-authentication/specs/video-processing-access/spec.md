## ADDED Requirements

### Requirement: Processing Access Is Scoped to Verified Identity

Video-processing handlers SHALL accept a verified `UserID` from the authentication boundary and SHALL NOT infer identity from arbitrary request fields, filenames, or query parameters.

#### Scenario: Protected upload has verified identity

- **GIVEN** an authenticated user uploads a valid video
- **WHEN** the upload handler runs
- **THEN** it receives the `UserID` produced by token verification and can associate the operation with that identity

#### Scenario: Anonymous processing access is rejected

- **GIVEN** a request lacks a valid bearer token
- **WHEN** it reaches a protected processing endpoint
- **THEN** the request is rejected with HTTP 401 before video processing begins
