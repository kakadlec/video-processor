## MODIFIED Requirements

### Requirement: Delivery Runs in Its Own Entrypoint, Requiring Only Its Own Configuration

The Notification context's event consumer SHALL be its own entrypoint, `cmd/notifier`, built from the same source tree and shipped in the same image as the three HTTP services and `cmd/worker`, and started as that image with a different command. It SHALL listen on no port.

It SHALL require exactly the configuration it uses — the Notification context's own PostgreSQL DSN, the broker URL, and the configuration each delivery channel it composes needs to send — and SHALL fail fast with an error naming a missing variable rather than starting in a degraded mode. Requiring a channel's configuration is what keeps the closed channel set honest from this side: a preference on a channel this process could not send through would be stored and silently never honoured. It SHALL NOT require identity configuration, object-storage configuration, Redis, or `ffmpeg`: it authenticates no caller, stores no artifact, holds no lease, and runs no extraction. Requiring any of them would misrepresent what the process does.

A separate process rather than a goroutine inside an existing one is required for the reason `videojob-terminal-events` gives for placing the terminal relay in the worker, applied to the other direction: an outbound request to a third party must not share a lifecycle with serving HTTP requests, nor with the worker's single-extraction-at-a-time shape. They scale on different axes.

One event SHALL be able to resolve to one preference per channel, and the handler SHALL process them one after another, so handling a single message can consume one full claim-hold budget **per channel** rather than one in total. The bounded drain that shutdown waits on SHALL therefore be sized from the number of channels in the closed set, not from a single claim hold. Leaving it at one hold would make the drain expire during work that is proceeding normally and within budget, turning the skipped pool close from the exceptional path into the ordinary one.

It SHALL compose one delivery implementation per channel in the closed channel set and select between them on the preference's own channel. That selection SHALL live in the composition root: the use case that claims, attempts and resolves a delivery SHALL hold a single outbound port and SHALL NOT branch on the channel, so a second channel does not reach the code that holds the claim and the fence. The composition SHALL be exhaustive over the channel set at startup, so a channel with no implementation is a startup failure rather than a delivery-time one.

Broker reachability SHALL NOT be a startup gate. The consumer SHALL dial with bounded backoff and SHALL redial when the connection or channel is lost.

#### Scenario: The notifier starts with only its own configuration

- **GIVEN** the Notification DSN, the broker URL, and each channel's send configuration are set and no other application variable is
- **WHEN** `cmd/notifier` starts
- **THEN** it starts and begins consuming, opening no port

#### Scenario: A missing required variable fails startup

- **GIVEN** the Notification DSN is absent
- **WHEN** `cmd/notifier` starts
- **THEN** startup fails with an error naming the variable, and nothing is consumed

#### Scenario: An unreachable broker does not prevent startup

- **GIVEN** the broker is down
- **WHEN** `cmd/notifier` starts
- **THEN** it starts, retries the dial with backoff, and begins consuming once the broker returns

#### Scenario: Each of the processes starts without the others

- **GIVEN** the built image
- **WHEN** any one of the three HTTP services, the worker, and the notifier is started alone with its own configuration
- **THEN** it starts and operates without any of the others running

#### Scenario: Each channel is delivered by its own implementation

- **GIVEN** one event resolving to preferences on two different channels
- **WHEN** the deliveries are attempted
- **THEN** each is attempted by the implementation composed for its own channel, and the use case that claims and resolves them holds one outbound port and does not name a channel

#### Scenario: The drain covers a message that delivers on every channel

- **GIVEN** one event resolving to a preference on every channel in the set, each taking its full budget
- **WHEN** shutdown is signalled while that message is being handled
- **THEN** the drain waits long enough for all of them to reach a disposition, rather than expiring while bounded work is still proceeding
