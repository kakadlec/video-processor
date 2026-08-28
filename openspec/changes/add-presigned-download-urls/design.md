## Context

`GET /download/:filename` today authorizes from the `VideoJob` row, calls `ResultStorage.Open`, and copies the object into the response with `c.DataFromReader`. Every result byte therefore crosses `cmd/api`'s address space and occupies one of its connections for the duration of the transfer. The bucket that holds those bytes can serve them directly, and S3-compatible storage has a standard mechanism for doing so under a bounded grant: a presigned URL.

The constraints this design has to satisfy are not obvious from the outside, and three of them were established by probing the pinned stack (`minio-go v7.3.0` against `minio/minio:RELEASE.2025-04-22T22-12-26Z`) rather than read out of documentation:

1. **`PresignedGetObject` is not unconditionally offline.** On a client with no configured `Region`, it first issues `GetBucketLocation` — a real request to the endpoint — and only then signs. Against an endpoint the server cannot resolve it fails outright (`dial tcp: lookup … no such host`). With `Region` set it never touches the network: measured at ~150 µs against a host that does not exist.
2. **SigV4 signs `Host`.** The minted URL's host comes from the client's endpoint and cannot be rewritten afterwards. A presigning client must be built against the browser-facing host to begin with.
3. **`response-content-disposition` is signed and honored.** MinIO returns the requested `Content-Disposition` verbatim, and rejects a URL whose disposition was altered after signing with `403`. That parameter, not the HTML `download` attribute, is what makes a cross-origin result a download.

Two further behaviors were confirmed because the requirement wording depends on them, and both cut against the intuitive phrasing:

- **Expiry bounds admission, not transfer.** A URL minted with a 2-second TTL, followed immediately, streamed 64 MiB to completion over 4 seconds. A second request on the same URL then returned `403 AccessDenied` / "Request has expired". "The URL expires after five minutes" is therefore false as stated; what is true is that MinIO refuses a request that *arrives* after the expiry instant.
- **Issuance does not prove existence.** Signing is local, so a key that holds no object presigns without error; the `404` appears only when the URL is followed.

The library also enforces `1s <= TTL <= 7 days`, returning a clear error outside that range, and rejects an endpoint whose scheme contradicts the `Secure` option — both of which this design leans on rather than re-validating.

## Goals / Non-Goals

**Goals:**

- Take `cmd/api` out of the result-byte path entirely, so an API replica's resource envelope is set by request rate rather than by artifact size.
- Preserve the existing entitlement contract exactly — the same five conditions, evaluated in the same place — and the byte-identical `404` that makes the endpoint useless as a probe for other users' artifacts.
- Make the browser-facing host explicit configuration, so the common deployment shape (an internal service name the browser cannot resolve) is a configuration decision rather than a silent failure.
- Write the URL's lifetime into the specs as what the server enforces, not as what the design intends.

**Non-Goals:**

- Presigned *upload* URLs. `POST /upload` must read every byte anyway — to hash it for idempotency and to reject an invalid extension before the transfer is paid for — so direct-to-bucket upload would relocate both checks somewhere they cannot run.
- Revocation. A presigned URL cannot be withdrawn; the TTL is the whole of it.
- Re-keying result objects. The flat key is still consumed as a route path segment (see Decision 3), and changing it would orphan every object already stored.
- Any change to the write path: aggregate, lifecycle, extraction, and storage of the result are untouched.
- Pagination on `GET /api/status`, which stays a frontend-coupled follow-up.

## Decisions

### 1. The route survives; only its response body changes

`GET /download/:filename` keeps its path, its bearer gate, its rate-limit membership, and its entitlement logic, and returns `{"url": ..., "expires_at": ...}` instead of the zip.

*Alternatives considered.* A `302` to the presigned URL would have preserved `app.js`'s `fetch()` unchanged — but `fetch` following a cross-origin redirect must then read a cross-origin response, which makes the feature depend on MinIO's CORS configuration, and it keeps the whole result materialized as a blob in browser memory. Issuing the URL as JSON and navigating to it is not CORS-gated at all. A second route (`GET /api/downloads/:filename`) alongside the existing proxy would have been non-breaking, but leaves two paths to the same bytes and two copies of the entitlement check — and the roadmap's scope for this change is replacing the proxy, not supplementing it.

### 2. `ResultStorage.Open` is removed rather than left in place

`handleDownload` was its only caller. A port method with no consumer is an invitation to route bytes back through the API, which is the thing this change exists to stop. `Stat` stays — `GET /api/status` uses it, and Decision 5 gives it a second caller.

