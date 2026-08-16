# FIAP X — Video Frame Processor

A Go web service that accepts a video upload, extracts frames at 1 fps via `ffmpeg`, packages them into a ZIP, and serves the result for download. Built as the code deliverable for a POSTECH/FIAP hackathon.

## Prerequisites

| Dependency | Version | Notes |
|---|---|---|
| Go | 1.25+ | Required; see `go.mod` |
| ffmpeg | any recent | Must be on `PATH`; the app shells out to it |
| Docker | any recent | Optional for local dev; required if Go/ffmpeg are not installed |

## Quickstart

The server requires identity configuration (`IDENTITY_POSTGRES_DSN` and `IDENTITY_JWT_SIGNING_KEY`) to start — see [docs/development.md](docs/development.md) for running it directly with `go run ./cmd/api`. The fastest path with no manual wiring is Docker:

```bash
# 1. Clone and enter the repo
git clone https://github.com/kakadlec/video-processor.git
cd video-processor

# 2. Run the full stack (app + PostgreSQL, identity already configured)
docker compose up --build
# Server starts on http://127.0.0.1:8080, with PostgreSQL-backed identity
# already wired in — /api/auth/register and /api/auth/login are live

# 3. Open http://127.0.0.1:8080 in your browser
# Register/log in, then upload a video file; the page returns a download
# link when processing completes.
```

`docker-compose.yml` is the only supported way to run the application via Docker **for local development** — there is no separate plain `docker build`/`docker run` workflow documented for that purpose. (Container deployment is a different concern; see [docs/operations.md](docs/operations.md).) See [docs/development.md](docs/development.md) for running the test suite the same way.

## Current Limitations

The application is a synchronous monolith at this stage:

- **No async processing** — `POST /upload` blocks until `ffmpeg` finishes. Large videos will hold the HTTP connection open for minutes.
- **Local filesystem only** — uploads, frames, and ZIPs are stored in `uploads/`, `temp/`, and `outputs/` on the server's local disk. Not suitable for horizontally-scaled deployments.
- **No job queue** — concurrent uploads each run their own `ffmpeg` process with no concurrency limit.
- **No notifications** — users must stay on the page or poll `GET /api/status` to find out when processing completes.
- **No persistent state** — all state lives in the filesystem; a restart does not lose existing ZIPs, but in-flight processing is lost silently.

These limitations are addressed in the [architecture roadmap](docs/roadmap.md).

## Documentation

| Document | Contents |
|---|---|
| [docs/architecture.md](docs/architecture.md) | Current implementation, target DDD structure, roadmap summary |
| [docs/domain-model.md](docs/domain-model.md) | Bounded contexts, `VideoJob` aggregate, state machine, domain events |
| [docs/flows.md](docs/flows.md) | Current synchronous flow, target async flow, frontend interaction sequences |
| [docs/development.md](docs/development.md) | Local setup, test execution, Docker workflow, contribution conventions |
| [docs/operations.md](docs/operations.md) | Deployment, runtime directories, environment variables, planned infrastructure |
| [docs/roadmap.md](docs/roadmap.md) | 8-phase evolution roadmap (summary) |

For the full project requirements see [docs/project-requirements.pdf](docs/project-requirements.pdf).

## API

| Method | Path | Description |
|---|---|---|
| `GET` | `/` | Web upload UI (inline HTML/CSS/JS); always public |
| `POST` | `/api/auth/register` | Create a user account |
| `POST` | `/api/auth/login` | Authenticate and receive a bearer access token |
| `POST` | `/upload` | Upload a video file (multipart `video` field); returns JSON with `zip_path` on success; requires `Authorization: Bearer <token>` |
| `GET` | `/download/:filename` | Download a processed ZIP by filename; owner-only |
| `GET` | `/api/status` | List processed ZIPs with metadata; scoped to the caller's own uploads |

## Tech Stack

- **Language:** Go 1.25
- **HTTP framework:** [Gin](https://github.com/gin-gonic/gin) v1.12
- **Frame extraction:** `ffmpeg` (via `exec.Command`)
- **Identity persistence:** PostgreSQL (via `pgx`)
- **Password hashing:** bcrypt
- **Access tokens:** JWT ([`golang-jwt/jwt`](https://github.com/golang-jwt/jwt))
- **CI:** GitHub Actions — `go vet`, `go test`, [gosec](https://github.com/securego/gosec), [govulncheck](https://go.dev/security/vuln)
