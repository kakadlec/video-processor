# Changelog

## [3.0.0](https://github.com/kakadlec/video-processor/compare/v2.0.0...v3.0.0) (2026-09-05)


### ⚠ BREAKING CHANGES

* **notification:** PUT /api/notification-preferences now refuses an "http" destination and a destination naming a non-globally-reachable address with 400. Both were accepted before. NOTIFICATION_ALLOW_INSECURE_DESTINATIONS restores the previous behaviour for a local stack; production deployments that registered such a destination must re-register it over https against a publicly reachable host.

### Features

* **notification:** add the delivery record and the secret read path ([#215](https://github.com/kakadlec/video-processor/issues/215)) ([a0bf800](https://github.com/kakadlec/video-processor/commit/a0bf8007b15582a704d88072515c4a6ef8f53923))
* **notification:** add the NotificationPreference domain ([#207](https://github.com/kakadlec/video-processor/issues/207)) ([03c3d9a](https://github.com/kakadlec/video-processor/commit/03c3d9ac0a2a3ad896c997193870735ea68ae6da))
* **notification:** add the notifier entrypoint ([#218](https://github.com/kakadlec/video-processor/issues/218)) ([4de6407](https://github.com/kakadlec/video-processor/commit/4de64072fcc9daf819a64bc0d876e78c9de12328))
* **notification:** add the preference use cases ([#210](https://github.com/kakadlec/video-processor/issues/210)) ([4f1c740](https://github.com/kakadlec/video-processor/commit/4f1c740f208741840bffade16f863cf6c480f75d))
* **notification:** add the terminal event consumer ([#216](https://github.com/kakadlec/video-processor/issues/216)) ([7e9f793](https://github.com/kakadlec/video-processor/commit/7e9f793728ee4c88274e963b9521314701f8f793))
* **notification:** add the webhook deliverer and the delivery use case ([#217](https://github.com/kakadlec/video-processor/issues/217)) ([382303e](https://github.com/kakadlec/video-processor/commit/382303e1791ec8f1e7ac807e5ec97c2d173fd9ae))
* **notification:** add the webhook delivery domain ([#214](https://github.com/kakadlec/video-processor/issues/214)) ([9afb18d](https://github.com/kakadlec/video-processor/commit/9afb18da0c70267c8bc1aefc59532a62bba80541))
* **notification:** apply the destination policy at write time ([#220](https://github.com/kakadlec/video-processor/issues/220)) ([5a76e3c](https://github.com/kakadlec/video-processor/commit/5a76e3ca9ea33d875fbfbc051e1e32a834ba8b8f))
* **notification:** expose preference routes on cmd/api ([#211](https://github.com/kakadlec/video-processor/issues/211)) ([fcbe8d9](https://github.com/kakadlec/video-processor/commit/fcbe8d9623f4254b6d2f340599d877f337d9efd2))
* **notification:** persist notification preferences in PostgreSQL ([#209](https://github.com/kakadlec/video-processor/issues/209)) ([a78520b](https://github.com/kakadlec/video-processor/commit/a78520b86ef1bae5981a68bb1d105cc14d25be1c))

## [2.0.0](https://github.com/kakadlec/video-processor/compare/v1.0.0...v2.0.0) (2026-09-04)


### ⚠ BREAKING CHANGES

* POST /upload returns 202 with {job_id, status, status_url} instead of a completed processing result. Clients must poll GET /api/video-jobs/:id for the outcome. The job-dispatch topology moves to video.jobs.v2 / video.jobs.queued.v2 with routing key video_job.queued.v2; the .v1 exchange and queue are retired by an operator after the deploy.

### Features

* add the RabbitMQ connection adapter and job-dispatch topology ([#190](https://github.com/kakadlec/video-processor/issues/190)) ([6228cb1](https://github.com/kakadlec/video-processor/commit/6228cb13ccc513646759802b90473dce38c627d0))
* add VideoJob source key and the transactional outbox relay ([#194](https://github.com/kakadlec/video-processor/issues/194)) ([b9b77a5](https://github.com/kakadlec/video-processor/commit/b9b77a5521bd1fb146037490d3a2f4924953adc3))
* process uploads asynchronously in a dedicated worker ([#197](https://github.com/kakadlec/video-processor/issues/197)) ([b04bee4](https://github.com/kakadlec/video-processor/commit/b04bee405b96813dbdd81a7ee4c2f827beecd5e9))
* **video:** emit terminal job events through a transactional outbox ([#204](https://github.com/kakadlec/video-processor/issues/204)) ([aa6e463](https://github.com/kakadlec/video-processor/commit/aa6e463ef57750e0a454588201eed918e6987f74))
* **worker:** recover abandoned video jobs with a lease, a sweeper, and a fence epoch ([#201](https://github.com/kakadlec/video-processor/issues/201)) ([6151f1a](https://github.com/kakadlec/video-processor/commit/6151f1ac3c87118319b1e51b0ab24a81d790ff56))

## [1.0.0](https://github.com/kakadlec/video-processor/compare/v0.10.0...v1.0.0) (2026-08-28)


### ⚠ BREAKING CHANGES

* GET /download/:filename returns {"url", "expires_at"} JSON instead of the zip archive. Clients that read the archive from this endpoint must follow the returned URL instead.
* the /uploads static mount is removed. An authenticated owner can no longer retrieve their own source video over HTTP; the route returns 404 because it no longer exists. cmd/api/web/app.js never referenced it, so the bundled frontend is unaffected.
* MinIO configuration is now required at startup. setupVideo loads the config, opens the client, pings it, and ensures the bucket, failing fatally on any of the four — fail-closed, deliberately unlike every Redis backed feature, since a result that cannot be stored cannot be delivered. A deployment that does not set VIDEO_MINIO_ENDPOINT, VIDEO_MINIO_ACCESS_KEY, VIDEO_MINIO_SECRET_KEY, and VIDEO_MINIO_BUCKET will refuse to start. VIDEO_MINIO_USE_SSL remains optional.

### Features

* add MinIO connection adapter for the video context ([#174](https://github.com/kakadlec/video-processor/issues/174)) ([a56d29e](https://github.com/kakadlec/video-processor/commit/a56d29ee612eb1a8843771d64db6fd009aa7a836))
* issue presigned download URLs instead of proxying result bytes ([#186](https://github.com/kakadlec/video-processor/issues/186)) ([2e1e241](https://github.com/kakadlec/video-processor/commit/2e1e241dc9cd6dcdea8b95e1d3302f24ce70e085))
* store uploaded source videos in MinIO instead of the uploads directory ([#181](https://github.com/kakadlec/video-processor/issues/181)) ([00ccd75](https://github.com/kakadlec/video-processor/commit/00ccd75f24dc1486209bb78d8c7c1a16d8662a25))
* store video results in MinIO instead of the outputs directory ([#178](https://github.com/kakadlec/video-processor/issues/178)) ([e3157f9](https://github.com/kakadlec/video-processor/commit/e3157f9753f886fcae140443d1a790800f2b765b))

## [0.10.0](https://github.com/kakadlec/video-processor/compare/v0.9.0...v0.10.0) (2026-08-22)


### Features

* add per-user rate limiting middleware for video routes ([#151](https://github.com/kakadlec/video-processor/issues/151)) ([1ed5a1a](https://github.com/kakadlec/video-processor/commit/1ed5a1a691cb7942bc95a6a4c64f2262afc0129b))
* add Redis-backed status cache for VideoJob lookups ([#155](https://github.com/kakadlec/video-processor/issues/155)) ([6487cc0](https://github.com/kakadlec/video-processor/commit/6487cc0f2101ea95d5612a7f7a36075c3a34359f))
* fail open on Redis errors during upload idempotency reservation ([#162](https://github.com/kakadlec/video-processor/issues/162)) ([cede8a6](https://github.com/kakadlec/video-processor/commit/cede8a6047b637f98256c8868c1ad0d8553a3722))


### Bug Fixes

* reject invalid upload extension before reading file body ([#160](https://github.com/kakadlec/video-processor/issues/160)) ([476a6ec](https://github.com/kakadlec/video-processor/commit/476a6ec0746097cea8fb42f9743e844826be25d4))

## [0.9.0](https://github.com/kakadlec/video-processor/compare/v0.8.0...v0.9.0) (2026-08-21)


### Features

* add content-hash idempotency keys to POST /upload ([#144](https://github.com/kakadlec/video-processor/issues/144)) ([9da9167](https://github.com/kakadlec/video-processor/commit/9da9167dc439c1402fe33e6ce33edaacdbdcb07b))
* add internal/platform/redis connection adapter ([#138](https://github.com/kakadlec/video-processor/issues/138)) ([0954a36](https://github.com/kakadlec/video-processor/commit/0954a3656a35f75d85ad40a47f5a4720057ffbb8))
* migrate ffmpeg execution to VideoJob application layer ([#132](https://github.com/kakadlec/video-processor/issues/132)) ([2a624af](https://github.com/kakadlec/video-processor/commit/2a624afd3e90b9997c1059357e0bb1e19628e059))


### Bug Fixes

* include resolved Go toolchain version in the Go cache key ([#145](https://github.com/kakadlec/video-processor/issues/145)) ([761b95b](https://github.com/kakadlec/video-processor/commit/761b95bd92090b1438ac25e0a7e3d8cd67a87334))
* key gosec/govulncheck caches by resolved Go version ([#148](https://github.com/kakadlec/video-processor/issues/148)) ([7f166c4](https://github.com/kakadlec/video-processor/commit/7f166c407df7033cc471e9b31a0588eea20fdd1e))

## [0.8.0](https://github.com/kakadlec/video-processor/compare/v0.7.0...v0.8.0) (2026-08-17)


### Features

* add PostgreSQL adapter for VideoJob with transactional outbox ([#120](https://github.com/kakadlec/video-processor/issues/120)) ([34dc140](https://github.com/kakadlec/video-processor/commit/34dc140e78c01eae6b1e5f234bb68a0fd894c734))
* add repo-workflow and change-lifecycle Claude Code skills ([#98](https://github.com/kakadlec/video-processor/issues/98)) ([96fb06b](https://github.com/kakadlec/video-processor/commit/96fb06b912e58d65957b9cc95c2a27a47e2b0250))
* add VideoJob domain and application layers ([#116](https://github.com/kakadlec/video-processor/issues/116)) ([cf19322](https://github.com/kakadlec/video-processor/commit/cf19322f9fcd04624152f83b5931aedf5528aa19))
* extract frontend into static files served via go:embed ([#110](https://github.com/kakadlec/video-processor/issues/110)) ([c40a755](https://github.com/kakadlec/video-processor/commit/c40a755964d6074541e028d9abff4458895d2b0d))
* make roadmap-status flip and explore criteria row-optional ([#102](https://github.com/kakadlec/video-processor/issues/102)) ([67d0def](https://github.com/kakadlec/video-processor/commit/67d0def9f7f705f7f285ea91db8a18ccc5f0fc23))
* wire VideoJob use cases into HTTP endpoints ([#128](https://github.com/kakadlec/video-processor/issues/128)) ([13cd6cf](https://github.com/kakadlec/video-processor/commit/13cd6cf5c2e7901642f8a4835b986e8a77944886))

## [0.7.0](https://github.com/kakadlec/video-processor/compare/v0.6.0...v0.7.0) (2026-08-09)


### Features

* harden Dockerfile with a multi-stage, non-root build ([#81](https://github.com/kakadlec/video-processor/issues/81)) ([8ac9ef4](https://github.com/kakadlec/video-processor/commit/8ac9ef4debf6baf634af6b5c81f0ac641148d5cc))


### Bug Fixes

* require identity configuration on startup, no unauthenticated fallback ([#95](https://github.com/kakadlec/video-processor/issues/95)) ([c77f136](https://github.com/kakadlec/video-processor/commit/c77f136d0ff2a42c9a0d35775f68d78b162c6e30))

## [0.6.0](https://github.com/kakadlec/video-processor/compare/v0.5.0...v0.6.0) (2026-08-09)


### Features

* add docker-compose app service for full local stack ([#75](https://github.com/kakadlec/video-processor/issues/75)) ([4106f3a](https://github.com/kakadlec/video-processor/commit/4106f3a3d60616b616e3767737c65fe64ef8c791))

## [0.5.0](https://github.com/kakadlec/video-processor/compare/v0.4.0...v0.5.0) (2026-08-08)


### Features

* authenticate the built-in web UI ([#54](https://github.com/kakadlec/video-processor/issues/54)) ([c5b8696](https://github.com/kakadlec/video-processor/commit/c5b86965df45bb980bd2ab68855c4aaea1c0c0bf))


### Bug Fixes

* run the whole package in the Docker CMD, not just main.go ([#55](https://github.com/kakadlec/video-processor/issues/55)) ([3fe9f67](https://github.com/kakadlec/video-processor/commit/3fe9f67bdf94c71b8e15f394c58276caa5c47306))

## [0.4.0](https://github.com/kakadlec/video-processor/compare/v0.3.0...v0.4.0) (2026-08-08)


### Features

* enforce authenticated ownership of video artifacts ([b73da2e](https://github.com/kakadlec/video-processor/commit/b73da2e0b858e79581a66358d1af6ba87374fb80))
* enforce authenticated ownership of video artifacts ([1531901](https://github.com/kakadlec/video-processor/commit/1531901d05f5cc65e13280e72e4e6a7f67dba00a))


### Bug Fixes

* address Copilot review findings on artifact ownership ([3c0f555](https://github.com/kakadlec/video-processor/commit/3c0f555fa9ea86f7f09f5063b954069137932df0))

## [0.3.0](https://github.com/kakadlec/video-processor/compare/v0.2.0...v0.3.0) (2026-08-08)


### Features

* add bearer auth middleware ([85defe7](https://github.com/kakadlec/video-processor/commit/85defe73a8283af80d5a3ac0d8af2779aeb6c5cd))
* add bearer auth middleware ([a000b57](https://github.com/kakadlec/video-processor/commit/a000b570e9bb0c3706de2c92435ab89cf67c9af4))
* add HTTP register/login endpoints ([fd80328](https://github.com/kakadlec/video-processor/commit/fd803285e7363fde5aef2c4b7d78ec3686d3e894))
* add HTTP register/login endpoints ([43d06db](https://github.com/kakadlec/video-processor/commit/43d06db277a819f3d18222e9dea3ecd5af349b49))
* add identity domain primitives ([ebb49f8](https://github.com/kakadlec/video-processor/commit/ebb49f81d74d2fff0a24208b148441d1fa7c5e22))
* add identity domain primitives ([dea1ccb](https://github.com/kakadlec/video-processor/commit/dea1ccb259d9868a223c4b0872f0539735e7d9db))
* add password, JWT, and UUID infrastructure adapters ([9e0a053](https://github.com/kakadlec/video-processor/commit/9e0a053a576343959f4d3ca4cc0cc6b7aa8f970a))
* add password, JWT, and UUID infrastructure adapters ([89392f9](https://github.com/kakadlec/video-processor/commit/89392f9404515201ad65c748935b40e93bc7a2c0))
* add PostgreSQL persistence adapter and config ([f76452a](https://github.com/kakadlec/video-processor/commit/f76452ae213b277a3ffc4224f69ad524926cc747))
* add PostgreSQL persistence adapter and config ([fb5cbb9](https://github.com/kakadlec/video-processor/commit/fb5cbb922f423b4474154d3cabe55b7bf6fd5346))
* add RegisterUser and AuthenticateUser use cases ([1b42a3b](https://github.com/kakadlec/video-processor/commit/1b42a3b2a1114049e5e8dce62d69444f72be4806))
* add RegisterUser and AuthenticateUser use cases ([5e7e758](https://github.com/kakadlec/video-processor/commit/5e7e758f0ed32daa8867aa13e3929fedd861b893))
* protect video-processing routes with bearer auth ([dab382e](https://github.com/kakadlec/video-processor/commit/dab382e7cfd248a1778700653b6720bf8051c759))
* protect video-processing routes with bearer auth ([691a3b2](https://github.com/kakadlec/video-processor/commit/691a3b2306b687fa0d053ab69d445cf3d627392b))


### Bug Fixes

* fail tests when ffmpeg is unavailable ([768177f](https://github.com/kakadlec/video-processor/commit/768177fce51d83edb142b8babe6f5dad63d798ad))

## [0.2.0](https://github.com/kakadlec/video-processor/compare/v0.1.0...v0.2.0) (2026-07-28)


### Features

* document PR-required workflow ahead of branch protection ([b5d7044](https://github.com/kakadlec/video-processor/commit/b5d7044fb88ce65fdd0404d26c81fd881c0d4961))


### Bug Fixes

* add missing govulncheck CI job required by branch protection ([6b8e0d8](https://github.com/kakadlec/video-processor/commit/6b8e0d801a8381ca553d8bf542f6d79037a4c106))
* replace 3 of 4 nosec suppressions with real path containment checks ([7fcd95e](https://github.com/kakadlec/video-processor/commit/7fcd95ebc58765c00bf4ca6d3b7856b8f80ccfd3))
* resolve gosec SAST findings and dependency vulnerabilities ([b9e32cc](https://github.com/kakadlec/video-processor/commit/b9e32cc80e54590c2e0d1b8f697cb4587a628980))
* resolve gosec SAST findings and dependency vulnerabilities ([24f2137](https://github.com/kakadlec/video-processor/commit/24f21370d83ba1650849342167887716a817eb5c))