### 3. The flat result key is still load-bearing

`videojob-result-storage` justifies `frames_<jobID>.zip` containing no `/` by the key being used verbatim as `GET /download/:filename`'s single path segment. Since the route survives with the same shape, that justification survives unchanged. This is written down because the natural inference from "the frontend no longer downloads through the API" is that the constraint has lapsed, and acting on that inference would orphan every stored object.

### 4. Two clients: one internal, one presign-only

`setupVideo` keeps its existing client for `Put`/`Stat`/`Get`/`Delete` and constructs a second one whose only job is signing. The second is built from `VIDEO_MINIO_PUBLIC_ENDPOINT` (defaulting to `VIDEO_MINIO_ENDPOINT`) and `VIDEO_MINIO_PUBLIC_USE_SSL` (defaulting to `VIDEO_MINIO_USE_SSL`), so a deployment where one endpoint serves both audiences needs no new variables.

That second client is never pinged and never used for a bucket operation, because in the general case it points at a host the server cannot reach. `add-minio-infrastructure` already specified that `Open` performs no connectivity check, so this is a use the existing contract permits rather than a new exception — but the code says so explicitly, since "we construct a client we deliberately never talk to" is otherwise indistinguishable from a wiring bug.

*Alternative considered.* Minting the URL with the internal client and rewriting the host afterwards. Rejected on evidence, not preference: SigV4 covers the `Host` header, so the rewritten URL fails signature verification.

### 5. The region is discovered at startup, not configured

The presign-only client must carry a `Region` or its first signing attempt will try to reach a host it cannot reach (Context, item 1). `setupVideo` already opens, pings, and ensures the bucket against the reachable client; it additionally calls `GetBucketLocation` there and passes the result into the presign-only client's options.

*Alternatives considered.* A `VIDEO_MINIO_REGION` variable is one more thing an operator can set wrong, for a value the server can simply ask for. Hardcoding `us-east-1` — which is what this MinIO reports — would be correct for MinIO and wrong for real S3 in any other region, and would fail in a way that is hard to read (MinIO accepts a mismatched region, so the bug stays invisible until the deployment moves to S3). Discovery is one round trip on an already fail-closed startup path.

### 6. The TTL is a fixed five-minute constant

No environment variable, matching the status cache's fixed TTL. The interval that actually has to be survived is the gap between the JSON response and the browser's navigation, which is sub-second: issuance happens when the user clicks, not when the listing renders. Five minutes leaves several orders of magnitude of headroom for a slow client without leaving a usable credential lying around.

*Alternative considered.* Minting the URL directly into `GET /api/status`'s `download_url`, removing a round trip. Rejected: it starts every TTL at listing time — so a listing left open outlives its own links — and puts one credential per completed job into a single response body, multiplying the exposure of a value that must not be logged.

### 6a. The specs describe expiry as request admission, not as transfer duration

The requirement wording is "MinIO SHALL reject a request that arrives after the expiry instant", never "the URL stops working after five minutes" or "no download continues past expiry". A transfer already in flight runs to completion (Context: 64 MiB over 4 s on a 2 s TTL), and clock skew between the signing process and MinIO shifts the effective instant in both directions.

This is called out as its own decision because the same defect — an obligation or a bound written up as an absolute — has been found in review three times in this phase, in three different artifact types. The scenarios are where it keeps reappearing, not the requirement text, so the scenarios are what must be checked.

### 7. A `Stat` precedes issuance, to keep the `404` uniform

Because signing is offline, a key naming no object presigns happily; the failure would then surface as a `404` from MinIO with an XML body, at a different origin, instead of the byte-identical `404` this endpoint promises for every rejection. One `Stat` before signing closes that: a missing object is refused at issuance, exactly like an unparseable key or someone else's job.

The cost is one round trip per download, on a path that previously made one anyway (`Open` resolves existence before returning). The residual race — the object deleted between `Stat` and the browser's `GET` — is not closable by any amount of checking and is not worth pretending otherwise.

### 8. Every issuance failure renders as the same `404`

