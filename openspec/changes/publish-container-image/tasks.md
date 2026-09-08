## 1. Make the image multi-platform

- [ ] 1.1 Pin the builder stage to the build platform (`FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS builder`) and declare `ARG TARGETARCH` in it. Leave `test` as `FROM builder` — that inheritance is what keeps the test stage native, and it is a requirement, not a side effect.
- [ ] 1.2 Pass `GOARCH=$TARGETARCH` to **all five** `go build` invocations. The five are chained with `&&`; a value reaching four of them produces an image that builds, pushes and scans clean, with one binary that dies at `exec` on one platform only.
- [ ] 1.3 Verify locally with `docker buildx build --platform linux/amd64,linux/arm64` that both platforms build, then, for each platform, confirm the architecture of all five binaries by **reading their ELF headers on the host** — extract them from the image (`docker create` + `docker cp`, or a `--output type=local` build) and inspect the files. Do not execute them: that needs an emulator. And do not substitute `docker buildx imagetools inspect`, which reads the manifest's *declared* platform and never opens a layer — a manifest can say `linux/arm64` over an image holding an amd64 binary, which is precisely the defect being hunted, so that check would pass through it.
- [ ] 1.4 Run `ffmpeg -version` inside the `arm64` runtime image. This one does require executing a foreign binary: on a host without `binfmt_misc` registered for `arm64` (a plain WSL2 or Linux install typically has none) it fails with `exec format error`, which is an unregistered emulator and not a finding about the image. Register it (`tonistiigi/binfmt`) or run the check where emulation exists; **do not record this task as passed on the strength of a skipped run.**
- [ ] 1.5 Confirm the local loop is untouched: `docker compose run --build --rm app-test go test ./... -v` still builds the `test` target natively and the suite passes. Compare build time against a run from before the change — a large regression means the test stage stopped being native.

## 2. The pull-request gate

- [ ] 2.1 Add an image-build job to `.github/workflows/ci.yml`, in the shape of the existing `sast`/`vulncheck` jobs: set up **QEMU and then** buildx — the non-native `runtime` stage runs `apk`, so without the emulator registered the job fails on the second platform — build the runtime target for both published platforms with `push: false`, and use the GitHub Actions build cache so repeat runs do not rebuild the emulated layer from scratch.
- [ ] 2.2 In the same job, build the `test` target for the runner's platform only. It backs the documented local test command and is the one stage the runtime build does not exercise end to end.
- [ ] 2.2a In the same job, assert per platform that all five binaries are compiled for that platform, and make the job fail when one is not. Read the ELF headers of the extracted files, as in 1.3 — not the manifest's declared platform, which is metadata the defect does not disturb. Without this the gate is green for the exact defect it exists to catch, since the image builds either way. Verify the assertion by temporarily dropping `GOARCH` from one of the five builds and confirming the job goes red.
- [ ] 2.3 Confirm the job requests no registry permission and pushes nothing — a pull request from a fork must be able to run it.
- [ ] 2.3a Register the new job's check name in `main`'s branch protection required checks, so a red image build blocks the merge. Until this is done the job reports and blocks nothing, and the gate is a notification. Verify by confirming the check appears as required on an open PR — the four names together, not three plus a job that happens to run.
- [ ] 2.4 Record the job's wall-clock time on a first (cold-cache) run and a second (warm) run, and put both in the PR description. This is the data the design's open question needs; without it, the both-platforms-on-PR decision stays a guess.

## 3. The publish workflow

