## MODIFIED Requirements

### Requirement: Unchanged External Contract

Hardening the image SHALL NOT change its external contract: the application SHALL still listen on port 8080 and create the runtime directories it needs on first run — so `docker-compose.yml`'s services and the deployment commands documented in `docs/operations.md` keep working.

The image now serves **five** processes, and that SHALL NOT change how any of them is configured or reached: each of the three HTTP services SHALL listen on port 8080 inside its own container, while the worker and the notifier SHALL each expose no port at all. Because the three HTTP services share that port number, they SHALL be distinguished by container rather than by port, and the compose stack SHALL publish exactly one host port **for the application** — the ingress's. A development-only support service built from an image this repository does not produce, serving no application route and present in no deployment, MAY publish a loopback-bound port of its own; the local mail catcher `development-workflow` requires is the only one. The distinction is between what the application exposes and what the local stack provides for inspection: the first is a deployment contract, the second is not deployed at all. Adding a process SHALL NOT make it a prerequisite for any other to start, in either direction — each SHALL start, run, and fail independently.

The image carries five binaries and can default to only one, so its default command SHALL name a binary that exists. Removing or renaming the binary the default command names, without changing it, produces an image that builds and scans clean and exits immediately when run without an explicit command — a failure invisible to the compose stack, which names a command for every service, and visible only in the deployment commands `docs/operations.md` documents. Every process other than the default SHALL be started by naming its binary explicitly, and that SHALL be documented.

The ingress is the first service in the stack built from an image this repository does not produce. That SHALL NOT change what this image contains: the ingress SHALL be the stock upstream image plus a mounted configuration file, and no application binary, key, or credential SHALL be added to it.

Each process SHALL require only the environment configuration it uses, and SHALL fail fast with a clear error when it is missing rather than starting in a degraded mode. The surfaces are deliberately different: the Identity service requires no object-storage, broker, cache, or `ffmpeg` configuration; the worker requires no identity configuration; the notifier requires neither identity nor object-storage configuration nor `ffmpeg`; and the two non-Identity HTTP services require a public key but SHALL be given no private key. Requiring any of the absent ones would misrepresent what the process does. Which variables are required is specified by the capabilities that own them, not by this one.

#### Scenario: Each HTTP service starts from the same image

- **WHEN** `docker compose up --build` runs
- **THEN** the Identity, Video Processing, and Notification HTTP services each start from the same image, running their own binary with the environment the compose file supplies, each listening on port 8080 inside its own container and publishing none

#### Scenario: The documented deployment commands match what the image now contains

- **GIVEN** that the HTTP surface is served by three processes behind an ingress rather than by one
- **WHEN** `docs/operations.md`'s `docker build`/`docker run` commands are followed as written, supplying every environment variable those docs list as required for the process being started
- **THEN** each command succeeds and the container behaves as documented: the same host port reaches the system through the ingress, the same first-run directory creation happens in the process that needs it, and configuration is still fail-fast. The documentation SHALL be updated as part of the change that renames the binaries, rather than promising that commands naming a binary the image no longer contains still work

#### Scenario: The image's default command names a binary it contains

- **GIVEN** the built image
- **WHEN** it is run with no command argument
- **THEN** the process named by the image's default command starts and behaves as documented, rather than failing because the binary it names is not present

#### Scenario: The worker service starts from the same image

- **WHEN** `docker compose up --build` runs
- **THEN** the `worker` service starts from the same image as the HTTP services, running the worker binary with the environment the compose file supplies, and exposes no port

#### Scenario: The notifier service starts from the same image

- **WHEN** `docker compose up --build` runs
- **THEN** the `notifier` service starts from the same image as the others, running the notifier binary with the environment the compose file supplies, and exposes no port

#### Scenario: The ingress carries no application code

- **GIVEN** the ingress service in the compose stack
- **WHEN** its image and mounts are inspected
- **THEN** it is the stock upstream image with a configuration file mounted read-only, carrying no binary built from this repository and no key material

#### Scenario: Each process starts without the others

- **GIVEN** the built image
- **WHEN** any one of the five binaries is started on its own with its own required configuration
- **THEN** it starts and operates, without requiring any of the others to be running

#### Scenario: Missing configuration fails fast rather than degrading

- **WHEN** a container is started without the environment variables the process it runs requires
- **THEN** it exits with an error naming what is missing, rather than starting and failing at request time

#### Scenario: A development-only support service may publish its own port

- **GIVEN** the local compose stack
- **WHEN** it publishes a host port for a service that is neither built from this image nor serving any application route
- **THEN** that is permitted provided the port is loopback-bound, and the application's own HTTP surface is still reached on the ingress's single port