Presign failure, `Stat` failure, and storage unreachability join the existing rejection list and produce the identical body. A presign failure is in practice unreachable (the TTL is a constant inside the library's accepted range and the key is already validated), but the branch exists and must not be the one that leaks an endpoint name into a response.

### 9. The URL is a credential, and is treated as one

It is never logged, never echoed into an error, never included in a failure message. The existing handlers log the *key* on failure; the new paths do the same. This is a code-review property more than a testable one, which is why it is stated here and reflected in a spec requirement rather than left implicit.

### 9a. The issuance response is `Cache-Control: no-store`

An authenticated `200` carrying JSON is ordinarily cacheable, and the JSON here is a credential. A private or user-agent cache retaining it would preserve working access past the request that asked for it, which is precisely what the five-minute lifetime exists to prevent. The header goes on every response the endpoint produces, not only the successful one — the rejection responses are required to be byte-identical down to headers, and a directive present on one path and absent on another would break that.

### 9b. The reported expiry is read off the signature, not off the clock

`expires_at` is derived by parsing the issued URL's `X-Amz-Date` and adding its `X-Amz-Expires` seconds. Computing `time.Now().Add(ttl)` alongside the signing call looks equivalent and is not: the library stamps the signature's start instant at whole-second precision and truncates the requested lifetime to whole seconds. Measured against the pinned stack, the naive value overstated the real admission window by 561 ms in a single issuance, and a requested `5m0.5s` signed as exactly `300` seconds.

The direction of the error is what makes it worth a decision rather than a rounding note. An `expires_at` that *overstates* the window tells a client the credential is still good when the service has already stopped accepting it; a client that schedules a retry against that instant retries into a `403`. Reading the value off the credential itself cannot drift from what the credential actually says.

### 10. `app.js` navigates; it does not fetch the artifact

`downloadFile()` fetches the issuance endpoint with the bearer token, reads `url`, and drives an anchor at it. The `download` attribute is not relied on — it is ignored cross-origin — so the attachment behavior comes entirely from the signed `response-content-disposition`. User-facing copy stays pt-BR per the language policy; the Go side stays English.

## Risks / Trade-offs

- **A wrong or unset public endpoint produces URLs that are correctly signed and unreachable.** The API cannot detect this, because it never follows a URL it issues → Compose ships the host-mapped `127.0.0.1:9000` so the default local stack works end to end; `docs/operations.md` documents the failure mode explicitly, including that the symptom is a browser error rather than an API error.
- **An issued URL cannot be revoked.** Deleting the job or transferring ownership leaves an outstanding URL working until it expires → the TTL is five minutes, and the spec states non-revocability as a property rather than leaving it to be discovered.
- **Result bytes stop being rate limited.** Issuance is limited; the transfer is between browser and MinIO → accepted. `rate-limiting`'s spec is amended so "every authenticated route is rate limited" is not read as "every byte served is rate limited". Bounding transfer bandwidth is an object-storage-side concern, not an API middleware's.
- **The bucket must be reachable from the browser's network.** Previously only the API needed reach; now the client does too → an operational fact, not a defect, but one that changes the deployment topology and belongs in the docs rather than in a surprise.
- **A test that only inspects the URL string proves nothing.** Query-parameter assertions pass happily against a URL that `403`s → at least one test fetches the issued URL against the real test MinIO, with no `Authorization` header, and compares bytes. Structural assertions are supplementary to that, never a substitute.
- **An issuance response could be cached.** It is an authenticated `200` whose body is a credential → `Cache-Control: no-store` on every response from the endpoint (Decision 9a), asserted in tests on both the success and the rejection path.
- **Breaking change for non-browser clients.** Anything scripting `GET /download/...` for the zip now receives JSON → the frontend is updated in the same change; there is no other known consumer, and the roadmap has scoped this behavior since Phase 5 was planned.

## Migration Plan

No data migration: keys, objects, and rows are untouched, and the change is confined to how a stored object is handed back. Deployment order is irrelevant — a rolled-back binary serves the old streaming response from the same objects.

The one deployment step is configuration. A stack where the browser and the API reach MinIO at the same address needs nothing. A stack like `docker-compose.yml`, where they do not, needs `VIDEO_MINIO_PUBLIC_ENDPOINT` set before the change ships, or downloads will fail in the browser while the API reports success. Since both new variables are optional and default to their internal counterparts, the failure is a misconfiguration rather than a startup refusal — which is the right trade (a single-endpoint deployment must not be forced to declare a redundant variable), but it is the reason this is called out as a step rather than assumed.

Rollback is reverting the binary and, optionally, unsetting the two new variables; neither leaves residue.

## Open Questions

None blocking. One deliberately deferred: whether `GET /api/status` should eventually return issued URLs directly, saving a round trip per download. Decision 6 rejects it for this change on TTL and credential-exposure grounds, but it becomes worth revisiting if the listing ever gains pagination — a bounded page is a bounded number of credentials, which is a materially different trade from an unbounded listing.
