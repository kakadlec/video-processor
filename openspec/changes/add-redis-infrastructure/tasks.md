## 1. Dependency

- [ ] 1.1 Add `github.com/redis/go-redis/v9` to `go.mod`/`go.sum` (`go get github.com/redis/go-redis/v9`, then `go mod tidy`).

## 2. `internal/platform/redis` package

- [ ] 2.1 `config.go`: `Config` struct (`Addr string`), `ErrAddrRequired` sentinel error, `LoadConfigFromEnv()` reading `REDIS_ADDR`.
- [ ] 2.2 `client.go`: `Open(cfg Config) *redis.Client` — constructs a `*redis.Client` from `cfg.Addr` via `redis.NewClient(&redis.Options{Addr: cfg.Addr})`, no connectivity check, no `error` return (construction from an unparsed `Addr` string cannot fail).
- [ ] 2.3 `client.go`: `Ping(ctx context.Context, client *redis.Client) error` — issues `client.Ping(ctx)`, wraps a failure with a package-prefixed error message (mirroring `postgres.Open`'s `fmt.Errorf` wrapping style).
- [ ] 2.4 `client.go`: `Close(client *redis.Client) error` — thin wrapper over `client.Close()`.

## 3. Tests

- [ ] 3.1 `config_test.go`: `TestLoadConfigFromEnv_RequiresAddr` and `TestLoadConfigFromEnv_ReadsAddr`, mirroring `internal/identity/infrastructure/postgres/config_test.go`'s pattern via `t.Setenv`.
- [ ] 3.2 `client_test.go`: `TestOpen_SucceedsWithoutConnecting` (unreachable address, `Open` still returns a non-nil client).
- [ ] 3.3 `client_test.go`: `TestPing_SucceedsAgainstRunningRedis` (uses `REDIS_TEST_ADDR`; skips with a clear message if unset, matching how the Postgres adapter's own integration tests behave when their test DSN is unset).
- [ ] 3.4 `client_test.go`: `TestPing_FailsAgainstUnreachableRedis` (bogus address, expects a non-nil wrapped error).
- [ ] 3.5 `client_test.go`: `TestClose_ReleasesClient` (open against `REDIS_TEST_ADDR`, close, confirm a subsequent command fails).

## 4. Local dev & CI infrastructure

- [ ] 4.1 `docker-compose.yml`: add a `redis` service (pinned `redis:7-alpine` image, `redis-cli ping` healthcheck, no named volume — see design.md's rationale), following the existing `postgres` service's comment style.
- [ ] 4.2 `docker-compose.yml`: add `REDIS_TEST_ADDR` to the `app-test` service's environment, pointing at the new `redis` service (`redis:6379`), and add `redis` to `app-test`'s `depends_on` with `condition: service_healthy` — `docker compose run` only starts a service's declared dependencies, so without this the `redis` service never starts and the tests in task 3 would connect to nothing.
- [ ] 4.3 `.github/workflows/ci.yml`: add a `redis` service container to the `test` job (mirroring the existing `postgres` service block) and `REDIS_TEST_ADDR` to the `Test` step's env, pointing at `localhost:6379`.

## 5. Verification

- [ ] 5.1 `go vet ./...` passes.
- [ ] 5.2 `go test ./... -v` passes locally via `docker compose run --build --rm app-test go test ./... -v` (exercises the new Redis service alongside the existing Postgres one).
- [ ] 5.3 Confirm `internal/video/dependency_rules_test.go` and `internal/identity/dependency_rules_test.go` still pass unmodified — `internal/platform/redis` is imported by neither context yet, so nothing in those tests' scope changes.

## 6. Finalization (separate PR, per repo-workflow)

- [ ] 6.1 Promote this change's delta specs into `openspec/specs/redis-infrastructure/spec.md` (new) and `openspec/specs/ddd-architecture/spec.md` (modified), then archive the change folder.
- [ ] 6.2 Update `docs/architecture.md`: "Infrastructure Components" table (Redis row: still Phase 4, but now "connection adapter implemented" rather than fully "Planned"; also correct the row's "distributed locks" mention to reflect the Phase 6 deferral), "Target Package Topology" tree (add `internal/platform/redis/`), and the introductory Phase-status callout.
- [ ] 6.3 Update `docs/roadmap.md`: flip this row's status to archived in the Change Backlog under a new "Phase 4" section (create that section — this is the first Phase 4 row), and update the Phase Summary table's Phase 4/Phase 6 rows to move "distributed lock for worker job pickup" from Phase 4 to Phase 6 (this was drafted standalone in PR #136, held open pending this change so the canonical `ddd-architecture` delta above lands first — fold that edit in here instead of merging #136 separately).
- [ ] 6.3a Update `docs/operations.md`'s "Redis — Planned (Phase 4)" section: its "Four explicit responsibilities" list still includes distributed locks as Phase 4 scope (item 4) — move it to the future Phase 6 section (or a new one) alongside the other Phase 4 fixes above, so the deferral reads consistently across `docs/architecture.md`, `docs/operations.md`, and `docs/roadmap.md`.
- [ ] 6.4 Update `CLAUDE.md` only if its existing Architecture/Notable-constraints sections make a claim this change invalidates (expected: none, since nothing is wired into `cmd/api` yet) — confirm rather than assume.
