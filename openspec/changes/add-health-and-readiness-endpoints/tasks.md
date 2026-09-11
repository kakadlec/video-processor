## 1. Confirm the premises before building on them

- [ ] 1.1 Re-confirm the dependency matrix by **reading the setup functions**, not the environment matrix: `docs/operations.md:134-150` enumerates *variables read*, not *connections opened*, and diverges in at least three places (`RABBITMQ_URL` is "required" for `video-api` but `setupVideo` performs no AMQP dial; `REDIS_ADDR` is "required" for three processes but `platformredis.Open` never connects; `NOTIFICATION_SMTP_*` are "required" for the notifier but `setupNotifier` only loads them). A readiness check derived from that table would check things nothing opens and miss things startup does.
- [ ] 1.2 Confirm `platformrabbitmq.Ping`'s signature before writing the exclusion into a comment: it takes a live `*amqp.Connection` and **no context**. If either has changed, the structural half of the broker argument changes with it and only the behavioural half survives.
- [ ] 1.3 Confirm `container-image`'s "the worker and the notifier SHALL each expose no port at all" is still the canonical text (`openspec/specs/container-image/spec.md:55`, scenarios `:83`, `:88`). The whole answer to the roadmap's first open question rests on that being a requirement rather than a convention; if it is not, this change needs a `container-image` delta and the argument has to be made rather than cited.
- [ ] 1.4 Enumerate every test that will break: the three roots' `logging_test.go` pin "every request yields an access record" (commit `390e4ce` exists for that), and any router test that counts routes. A pinned assertion fails at run time, not build time — find them first.

## 2. The readiness checker

- [ ] 2.1 Add the checker to each of the three roots, built in `main` from the handles startup already holds. It runs its checks **concurrently** and bounds each with an explicit timeout derived from the request context, so a caller that disconnects releases the work and a hung dependency does not hold the goroutine.
- [ ] 2.2 `setupVideo` gains the `*minio.Client` it already constructed, pinged and bucket-checked as a return value — the only structural change the endpoints force. Do **not** put it on `videoModule`, which holds use cases and domain ports and no raw driver handle, and do **not** add a health operation to `SourceStorage`: `Stat` of an absent key returns an error that means *healthy*, which is a check waiting to be "fixed".
- [ ] 2.3 The checks are `db.PingContext` and `storage.Ping`, and nothing else is written. `platformredis.Ping` and `platformrabbitmq.Ping` keep their current count of production callers, which is zero — say so in the PR description, because "we added a ping" is what a reviewer will assume.
- [ ] 2.4 No verdict caching, at any layer. `storage.Ping` exists in the form it does specifically because `IsOnline` "reports a cached observation from a background goroutine instead, which would answer for a different moment than the caller asked about"; a cache here reintroduces exactly that.
- [ ] 2.5 Cover each service's matrix row as a test, including the **negative** rows: with the cache unreachable readiness is `200` and no cache call is made, and with the broker unreachable readiness is `200`. The positive rows are the easy half; the exclusions are the decision, and an untested exclusion is an opinion.

## 3. The two endpoints

- [ ] 3.1 `GET /health` in all three roots: no dependency, fixed body, `200`. Register it on the engine, beside `/`, `/styles.css` and `/app.js` — outside `requireBearerAuth()` and outside `rateLimitMiddleware`. The documented invariant is the *pair and its order*, not that every route is inside the group.
- [ ] 3.2 `GET /ready` in all three roots: `200` or `503`, fixed body per verdict.
- [ ] 3.3 Both carry `Cache-Control: no-store`, following `GET /download/:filename`.
- [ ] 3.4 Assert non-disclosure as a **test over two different failures**, not as a code reading: fail the video service's readiness once through PostgreSQL and once through MinIO, and assert the two responses are byte-identical in status, body and headers. A test that only checks one failure passes with a dependency name in the body.
- [ ] 3.5 Assert a probe with no `Authorization` header is answered rather than `401`, on all three — including `cmd/identity-api`, which mounts neither middleware and where the assertion is about the route's placement rather than about the middleware.
- [ ] 3.6 Assert the rate limiter does not count a probe: exhaust a user's budget, then probe, and assert `200` rather than `429`. This is the failure mode that would make a healthy service report unhealthy under load, and it is invisible in any test that probes an idle service.

## 4. The access-log exemption and the transition record

- [ ] 4.1 Add the exemption to `accessLogMiddleware` in each of the three roots, as a **closed list of exactly the two probe route templates**. No configurable skip list, no per-route option, no exported knob — the spec delta refuses a general mechanism and the implementation is what makes that true.
- [ ] 4.2 Update each root's `logging_test.go` to pin the exemption's **boundary**, not merely its existence: a probe request yields no access record, and a request to any other route — matched or unmatched — still yields one. A test asserting only the first half passes with the middleware disabled entirely.
- [ ] 4.3 **The exemption suppresses the record, not the chain.** The natural implementation — an early `return` in `accessLogMiddleware` when the matched route is a probe template — never calls `c.Next()`, so the probe handler never runs and every probe answers `200` with an empty body. That defect passes a test asserting "no access record for a probe" *and* a test asserting "a probe answers `200`"; it fails only a test that reads the body. Assert the body, on both endpoints, and assert `/ready` still answers `503` when a dependency is down.
- [ ] 4.3a Assert a panic raised while serving a probe route is still recorded. The exemption covers the routine per-request record and not the report of a failure, and that is the other boundary the skip's placement in the chain can get wrong.
- [ ] 4.4 The transition record: hold the verdict per process, swap it with an atomic compare-and-set, and emit one `warn` on ready → not-ready naming the failing dependencies and one `info` on recovery. Assert that many probes across one failure and one recovery produce exactly two records, and that two concurrent probes observing one transition produce one record.
- [ ] 4.5 The record obeys the three source rules unchanged — string-literal message, typed scalar attributes, no `slog.Any` — and the dependency names come from a closed set this code owns. `internal/platform/logging`'s AST walk enforces this automatically; run it rather than assuming it.

