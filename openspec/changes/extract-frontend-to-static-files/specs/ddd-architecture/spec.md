## MODIFIED Requirements

### Requirement: Frontend as Presentation/Delivery Layer

The web frontend (HTML/CSS/JavaScript in `web/index.html`, `web/styles.css`, and `web/app.js`, embedded into the binary via `go:embed` and served as `GET /`, `GET /styles.css`, and `GET /app.js` respectively) SHALL be treated as a presentation/delivery layer, not as a bounded context. It SHALL remain functional throughout all phases of the DDD migration, and any backend contract change that affects its consumed endpoints SHALL include an explicit task to update it.

#### Scenario: Frontend is not a bounded context

- **GIVEN** the system is organized into bounded contexts
- **WHEN** the HTML/CSS/JS served by `GET /` is evaluated
- **THEN** it SHALL NOT be assigned domain responsibilities, aggregate roots, or domain events; it is a delivery layer that consumes the Video Processing context's HTTP API

#### Scenario: Frontend extraction preserves GET / behavior

- **GIVEN** `web/index.html`, `web/styles.css`, and `web/app.js` have been extracted from `getHTMLForm()` and are served via `go:embed`
- **WHEN** a browser requests `GET /`
- **THEN** the server returns HTTP 200 with the HTML page and the page renders without JavaScript errors

#### Scenario: POST /upload remains available during async migration

- **GIVEN** Phase 6 introduces `POST /jobs` as the canonical async job endpoint
- **WHEN** an existing client sends a request to `POST /upload`
- **THEN** the endpoint SHALL remain available and SHALL accept the same multipart form data; only the response schema changes (returns job ID + status URL instead of a direct download link)

#### Scenario: Backend contract change must not silently break the frontend

- **GIVEN** a backend change adds, renames, or removes an HTTP endpoint consumed by the frontend
- **WHEN** the change is being specified and implemented
- **THEN** the same OpenSpec change SHALL include a task to update `web/app.js` to reflect the new contract
