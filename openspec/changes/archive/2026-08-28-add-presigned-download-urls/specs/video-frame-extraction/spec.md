## MODIFIED Requirements

### Requirement: Zip Packaging Of Extracted Frames

The system SHALL package all extracted frames into a single downloadable `.zip` file, containing exactly the extracted PNG frames, and SHALL store that zip as an object in the configured MinIO bucket under the job's derived storage key. The zip SHALL NOT be written to, or served from, a local `outputs/` directory.

#### Scenario: Zip contains all extracted frames

- **WHEN** frame extraction succeeds for an upload
- **THEN** the response includes a `zip_path`, and following the URL `GET /download/:filename` issues for that path yields a zip archive whose entry count matches the reported `frame_count`

#### Scenario: The zip is stored in the bucket, not on local disk

- **WHEN** frame extraction succeeds for an upload
- **THEN** the zip exists as an object in the configured bucket, and the transient zip this request produced under `temp/` no longer exists after the request completes

### Requirement: Processed Files Listing

The system SHALL expose `GET /api/status`, returning the authenticated caller's `completed` jobs' results with filename, size, creation time, and a download URL for each. The listing SHALL be derived from the caller's own `VideoJob` records — never from an enumeration of stored objects — with size and creation time read from each stored object. A result whose object cannot be read SHALL be omitted rather than shown.

The `download_url` each entry carries SHALL address `GET /download/:filename` on this API, not the storage service. The listing SHALL NOT contain a presigned URL: minting one per entry would start every expiry at listing time and place one credential per completed job into a single response body. The listing names *where to ask*; the asking is what issues a credential.

The endpoint has no unauthenticated behavior: it is reachable only behind bearer authentication, and there is no listing that is not scoped to a single user.

#### Scenario: Newly created zip appears in status listing

- **WHEN** an upload has been successfully processed
- **THEN** `GET /api/status` includes an entry whose `filename` matches the returned `zip_path`

#### Scenario: Listing is scoped to the authenticated owner

- **GIVEN** completed jobs produced by two different authenticated users
- **WHEN** one of them requests `GET /api/status` with a valid bearer token
- **THEN** the response lists only that caller's own results, and `total` counts only those

#### Scenario: The listing carries no credential

- **WHEN** an authenticated caller requests `GET /api/status`
- **THEN** every entry's `download_url` addresses this API, and no entry carries a signed URL, signature parameter, or expiry

### Requirement: Processed File Download

The system SHALL expose `GET /download/:filename`, issuing a bounded-lifetime URL that grants access to the stored object when the authenticated caller owns the `completed` `VideoJob` that key belongs to, and responding `HTTP 404` with a generic `File not found` error body otherwise. The endpoint SHALL NOT return the object's bytes itself; the transfer occurs between the client and the storage service, with this API absent from the data path.

Entitlement SHALL be determined from the `VideoJob` record, not from an ownership record stored alongside the artifact. Because an issued URL carries no identity, that determination SHALL be made at issuance and SHALL be the complete authorization decision. The not-found and not-owned responses SHALL be indistinguishable, so a caller cannot use them to probe for artifacts belonging to someone else.

The endpoint has no unauthenticated behavior: it is reachable only behind bearer authentication, and there is no entitlement rule that applies in the absence of an identity.

#### Scenario: Existing zip is downloadable by its owner

- **WHEN** an authenticated client requests `GET /download/:filename` for the storage key of a `completed` job it owns, whose object is present
- **THEN** the server responds `HTTP 200` with a URL for that object and the instant it stops being accepted, and following that URL yields the zip's binary content

#### Scenario: Nonexistent file returns 404

- **WHEN** a client requests `GET /download/:filename` for a key that belongs to no job, or whose object is not in the bucket
- **THEN** the server responds `HTTP 404` and issues no URL

#### Scenario: Another user's zip is indistinguishable from a missing one

- **GIVEN** a `completed` job owned by user A
- **WHEN** user B requests its storage key with a valid bearer token
- **THEN** the server responds `HTTP 404` with the same body it returns for a key that belongs to no job

#### Scenario: The API is not in the transfer path

- **GIVEN** an authenticated owner who has obtained a URL for their own completed result
- **WHEN** the object is transferred
- **THEN** the bytes travel from the storage service to the client, and no response from this API carries them
