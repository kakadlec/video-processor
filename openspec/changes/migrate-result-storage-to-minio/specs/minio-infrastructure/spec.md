## REMOVED Requirements

### Requirement: MinIO Configuration Is Not Required At Application Startup

**Reason**: This requirement described a deliberately transitional state. `add-minio-infrastructure` shipped the connection adapter without wiring it into any composition root, and stated that property normatively so a reviewer could tell "not yet wired" apart from "wired and broken". That state ends here: `cmd/api` now loads the configuration, opens the client, pings it, and ensures the bucket at startup, so the variables this capability already marks as required become startup preconditions.

**Migration**: The opposite property is now specified by `videojob-result-storage`'s "MinIO Configuration Is Required At Application Startup" requirement, which also records why this capability's fail-closed posture differs from every Redis-backed feature's fail-open one. Nothing else in `minio-infrastructure` changes — `Open` still constructs a client without blocking on connectivity, `Ping` still performs a real round trip, and `EnsureBucket` is still idempotent and concurrency-safe; only the claim that no composition root calls them is retired.

This capability's own configuration contract is untouched: `VIDEO_MINIO_ENDPOINT`/`ACCESS_KEY`/`SECRET_KEY`/`BUCKET` stay required and `VIDEO_MINIO_USE_SSL` stays optional, with `LoadConfigFromEnv` unchanged. Only the claim that nothing calls the loader is retired.

Operationally, an existing deployment that set none of the `VIDEO_MINIO_*` variables started and served every route before this change and will refuse to start after it. That is the intended breaking change, declared in this change's proposal.
