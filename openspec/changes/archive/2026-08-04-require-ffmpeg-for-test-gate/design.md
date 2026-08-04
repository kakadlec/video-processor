## Context

`main_test.go`'s `TestMain` checks for `ffmpeg` before running the suite:

```go
func TestMain(m *testing.M) {
    if _, err := exec.LookPath("ffmpeg"); err != nil {
        fmt.Println("SKIP: ffmpeg não encontrado no PATH — ...")
        os.Exit(0)   // ← reports success; zero tests ran
    }
    createDirs()
    os.Exit(m.Run())
}
```

`os.Exit(0)` signals success to the caller — `go test`, Make, CI, a shell script, or a developer's local run. A machine without `ffmpeg` gets a green `go test` run with zero tests exercised, indistinguishable from a run where all tests passed.

CI is unaffected in practice (the `test` job installs `ffmpeg` first), but the false green is still a correctness problem: the exit code contract of `go test` is that 0 means "all tests ran and passed", not "nothing ran and we didn't notice".

## Goals / Non-Goals

**Goals:**
- Make "ffmpeg absent" a clearly reported failure (`os.Exit(1)`) rather than a silent skip.
- Keep the behavior for environments where `ffmpeg` is present completely unchanged.

**Non-Goals:**
- Installing `ffmpeg` automatically or embedding a bundled binary.
- Changing CI configuration — CI already installs `ffmpeg`; no workflow file needs touching.
- Adding, removing, or modifying any existing test case.
- Changing any file other than `main_test.go`.

## Decisions

**`os.Exit(1)` rather than `t.Skip` or a build tag.** `TestMain` runs before any `*testing.T` exists, so `.Skip()` is unavailable. Build tags (`//go:build requires_ffmpeg`) would let environments opt out gracefully, but that's a separate ergonomics change with its own trade-offs; the exit-code correction is the minimal, precise fix. The distinction between exit 0 and exit 1 is exactly right: 0 = "everything I ran succeeded", 1 = "a hard prerequisite was missing".

**Replace the Portuguese skip message with an English error message.** CLAUDE.md policy: new and changed strings use English. The existing Portuguese message is grandfathered in place; the replacement is new text and must be English. It should identify the missing prerequisite and point to the Docker fallback documented in `CLAUDE.md`.

## Risks / Trade-offs

- [Developer experience shift] Environments without `ffmpeg` will now see a failing `go test` where they previously saw a pass. This is intentional — a false green is worse than a visible, actionable failure — and `CLAUDE.md` already documents the Docker escape hatch.
- [No other test files affected] No other test code depends on or needs to know about this change.

## Open Questions

None. Scope is a single exit-code constant and a string replacement.
