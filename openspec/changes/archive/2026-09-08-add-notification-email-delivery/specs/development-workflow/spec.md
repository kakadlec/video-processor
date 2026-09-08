## MODIFIED Requirements

### Requirement: Local Full-Stack Development Service
The repository SHALL provide a documented single command that starts the application together with PostgreSQL, with identity enabled, so a contributor can exercise registration, login, and bearer-protected video-processing routes locally without hand-configuring environment variables or manually wiring network access to the database. `docker-compose.yml` SHALL be the sole documented entry point for Docker-based **local development** workflows — there SHALL NOT be a separately-documented plain `docker build`/`docker run` alternative for local development. Container deployment (`docs/operations.md`) is a distinct concern and is unaffected by this requirement.

The stack that command starts SHALL run **more than one** video-processing worker, so that concurrent processing of several videos is what the default demonstrates rather than a configuration the contributor has to discover. Concurrency here is worker count and nothing else: prefetch is 1, so one worker holds one job at a time, and what makes several workers safe against one queue is the atomic conditional claim (`videojob-execution`), not the replica count. The count SHALL remain overridable per run, in both directions, so a single-worker stack stays one flag away.

The stack SHALL also start a mail service, so that a contributor can register an e-mail delivery preference and observe the resulting message without configuring an external relay and without sending mail to a real address. It SHALL come from an image this repository does not build, as the ingress does. It SHALL accept the notifier's messages on the internal network and SHALL expose its inspection interface on a loopback-bound host port of its own, under the support-service allowance stated below.

The HTTP tier that command starts is **three services behind one ingress**, and the single-command promise is unchanged by that: the contributor SHALL still reach every route on one host port with no additional configuration, and SHALL NOT have to know which service answers which path. Exactly one service **serving the application's own HTTP surface** SHALL publish a host port — the ingress; the three application services SHALL publish none, which is also what allows any of them to be scaled without a port collision. A development-only support service that serves no application route MAY publish a port of its own, and SHALL bind it to loopback exactly as the ingress does; the mail service below is the only such service, and it is not reached through the ingress because routing a development inspection UI through the application's own gateway would put a path on that gateway which no deployment serves. The ingress SHALL be configured so that an upload larger than its software's default request-body limit succeeds and is not buffered to the ingress's own disk.

#### Scenario: Contributor starts the full stack with one command
- **WHEN** a contributor runs the documented `docker compose up --build` command
- **THEN** the ingress and the three application services build from the repository's `Dockerfile` and the stock ingress image, start only after PostgreSQL's healthcheck reports healthy, and serve `/api/auth/register` and `/api/auth/login` on the documented host port without any additional configuration

#### Scenario: One host port serves every route
- **WHEN** a contributor exercises the documented end-to-end flow against the stack
- **THEN** registration, login, upload, status polling, and download issuance are all reached on the same host and port, and no application service publishes a port of its own

#### Scenario: A large upload survives the ingress
- **WHEN** a contributor uploads a video larger than the ingress software's default request-body limit through the documented host port
- **THEN** the upload is accepted and processed, rather than being rejected by the ingress before the application sees it

#### Scenario: The default stack processes several videos concurrently
- **WHEN** a contributor runs the documented `docker compose up --build` command with no scaling flag
- **THEN** more than one `worker` container is started from the same image and configuration, each connecting its own consumer to the job queue, so several queued jobs are extracted at the same time rather than one after another

#### Scenario: A contributor overrides the worker count for a run
- **WHEN** a contributor runs the same command with `--scale worker=<n>`
- **THEN** exactly `<n>` worker containers run for that invocation, whether `<n>` is below or above the default, and no file has to be edited to get a single-worker stack

#### Scenario: Only the worker service is replicated by default
- **WHEN** the default stack starts
- **THEN** exactly one container runs for the ingress and for each of the three HTTP services, and the replica count applies to the `worker` service alone

#### Scenario: A contributor observes a delivered e-mail locally

- **WHEN** a contributor runs the documented `docker compose up --build` command, registers an e-mail delivery preference, and completes a job
- **THEN** the resulting message is captured by the stack's own mail service and is inspectable on that service's own loopback-bound host port, without any external relay being configured and without mail leaving the machine
