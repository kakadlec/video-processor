## ADDED Requirements

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
