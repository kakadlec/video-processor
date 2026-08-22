## Context

`handleVideoUpload` (`cmd/api/video.go:200`) is the sole `POST /upload` handler. Today its first two lines are:

```go
file, header, err := c.Request.FormFile("video")
...
if !isValidVideoFile(header.Filename) { ... }
```

`(*http.Request).FormFile` calls `ParseMultipartForm(defaultMaxMemory)` if the form hasn't been parsed yet. Gin sets `defaultMaxMemory` to `MaxMultipartMemory` (32MiB by default, not overridden anywhere in this repo — confirmed via grep). `ParseMultipartForm` reads the **entire** body: parts under the memory cap are buffered in memory, anything over spills to an OS temp file via the standard library's own `os.CreateTemp`. Either way, by the time `FormFile` returns, the whole request body has already been received and (for anything past 32MiB) written to disk — regardless of what the extension turns out to be. Our own ordering (validate before saving/hashing, lines 211 vs. 243/256) is correct; the unavoidable-looking cost is inside the standard library call we're not even aware is doing I/O.

## Goals / Non-Goals

**Goals:**
- Reject an invalid-extension upload without reading its body past the filename, independent of declared or actual payload size.
- Preserve `POST /upload`'s exact external contract (status codes, response messages, accepted extensions, behavior for valid uploads).

**Non-Goals:**
- A request-body size limit / `MaxMultipartMemory` override — a related but separate concern (caps cost for a *valid*-looking request too); not addressed here.
- Content-sniffing validation (magic bytes) instead of extension-only — out of scope, matches the existing `isValidVideoFile` contract (`CLAUDE.md`'s own noted gotcha).

## Decisions

### Stream the multipart body via `MultipartReader`/`NextPart` instead of `FormFile`

`c.Request.MultipartReader()` returns a `*multipart.Reader` that yields parts one at a time via `NextPart()`, without buffering anything — the caller decides when (and whether) to read each part's body. Reading just the next part's header (`part.FormName()`, `part.FileName()`) costs a few bytes of multipart framing, not the part's payload.

`handleVideoUpload` now:
1. Calls `c.Request.MultipartReader()`. An error here (e.g. not a multipart request at all) is reported the same way `FormFile`'s error was — `400` with `"Erro ao receber arquivo: " + err.Error()`.
2. Loops `NextPart()` looking for a part whose `FormName() == "video"` **and** `FileName() != ""` (matching `FormFile`'s own semantics: a part without a filename is a plain value field, not a file — `net/http`'s `FormFile` only ever returns parts from `r.MultipartForm.File`, never `.Value`). Any other part encountered along the way is closed and skipped without being read (a `multipart.Part`'s body must be drained or the reader can't safely advance — `io.Copy(io.Discard, p)` before `p.Close()`). `io.EOF` with no match found is treated identically to `FormFile`'s "no such file" error today.
3. Validates `isValidVideoFile(part.FileName())` **before** the part's body is read at all — this is the actual fix; the check itself doesn't move relative to where it already was.
4. Everything downstream (`io.TeeReader(part, hasher)` into `io.Copy`) is otherwise unchanged — `*multipart.Part` implements `io.ReadCloser`, a drop-in replacement for the `multipart.File` that `FormFile` returned.

The frontend (`cmd/api/web/app.js`) only ever sends a single `video` field (confirmed — `formData.append('video', file)`, nothing else), and every existing test (`cmd/api/main_test.go`'s `uploadVideo`/`uploadEmptyForm` helpers) only exercises that same single-field shape, so the "skip non-matching parts" loop is defensive, not load-bearing for any currently-tested path.

**Alternative considered:** set `c.Request.Body = http.MaxBytesReader(...)` and/or lower `router.MaxMultipartMemory` to shrink the buffered-before-rejection window instead of eliminating it. Rejected as insufficient — it reduces the cost but doesn't remove it, and doesn't address the core issue that filename should gate body reads at all, not just cap how much of the body gets buffered before the (still-late) check.

## Risks / Trade-offs

- **[Risk]** Manually draining/closing skipped parts is easy to get subtly wrong (e.g. forgetting to drain before `Close`, or leaking a goroutine-free but still-open part on an early return). → **Mitigation**: single small helper function, unit-tested directly against the "no video field" and "video field is not the first part" cases in addition to the primary large-invalid-extension scenario.
- **[Risk]** `MultipartReader()` returns an error if the form was already parsed via `ParseMultipartForm` elsewhere in the pipeline (e.g. by a debug/logging middleware) — calling it twice on the same request panics/errors. → **Mitigation**: confirmed via grep that nothing in `cmd/api` (middleware or otherwise) calls `ParseMultipartForm`/`FormFile`/`PostForm` before `handleVideoUpload` runs for this route.

## Migration Plan

No data migration; pure behavior fix inside one handler. Rollback is a plain revert — no state to unwind.
