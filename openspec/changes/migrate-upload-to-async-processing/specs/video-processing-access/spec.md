## REMOVED Requirements

### Requirement: Existing synchronous processing remains functional behind access control

**Reason**: This change is exactly what the requirement forbade — it replaces the synchronous in-request processing path with asynchronous infrastructure. The requirement was written in Phase 2 to stop *authentication* work from opportunistically rewriting the pipeline; that constraint did its job and has now been discharged deliberately by a change whose whole purpose is the replacement. Leaving it standing would make it a known-false normative claim.

**Migration**: The guarantee it protected — an authenticated user can still reach their own result through the protected API — is preserved verbatim by the ADDED requirement below, restated over the asynchronous path. No client-visible access-control behavior changes; only the number of requests it takes to get there does.

## ADDED Requirements

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
