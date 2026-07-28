## Why

While verifying the `add-integration-tests` change, we manually confirmed a gap: `temp/` cleanup was only exercised implicitly (never asserted), and the failure path for `uploads/` was never tested at all. A real check (uploading a `.mp4`-named file with undecodable content) showed the uploaded file is left behind in `uploads/` forever when `ffmpeg` fails, while `temp/` is still cleaned up correctly either way. This needs to be captured as tests before the refactor, so this gap either stays intentional or gets fixed on purpose — not accidentally.

## What Changes

- Add two integration tests to `main_test.go`:
  - `temp/` per-request directory is removed after a request completes, on both success and failure.
  - On a processing failure, the uploaded file is *not* removed from `uploads/` (documents current behavior, doesn't fix it).
- No production code changes.

## Capabilities

### New Capabilities
(none)

### Modified Capabilities
- `video-frame-extraction`: adds two previously-uncaptured requirements about cleanup behavior on the failure path (temp dir always cleaned; uploaded file retained on failure as a known gap).

## Impact

- `main_test.go`: two new test functions, no production code touched.
- `openspec/specs/video-frame-extraction/spec.md`: two new requirements documenting cleanup behavior that already exists but wasn't previously specified.
