package storage

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/minio/minio-go/v7"

	"video-processor/internal/video/domain"
)

const resultContentType = "application/zip"

// noSuchKeyCode is the S3 error code MinIO returns for a missing object.
const noSuchKeyCode = "NoSuchKey"

// The SigV4 query parameters a presigned URL carries, and the layout of the
// first. They are read back to report the grant's real admission deadline.
const (
	signatureDateParam    = "X-Amz-Date"
	signatureExpiresParam = "X-Amz-Expires"
	signatureDateLayout   = "20060102T150405Z"
)

var _ domain.ResultStorage = (*ResultStorage)(nil)

// ResultStorage implements domain.ResultStorage against a MinIO bucket.
type ResultStorage struct {
	client    *minio.Client
	presigner *minio.Client
	bucket    string
}

// NewResultStorage wires a ResultStorage to an already-opened client, the
// presign-only client from OpenPresigner, and the bucket its artifacts live
// in. The two clients are distinct on purpose: presigner is built against
// the browser-facing host and is never dialed, while client does every
// operation that actually talks to the server.
func NewResultStorage(client, presigner *minio.Client, bucket string) *ResultStorage {
	return &ResultStorage{client: client, presigner: presigner, bucket: bucket}
}

// Put uploads the file at localPath under key. FPutObject rather than
// PutObject: the content length comes from the file itself, where a bare
// reader would have to be buffered to learn its size.
func (s *ResultStorage) Put(ctx context.Context, key domain.StorageKey, localPath string) error {
	if _, err := s.client.FPutObject(ctx, s.bucket, key.String(), localPath, minio.PutObjectOptions{
		ContentType: resultContentType,
	}); err != nil {
		return fmt.Errorf("%w: %s: %s", domain.ErrResultStoreFailed, key.String(), err.Error())
	}
	return nil
}

// PresignGet signs a URL granting read access to the stored artifact.
//
// response-content-disposition travels as a request parameter, so MinIO
// returns the Content-Disposition this asks for. It is covered by the
// signature — altering it after issuance yields 403 — which matters because
// the HTML download attribute is ignored cross-origin, leaving the header as
// the only thing that makes the response a download.
//
// Signing is offline: an absent key signs without error, and the 404 would
// only appear when the URL is followed. Callers that need existence resolved
// Stat first.
func (s *ResultStorage) PresignGet(ctx context.Context, key domain.StorageKey, ttl time.Duration, downloadFilename string) (string, time.Time, error) {
	params := url.Values{}
	params.Set("response-content-disposition", `attachment; filename="`+downloadFilename+`"`)

	signed, err := s.presigner.PresignedGetObject(ctx, s.bucket, key.String(), ttl, params)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%w: %s: %s", domain.ErrResultPresignFailed, key.String(), err.Error())
	}

	expiresAt, err := signedExpiry(signed)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%w: %s: %s", domain.ErrResultPresignFailed, key.String(), err.Error())
	}
	return signed.String(), expiresAt, nil
}

// signedExpiry reads the deadline out of the grant itself rather than
// computing time.Now().Add(ttl) beside the signing call. The two are not
// equivalent: the library stamps X-Amz-Date at whole-second precision and
// truncates X-Amz-Expires to whole seconds, so the computed value runs ahead
// of what the server enforces — measured at 561ms in one issuance, with a
// requested 5m0.5s signed as exactly 300 seconds. Overstating is the harmful
// direction, since a client trusting the reported instant retries into a 403.
func signedExpiry(signed *url.URL) (time.Time, error) {
	query := signed.Query()

	rawDate := query.Get(signatureDateParam)
	if rawDate == "" {
		return time.Time{}, fmt.Errorf("signed url carries no %s", signatureDateParam)
	}
	signedAt, err := time.Parse(signatureDateLayout, rawDate)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse %s %q: %w", signatureDateParam, rawDate, err)
	}

	rawExpires := query.Get(signatureExpiresParam)
	if rawExpires == "" {
		return time.Time{}, fmt.Errorf("signed url carries no %s", signatureExpiresParam)
	}
	seconds, err := strconv.Atoi(rawExpires)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse %s %q: %w", signatureExpiresParam, rawExpires, err)
	}

	return signedAt.Add(time.Duration(seconds) * time.Second), nil
}

// Stat reports the stored artifact's size and last-modified time.
func (s *ResultStorage) Stat(ctx context.Context, key domain.StorageKey) (int64, time.Time, error) {
	info, err := s.client.StatObject(ctx, s.bucket, key.String(), minio.StatObjectOptions{})
	if err != nil {
		return 0, time.Time{}, s.wrap(err, "stat result", key)
	}
	return info.Size, info.LastModified, nil
}

// wrap maps a missing object onto the domain's own sentinel and wraps
// everything else, so a caller can tell "not stored" from "storage failed"
// without matching on MinIO error codes of its own.
func (s *ResultStorage) wrap(err error, operation string, key domain.StorageKey) error {
	if minio.ToErrorResponse(err).Code == noSuchKeyCode {
		return fmt.Errorf("%w: %s", domain.ErrResultNotFound, key.String())
	}
	return fmt.Errorf("video: %s %q: %w", operation, key.String(), err)
}
