## ADDED Requirements

### Requirement: The Web Page Lets a Signed-In User Subscribe to Job Outcomes by E-mail

The web page served by `GET /` SHALL let a signed-in user subscribe to e-mail on each of the two terminal event types — `video_job.failed.v1` and `video_job.completed.v1` — and SHALL show which of those they are currently subscribed to. It SHALL do so only through the existing `GET /api/notification-preferences` and `PUT /api/notification-preferences` routes on the page's own origin, on channel `email`, without sending a secret.

The page SHALL NOT create a preference the user did not select: saving SHALL write a preference only for an event type the user selected, or for one whose preference already exists (writing it disabled when deselected). The address SHALL default to the signed-in account's e-mail when no `email` preference is stored, and SHALL be editable.

The page SHALL tell the user that a subscription applies to outcomes that occur after it is saved, because delivery ignores events that precede a preference's creation.

#### Scenario: A signed-in user subscribes to failures

- **GIVEN** a signed-in user with no stored preference
- **WHEN** they select "falha", leave "concluído" unselected, and save
- **THEN** exactly one `PUT /api/notification-preferences` is sent, for `video_job.failed.v1` on channel `email`, enabled, carrying the address in the form and no secret

#### Scenario: The page reflects stored subscriptions

- **GIVEN** a signed-in user with an enabled `email` preference for `video_job.failed.v1`
- **WHEN** the page loads or they sign in
- **THEN** the failure option is shown selected, the completion option unselected, and the address field shows the stored address

#### Scenario: Deselecting disables rather than creating or deleting

- **GIVEN** a signed-in user with an enabled `email` preference for `video_job.failed.v1` and none for `video_job.completed.v1`
- **WHEN** they deselect both options and save
- **THEN** one `PUT` is sent for `video_job.failed.v1` with `enabled` false, and none for `video_job.completed.v1`

#### Scenario: The section is not offered without a session

- **GIVEN** no signed-in user
- **WHEN** the page is shown
- **THEN** the notification section is hidden and no preference route is called

#### Scenario: A subscriber is notified of a failed job

- **GIVEN** the full local stack is running and a signed-in user has saved a subscription to failures
- **WHEN** they upload a video whose processing fails
- **THEN** an e-mail announcing the failure arrives at the subscribed address in the local mail catcher
