## ADDED Requirements

### Requirement: Extension Validation Precedes Any Body Read

`POST /upload` SHALL determine the uploaded file's extension (via the multipart part's filename) and reject an unsupported one with `400` without the handler consuming, draining, or persisting any of that part's payload, regardless of the request's declared or actual content length. This constrains the handler's own behavior, not `mime/multipart`'s internal parsing: the standard library's buffered reader may incidentally read ahead a small amount past a part's header boundary while scanning for the next one, bounded by the parser's own fixed-size internal buffer rather than by the request's payload size — that bounded, parser-internal read-ahead is not something the requirement forbids or is even able to observe. What it does forbid is the handler itself reading, copying, hashing, or writing any of that payload to disk before the extension is known to be valid.

#### Scenario: A large upload with an invalid extension is rejected without buffering its body

- **GIVEN** a `POST /upload` request whose "video" part has an unsupported file extension and a body far larger than a single read buffer
- **WHEN** the request is handled
- **THEN** the response is `400` with the existing unsupported-format message, and only a negligible amount of the part's body (not the full payload) was read off the wire before responding

#### Scenario: A valid upload is unaffected

- **GIVEN** a `POST /upload` request whose "video" part has a supported extension
- **WHEN** the request is handled
- **THEN** the file is saved and hashed exactly as before, with no change to `POST /upload`'s existing success behavior

#### Scenario: A request with no "video" field is still rejected the same way

- **GIVEN** a `POST /upload` request with no part named "video" (or none with a filename)
- **WHEN** the request is handled
- **THEN** the response is `400`, matching the existing missing-file behavior