- [ ] 3.1 Add a publish job to `.github/workflows/release-please.yml` that `needs` the release job and runs only when it actually created a release. **Verify the action's real output names against `googleapis/release-please-action@v5`** rather than assuming `release_created`/`tag_name`; the design deliberately does not pin them.
- [ ] 3.2 Give the job `packages: write` (and the `contents: read` it needs), log in to GHCR with the workflow's own `GITHUB_TOKEN`, and push the runtime image for both platforms as one manifest list, tagged with the release version. Confirm the image reference is lowercase.
- [ ] 3.2a Move `latest` onto that image only when no higher version is already published — `latest` tracks the highest published version, not the last publication, so republishing an older tag must leave it where it is. For an automatic release publication the two coincide; the check exists for the deliberate path below.
- [ ] 3.3 Add a `workflow_dispatch` trigger with a required tag input, checking out the **named tag** rather than `main`. There is no replace/force input — immutability admits no override.
- [ ] 3.3b Validate that input before anything is built: it must match the release-tag form and must resolve to a tag that exists. An unvalidated free-text input reaches a build minutes later with a confusing failure, or publishes under a tag nobody intended.
- [ ] 3.3a Guard the existing `release-please` job so a `workflow_dispatch` run does not also execute it: the trigger applies to the whole workflow, and a routine image recovery must not create or update a Release PR. Then write the publish job's condition so it still runs when that job was **skipped** — a skipped `needs` dependency is not a satisfied one, so a bare `needs` plus an output check silently disables the manual path.
- [ ] 3.4 Make the publish refuse a version that already has a published image — one registry lookup before the push, against the normalized tag. Confirm both paths normalize identically (`vX.Y.Z` → `X.Y.Z`): a check against one string and a push to another makes the refusal decorative.
- [ ] 3.4a Add a concurrency group keyed on the normalized version so publications for the same version serialize. The lookup alone is time-of-check/time-of-use — two runs for the same absent version can both see "absent" and both push.
- [ ] 3.5 Confirm that a merge to `main` which creates no release publishes nothing.

## 4. Bootstrap and verify against the real registry

- [ ] 4.1 After the implementation PR merges, trigger the dispatch publish for `v4.0.0`. This is both the bootstrap and the first real exercise of the publish path. Expect the non-native build to be slow: `v4.0.0`'s tree predates the `$BUILDPLATFORM` pin, so its Go toolchain runs emulated. That is accepted for a historical tag — the binaries are still correct — and is not a reason to publish anything other than what the tag contains.
- [ ] 4.2 Set the new GHCR package's visibility to public. It is **created private** regardless of the repository being public, so this is a step to perform, not a property to confirm.
- [ ] 4.2a Then pull the version tag from a client genuinely logged out of GHCR (`docker logout ghcr.io` first) and confirm it succeeds. A logged-in client pulls a private package happily and proves nothing.
- [ ] 4.3 Confirm both tags resolve to the same image and that both platforms are present in the manifest list.
- [ ] 4.4 Start one process from the pulled image (the default command, with configuration deliberately absent) and confirm it fails on missing configuration rather than on a missing or wrong-architecture binary — that failure mode is the one thing the gate cannot show.
- [ ] 4.5 Re-run the dispatch for `v4.0.0` and confirm it refuses, with no input available that would make it proceed.
- [ ] 4.6 Confirm `latest` points at `4.0.0` after the bootstrap, since it is the highest published version. The republish-does-not-move-`latest` rule cannot be exercised until a second version exists; note that as untested-in-practice in the PR rather than claiming it was verified.

## 5. Quality gates

- [ ] 5.1 `go vet ./...` and `go test ./... -v` — the diff carries no Go module input, so this is a confirmation that nothing was disturbed, not the change's gate.
- [ ] 5.2 `gosec ./...` and `govulncheck ./...` clean, as CI runs them on every PR regardless of what the diff touches.
- [ ] 5.3 `git diff --check`, and confirm the three required checks plus the new image-build job are green on the PR.

## 6. Finalization (after the implementation PR merges — not part of it)

- [ ] 6.1 Update `docs/operations.md`: the deployment section gains the pull path alongside the existing local build, and the CI/CD section gains the image-build gate and the publication, including what publication does **not** do — there is no deployment target and none is implied.
- [ ] 6.2 Update `README.md`'s quickstart with the pull command, naming the version that actually exists rather than a placeholder.
- [ ] 6.3 Update `CLAUDE.md`'s container-image bullet: one image still carries all five binaries, and it is now published, multi-platform, at a named registry path.
- [ ] 6.4 Add the `publish-container-image` row to `docs/roadmap.md`'s Change Backlog, marked archived with links. It belongs to no phase, like `split-api-by-bounded-context`.
- [ ] 6.5 `npx --yes @fission-ai/openspec validate publish-container-image --strict --no-interactive`, then archive.
