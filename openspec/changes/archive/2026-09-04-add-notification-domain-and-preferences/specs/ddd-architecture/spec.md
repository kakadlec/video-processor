## MODIFIED Requirements

### Requirement: Three Bounded Contexts With Non-Overlapping Responsibilities

The system SHALL be organized into exactly three bounded contexts — `Identity`, `Video Processing`, and `Notification` — each with a clearly delimited responsibility. No domain concept SHALL be owned by more than one context.

#### Scenario: Identity context owns user credentials and token issuance

- **GIVEN** a user wants to authenticate
- **WHEN** they submit credentials
- **THEN** only the Identity context validates credentials and issues tokens; no other context stores passwords or issues tokens

#### Scenario: Video Processing context owns the job lifecycle

- **GIVEN** an authenticated user uploads a video
- **WHEN** the upload is accepted
- **THEN** the Video Processing context creates, tracks, and completes the `VideoJob`; no other context mutates job state

#### Scenario: Notification context reacts to events without being called directly

- **GIVEN** a `VideoJobCompleted` or `VideoJobFailed` event is emitted
- **WHEN** the Notification context receives it
- **THEN** it delivers the notification per the user's preferences without the Video Processing context knowing or caring how delivery works

#### Scenario: Notification context owns delivery preferences and their HTTP surface

- **GIVEN** a user needs to record where and how they want to be told a job ended
- **WHEN** that preference is read or written
- **THEN** the Notification context owns the preference, its storage, and the authenticated owner-scoped routes that expose it; no other context stores a delivery destination, and being reachable over HTTP does not make the context something another context calls to trigger a delivery
