## Context

The repository builds five Go binaries into one image (`Dockerfile`, stages `builder` → `test` → `runtime`) and starts each process by overriding the command. That image is built in exactly two places today, both of them a human's laptop: `docker compose up --build` and `docker compose run --build --rm app-test`. CI never builds it, and nothing publishes it.

Three existing mechanisms constrain how this is done:

- **`release-please` already owns versioning.** It maintains a Release PR from Conventional Commits, and merging it creates the tag, the GitHub Release and the `CHANGELOG.md` entry. Any publication trigger that computes its own version, or that fires on something other than a release actually being created, duplicates that authority.
- **`docker-compose.yml` is the local development contract**, and `--build` is load-bearing: `container-image`'s own spec and `CLAUDE.md` both state that a stale image reporting green is the failure it prevents.
- **`CGO_ENABLED=0`** is already in the builder, which is the whole reason a second platform is affordable here: the compile step can cross-compile exactly, with no emulation.

## Goals / Non-Goals

**Goals:**
- A `Dockerfile` break cannot reach `main`.
- A released version has a corresponding immutable, pullable image, for `linux/amd64` and `linux/arm64`.
- The image that exists the day this ships is `v4.0.0` — not "whatever release happens next".
- The local development and test loop is byte-for-byte unaffected.

**Non-Goals:**
- Any deployment, any target environment, any orchestration.
- Signing, SBOM, provenance attestations, image vulnerability scanning.
- A moving `edge`/`main` tag, or any image produced by a merge rather than by a release.
- Platforms beyond `linux/amd64` and `linux/arm64`.

## Decisions

### D1 — Publication is a second job in `release-please.yml`, gated on the release actually being created

`googleapis/release-please-action` reports whether it created a release and which tag it created. A publish job that `needs` it and runs only on that condition inherits the authority that already exists instead of re-deriving it.

*Alternatives considered.* A separate workflow triggered by `release: published` is the shape one reaches for first, and it does not work here: a release created by an action using the workflow's own `GITHUB_TOKEN` does not trigger further workflow runs. `release-please` creates the release exactly that way, so such a workflow would sit silent through every release — green, correct-looking, and never running. This is the decisive reason for the same-workflow job, not a stylistic preference. A `push: tags: ['v*']` trigger avoids that limitation but fires for any tag anyone pushes, which is a wider door than intended.

*Consequence of adding `workflow_dispatch` to this file (D2).* The trigger applies to the whole workflow, so a manual publish would otherwise also run the `release-please` job and let a routine image recovery create or update a Release PR. The release job must therefore be guarded by event name, and the publish job's condition must be written so that it still runs when the release job was skipped rather than merely inheriting a `needs` success — a skipped dependency is not a satisfied one.

*To verify during implementation.* The action's output names are a v5 detail, not a design commitment; the job must be wired against what v5 actually emits, confirmed on a real run rather than from memory.

### D2 — A `workflow_dispatch` input publishes a tag that already exists, and refuses one that already has an image

Without this the change ships inert. `v4.0.0` is already tagged, and this change's own commits are `ci:`/`build:`/`docs:`, none of which bumps a version under Conventional Commits — so the first image would appear only when some unrelated `feat:` or `fix:` lands, while the documentation this change writes already tells a reader to pull `4.0.0`.

The dispatch path checks out the named tag and builds *that* tree, so a manually published `v4.0.0` is the code of `v4.0.0` rather than the code of `main` wearing its number.

It also **refuses to publish over a version that already has an image**, with no override. An earlier draft offered a second boolean input to force a replacement; that is guarded mutability wearing the word immutability, and a consumer cannot rely on a guarantee that a flag suspends. Replacing a published version now requires deleting it from the registry first — a deliberate human act, outside this pipeline, that no mistyped input can perform.

Both paths normalize before checking and pushing: git tag `vX.Y.Z`, image tag `X.Y.Z`. Without a stated normalization the automatic path and the manual path can check one string and push another, which turns the refusal above into decoration.

Two honest limits of the check. It is a registry lookup and therefore time-of-check/time-of-use: two runs for the same absent version could both observe "absent" and both push. Serializing publication by version — a concurrency group keyed on the normalized tag — is what actually closes that, and the lookup is the guard for the case that will really happen, which is a human typing a tag that already shipped. And the bootstrap publishes a tag whose tree predates this change, so its `Dockerfile` has no `$BUILDPLATFORM` pin and its non-native build emulates the Go toolchain; the binaries are still correct for their platform, only the build is slow. That is accepted for historical tags rather than fixed, because the alternative is publishing something other than what the tag contains.

*Alternative considered.* Publishing `:edge` (or `:main-<sha>`) on every merge would also make an image exist immediately. Rejected: it produces an image for every docs commit, it has no consumer this repository can name, and it does not solve the actual problem, which is that **`4.0.0`** — the version the README will tell people to pull — has no image.

### D3 — Cross-compile in the builder; emulate only the runtime layer

