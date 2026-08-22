## Why

`POST /upload` (`handleVideoUpload`, `cmd/api/video.go`) validates the uploaded file's extension via `isValidVideoFile` immediately after calling `c.Request.FormFile("video")` — but `FormFile` itself calls `net/http`'s own `(*Request).ParseMultipartForm` under the hood, which reads the **entire** multipart request body up front (spilling anything past the standard library's own 32MiB `defaultMaxMemory` to a temp file on disk) before the filename is even available to our own check. This is `net/http`'s limit, not Gin's — the handler calls `c.Request.FormFile` directly rather than Gin's own `c.FormFile`/`c.MultipartForm()` wrapper, so Gin's configurable `Engine.MaxMultipartMemory` (which happens to default to the same 32MiB value) is never consulted for this code path. So a large upload with an invalid extension pays the full transfer/disk cost of a legitimate upload before being rejected — confirmed by manual testing: a large file with an unsupported extension took as long to reject as a real upload takes to process. This is also a resource-exhaustion concern: an unauthenticated-by-content-type attacker (any authenticated user, since this is behind bearer auth) can force the server to fully receive and buffer arbitrary-sized garbage before any check runs.

## What Changes

- `handleVideoUpload` reads the upload via `c.Request.MultipartReader()` and `NextPart()` instead of `c.Request.FormFile("video")`, so the "video" part's filename is available (and validated) before the handler consumes, drains, or persists any of its body — an invalid extension is now rejected immediately regardless of upload size.
- No change to the endpoint's external contract: same status codes, same response messages, same accepted extensions, same behavior for a valid upload (still saved to `uploads/`, still hashed in the same `io.Copy` pass).

## Capabilities

### New Capabilities
- `upload-file-validation`: `POST /upload` validates the uploaded file's extension before the handler consumes, drains, or persists any of its body — a resource-bound guarantee no existing spec currently asserts (today it's an accidental side effect of implementation, and demonstrably broken by `FormFile`'s eager whole-body read).

### Modified Capabilities
(none)

## Impact

- **Changed code**: `cmd/api/video.go`'s `handleVideoUpload` (and a small new helper for the streaming part lookup). No changes to `internal/video/*` or any other handler.
- **Tests**: new test proving the fix empirically (bytes read from the request body stay small for an invalid-extension upload, regardless of declared/actual payload size) — a naive status-code-only test would pass identically before and after this fix.
- **Dependencies**: none new — `mime/multipart` is already transitively used via `net/http`/Gin.
- **Security**: reduces resource-exhaustion exposure for `POST /upload` (an authenticated user could otherwise force full-body buffering with a garbage-extension payload).