## 5. The gateway

- [ ] 5.1 Add two exact-match `location` blocks to `docker/nginx/nginx.conf` refusing `/health` and `/ready` with `404`. They need no `set $backend` — they proxy nothing.
- [ ] 5.2 Place them so they win over `location /`. An exact-match `location =` takes precedence over a prefix match in nginx regardless of order, but verify it against the running gateway rather than against that sentence.
- [ ] 5.3 Verify through the stack: the probe paths return `404` through the gateway on the published port, and answer `200` when requested on the service's own port from inside the network.

## 6. The local stack

- [ ] 6.1 Add a healthcheck to each of `identity-api`, `video-api` and `notification-api` in `docker-compose.yml`, targeting `/ready`, shaped like the gateway's block: `interval: 10s`, `timeout: 5s`, `retries: 5`, no `start_period`.
- [ ] 6.2 **Verify the probe command's exit code empirically, against both a healthy and an unhealthy service.** `curl` is not in the runtime image; `sh`, `wget` and `nc` are. The named trap: busybox `wget --spider` may issue a `HEAD`, and gin does not answer a `HEAD` on a `GET`-only route — which would make every probe report unhealthy for a reason that has nothing to do with the service. Confirm the chosen flags issue a `GET` and that a `503` exits non-zero.
- [ ] 6.3 Confirm the ordering, naming both sides so the row cannot be read backwards: **the compose healthcheck's `timeout` must be strictly greater than the handler's own per-check bound.** If the handler may run longer than the prober waits, the prober abandons every request and every verdict becomes a failure regardless of the dependency, with nothing in the output saying so. This is the one number in this change that is wrong in a way that looks like a working system.
- [ ] 6.4 Add **no** `depends_on: condition: service_healthy` against these services, and leave the gateway's `depends_on` on the three APIs in its present bare list form. Confirm `docker compose up --build` comes up exactly as it did before.
- [ ] 6.5 Exercise it: stop PostgreSQL, confirm the three services report unhealthy and the gateway does not; stop MinIO, confirm `video-api` reports unhealthy and the other two do not; stop Redis, confirm all three stay healthy. The Redis case is the one that proves the criterion was implemented rather than merely written down.

## 7. Pin the answer to the roadmap's first open question

- [ ] 7.1 Add a source-level test to `cmd/worker` and one to `cmd/notifier`, each over **its own** package's non-test files, asserting the package constructs no HTTP server and imports no HTTP framework. In-package rather than a cross-root scan, so the failure lands in the package that introduced the listener. The idiom is `TestOnlyTheIdentityServiceConstructsATokenIssuer`.
- [ ] 7.2 See each fail before it passes: add a temporary `net/http` server construction to one of them, confirm the test names the file and line, then remove it. A source-level test never seen failing is a test of nothing.
- [ ] 7.3 Make the walk skip `ENOENT` rather than returning `WalkDir`'s callback error verbatim, and fail loudly if it parses no file — the flake fixed in #251 for the two existing walkers, and the vacuous-pass it guards against.

## 8. Quality gates

- [ ] 8.1 `go vet ./...` clean; all five binaries build.
- [ ] 8.2 `docker compose run --build --rm app-test go test ./... -v` passes in full. The diff carries Go module inputs, so this is the gate. `--build` is not optional.
- [ ] 8.3 `go.mod` and `go.sum` byte-identical — this change adds no dependency.
- [ ] 8.4 `gosec ./...` and `govulncheck ./...` clean. Expect `gosec` to have an opinion about an unauthenticated endpoint's timeouts; answer it rather than suppressing it.
- [ ] 8.5 Run one upload end to end against the stack and confirm nothing regressed: `202`, polling, download issuance, and a delivered notification.
- [ ] 8.6 `git diff --check`, and the four required checks green on the PR.

## 9. Finalization (after the implementation PR merges — not part of it)

- [ ] 9.1 Check off the implementation tasks above.
- [ ] 9.2 `docs/operations.md`: replace the "Observability — Partly implemented (Phase 8)" section's probe sentence with what shipped; document the two endpoints, the per-service readiness matrix, and the bound-versus-prober-timeout constraint. Record the worker/notifier gap as an answered question with its cost, replacing the open one at `:679`.
- [ ] 9.3 `docs/operations.md`'s environment matrix: it enumerates *variables read*, and the three divergences task 1.1 names are already there. Correct them, or add a sentence saying explicitly that the table is about variables and not about connections — which is what made it the wrong source for this change's own matrix.
- [ ] 9.4 `docs/architecture.md` and `CLAUDE.md`: the two endpoints, the readiness criterion and where it came from, the access-log exemption, and that the worker and the notifier serve nothing by requirement rather than by omission.
- [ ] 9.5 `docs/roadmap.md`: mark this row `archived` with its links and update the Phase 8 summary row — one of three becomes two of three.
- [ ] 9.6 `npx --yes @fission-ai/openspec validate add-health-and-readiness-endpoints --strict --no-interactive`, fix every error, then archive.
