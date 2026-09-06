## MODIFIED Requirements

### Requirement: Unchanged External Contract

Hardening the image SHALL NOT change its external contract: the application SHALL still listen on port 8080 and create the runtime directories it needs on first run — so `docker-compose.yml`'s services and the deployment commands documented in `docs/operations.md` keep working.

The image now serves **five** processes, and that SHALL NOT change how any of them is configured or reached: each of the three HTTP services SHALL listen on port 8080 inside its own container, while the worker and the notifier SHALL each expose no port at all. Because the three HTTP services share that port number, they SHALL be distinguished by container rather than by port, and the compose stack SHALL publish exactly one host port — the ingress's. Adding a process SHALL NOT make it a prerequisite for any other to start, in either direction — each SHALL start, run, and fail independently.

The ingress is the first service in the stack built from an image this repository does not produce. That SHALL NOT change what this image contains: the ingress SHALL be the stock upstream image plus a mounted configuration file, and no application binary, key, or credential SHALL be added to it.

Each process SHALL require only the environment configuration it uses, and SHALL fail fast with a clear error when it is missing rather than starting in a degraded mode. The surfaces are deliberately different: the Identity service requires no object-storage, broker, cache, or `ffmpeg` configuration; the worker requires no identity configuration; the notifier requires neither identity nor object-storage configuration nor `ffmpeg`; and the two non-Identity HTTP services require a public key but SHALL be given no private key. Requiring any of the absent ones would misrepresent what the process does. Which variables are required is specified by the capabilities that own them, not by this one.

#### Scenario: Each HTTP service starts from the same image

- **WHEN** `docker compose up --build` runs
- **THEN** the Identity, Video Processing, and Notification HTTP services each start from the same image, running their own binary with the environment the compose file supplies, each listening on port 8080 inside its own container and publishing none

#### Scenario: Existing deployment commands keep working

- **WHEN** a deployer runs the `docker build`/`docker run` commands documented in `docs/operations.md`, supplying every environment variable those docs list as required for the process being started
- **THEN** they succeed unmodified and the container behaves as documented: same port, same first-run directory creation, and the same fail-fast behavior for missing configuration

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
