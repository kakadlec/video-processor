# Changelog

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
