## Context

Follow-up to `add-integration-tests` (already archived). No new architectural decisions — same harness (`setupRouter()` + `httptest`), same "characterize, don't fix" approach from that change's design.

## Goals / Non-Goals

**Goals:**
- Assert `temp/` cleanup explicitly for both outcomes (previously only exercised implicitly).
- Capture the `uploads/` leak-on-failure as a test, documenting it as known/current behavior.

**Non-Goals:**
- Fixing the `uploads/` leak. That's a real behavior change and belongs in the refactor, as a deliberate decision, not slipped in here.

## Decisions

**Trigger an `ffmpeg` failure with a valid-extension file that has undecodable content**, rather than mocking `exec.Command`. Consistent with the existing harness: no mocking, real `ffmpeg`, real filesystem.

**Assert `temp/` cleanliness via `filepath.Glob("temp/*")` excluding `.gitkeep`**, rather than trying to know the exact per-request subdirectory name — the harness has no hook into the server-side timestamp, and asserting "no leftover subdirectories at all" is sufficient given tests don't run concurrently.

## Risks / Trade-offs

- [Glob-based assertion on `temp/` could pass for the wrong reason if a previous test already left it clean] → Low risk: `defer os.RemoveAll` runs per-request regardless of outcome, and this is exactly the mechanism being verified, so a prior test's cleanup doesn't mask a real regression in a *new* request's cleanup.