```
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder   # always native
ARG TARGETARCH                                                  # set by BuildKit
RUN CGO_ENABLED=0 GOARCH=$TARGETARCH go build ...  (×5)

FROM builder AS test          # inherits BUILDPLATFORM ⇒ still native
FROM alpine:3.24 AS runtime   # target platform ⇒ apk runs emulated
```

Pinning the builder to `$BUILDPLATFORM` is what keeps the Go toolchain native: compiling Go under QEMU costs minutes per platform for no benefit, since `CGO_ENABLED=0` cross-compiles exactly. Only the `runtime` stage's `apk add ffmpeg` and `adduser` run emulated, and only for the non-native platform.

The `test` stage inheriting the build platform is a **required** consequence, not an accident: `docker compose run --build --rm app-test` must keep running a native Go toolchain, and an emulated one would make the documented local test path unusable.

*Scope restriction.* `GOARCH=$TARGETARCH` is a direct substitution that holds because Go's architecture names coincide with BuildKit's for exactly the two platforms published here. A 32-bit ARM platform would additionally need `GOARM` and is out of scope; this is a reason the platform list is fixed rather than open.

### D4 — The pull-request gate builds both published platforms, and the `test` target natively

The gate exists so the artifact cannot break silently. Building only the runner's platform would let an `arm64`-only failure — an `apk` package that resolves differently, a base image without that architecture — reach `main` and surface at release time, which is the worst moment.

It also builds the `test` target, natively and once. That target backs the documented local test command, and it is the one stage the `runtime` build does not exercise end to end.

Nothing is pushed from a pull request. The workflow needs no registry write permission there, and a fork's PR cannot publish anything.

*Alternative considered.* Native-only on pull requests, both platforms only at release: faster, and the tuning knob if the emulated layer proves slow or flaky, at the cost named in the proposal.

### D5 — Two tags: the version, and `latest` tracking the highest version

`ghcr.io/kakadlec/video-processor:4.0.0` and `:latest`. No `:4` or `:4.0` moving pointers — every additional moving tag is another way for a reader to run something other than what they think, and this project has one consumer profile (someone evaluating a release), not a fleet with an upgrade policy.

`latest` follows the **highest published version**, not the most recent publication. The two coincide for every automatic publication and diverge for exactly one case that D2 makes reachable: republishing an older tag after a newer one exists. Defining `latest` as "the last thing published" would make that recovery hand an unversioned puller an older image than they had before — a regression caused by an operation whose purpose was to repair something. So the publish path compares against what is already published before moving `latest`, and the bootstrap case falls out of the same rule: `v4.0.0` is the highest published version precisely because nothing is published yet.

The registry path must be lowercase; the owner and repository names already are.

## Risks / Trade-offs

- **An emulated `apk` layer is a single point of flakiness** → It fails the whole manifest list rather than degrading to one platform, so a transient failure blocks a merge or a publish. Mitigated by build caching and by D4's named fallback to native-only pull-request builds.
- **A wrong-architecture binary is invisible** → The builder chains five `go build` calls; if `GOARCH` reaches four of them, the image builds, pushes and passes every check, and one service dies with `exec format error` at runtime, in a deployment, on one platform only. Mitigated by verifying the architecture of all five binaries in the built image, per platform, as an explicit task rather than as an inference.
- **`ffmpeg` on `alpine:3.24/arm64` is assumed, not known** → The worker is the only process that shells out to it and the only one whose failure would be silent at build time. Mitigated by running `ffmpeg -version` inside the built `arm64` image.
- **A GHCR package is private when it is created** → It is not inherited from a public repository, so anonymous pullability has to be set, not confirmed. Left unset, the documented pull command fails for exactly the audience it is written for, while every check stays green. Mitigated by making the visibility change an explicit bootstrap step and verifying it from a genuinely logged-out client — a logged-in one pulls a private package happily and proves nothing.
- **`latest` is a moving tag by definition** → Accepted. It is documented as a convenience and the version tag is documented as the reproducible one.
- **The publish job's correctness cannot be fully proven before merge** — it runs only on `main`, after a release. Mitigated by D2's dispatch path being the first thing exercised after merge, which is also the bootstrap step, so the first real use is a deliberate one rather than a surprise during someone's release.

## Migration Plan

1. Merge. Nothing is published by the merge itself.
2. Trigger the dispatch publish for `v4.0.0`. This is both the bootstrap and the first end-to-end exercise of the publish path.
3. Verify: pull from a logged-out client on both platforms, run `--version`-equivalent startup for one binary, confirm the five binaries' architectures.
4. Document the pull path in the finalization PR, naming the version that actually exists.

Rollback is deleting the GHCR package or its tag; nothing in the running system depends on the registry, because nothing is deployed from it.

## Open Questions

- Whether both-platform pull-request builds stay, or fall back to native-only, once there is real data on how long the emulated layer takes. Deliberately left to be answered by the first few runs rather than guessed now.
