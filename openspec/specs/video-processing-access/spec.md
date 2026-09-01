# video-processing-access Specification

## Purpose

Define the access-control boundary between Identity and the Video Processing HTTP surface, and the guarantee that it survives every change to how processing is scheduled. Since the asynchronous cutover that surface is a three-step path — submit, poll, obtain a download grant — each step separately authenticated and owner-scoped, with `cmd/worker` outside the boundary entirely because it acts on an internal dispatch rather than on behalf of a caller.

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

### Requirement: Artifact Access May Be Delegated Only As A Bounded, Pre-Authorized Grant

Video Processing MAY hand a client a credential that grants access to one result artifact without that client presenting a bearer token, and SHALL do so only under all of the following conditions: the authenticated `UserID` was verified before the credential was minted, the credential names exactly one artifact belonging to that `UserID`, and the credential carries an expiry fixed by the system rather than by the caller.

Because such a credential carries no `UserID`, the entitlement check at the moment of issuance SHALL be the complete authorization decision. No component SHALL be relied upon to re-evaluate ownership when the credential is redeemed, and no request that fails the entitlement check SHALL cause a credential to be minted.

The credential SHALL NOT be revocable, and the system SHALL NOT represent it as such. Nothing that happens to the `VideoJob` after issuance — deletion, a change of owner, a change of status — withdraws a credential already handed out; the expiry is the whole of the mechanism by which access ends. Its short lifetime is what makes that acceptable.

A caller SHALL NOT be able to influence which artifact a credential names or how long it lasts. Both derive from the verified `UserID` and the job it owns, consistent with this capability's existing rule that caller-supplied owner fields are ignored.

#### Scenario: A credential is minted only after ownership is verified

- **GIVEN** an authenticated caller requesting access to an artifact
- **WHEN** the artifact does not belong to that caller
- **THEN** no credential is minted, and the response is indistinguishable from the response for an artifact that does not exist

#### Scenario: The credential names one artifact and no other

- **GIVEN** a credential minted for one artifact
- **WHEN** it is redeemed for a different artifact
- **THEN** access is refused

#### Scenario: The caller cannot choose the lifetime

- **GIVEN** a request carrying a caller-supplied expiry, duration, or equivalent parameter
- **WHEN** the credential is minted
- **THEN** that parameter is ignored, and the system's own fixed lifetime applies

#### Scenario: Redemption is not re-authorized

- **GIVEN** a credential minted for the artifact's rightful owner
- **WHEN** it is redeemed without any bearer token
- **THEN** access is granted, confirming that issuance was the authorization decision and that the system does not depend on a second check it cannot perform

### Requirement: Processing Remains Reachable End-To-End Behind Access Control After The Async Cutover

Moving processing to a worker SHALL NOT weaken, bypass, or relocate any access-control decision. An authenticated user SHALL still be able to submit a video and reach the resulting artifact through the protected API under the same authenticated identity, and every step of that path SHALL remain scoped to the authenticated `UserID`.

The path is now upload → poll → obtain a grant, rather than upload → receive. Each step SHALL be individually authenticated and owner-scoped: the submission derives its owner from the bearer token, the status read returns only the caller's own job, and the download issues a credential only after this capability's existing entitlement check passes. `cmd/worker` SHALL NOT perform an access-control decision at all — it acts on a job identified by an internal dispatch, never on behalf of a caller — and SHALL NOT be reachable from outside the deployment.

The asynchronous path SHALL NOT introduce an unauthenticated way to learn about, influence, or reach another user's job. In particular, the job identifier returned by the submission SHALL NOT be sufficient on its own to read that job's status.

#### Scenario: Authenticated end-to-end flow remains available

- **GIVEN** an authenticated user submits a valid video
- **WHEN** they poll the status endpoint named by the response until the job is `completed`, then request the download
- **THEN** they can reach the resulting artifact through the protected API using the same authenticated identity — which since presigned issuance means obtaining a grant for it there, not receiving its bytes from it

#### Scenario: A job identifier alone does not grant access

- **GIVEN** a job identifier returned to the user who submitted it
- **WHEN** another user, or an unauthenticated caller, presents that identifier to the status or download endpoint
- **THEN** the request is rejected exactly as it was before the cutover, and no information about the job is disclosed

#### Scenario: The worker makes no access-control decision

- **WHEN** `cmd/worker` processes a dispatched job
- **THEN** it neither reads nor requires a bearer token, and no code path in it consults an identity or authorization component
