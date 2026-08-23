package storage

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/minio/minio-go/v7"

	"video-processor/internal/video/domain"
)

const resultContentType = "application/zip"

// noSuchKeyCode is the S3 error code MinIO returns for a missing object.
const noSuchKeyCode = "NoSuchKey"

var _ domain.ResultStorage = (*ResultStorage)(nil)

// ResultStorage implements domain.ResultStorage against a MinIO bucket.
type ResultStorage struct {
	client *minio.Client
	bucket string
}

// NewResultStorage wires a ResultStorage to an already-opened client and the
// bucket its artifacts live in.
func NewResultStorage(client *minio.Client, bucket string) *ResultStorage {
	return &ResultStorage{client: client, bucket: bucket}
}

// Put uploads the file at localPath under key. FPutObject rather than
// PutObject: the content length comes from the file itself, where a bare
// reader would have to be buffered to learn its size.
func (s *ResultStorage) Put(ctx context.Context, key domain.StorageKey, localPath string) error {
	if _, err := s.client.FPutObject(ctx, s.bucket, key.String(), localPath, minio.PutObjectOptions{
		ContentType: resultContentType,
	}); err != nil {
		return fmt.Errorf("video: store result %q: %w", key.String(), err)
	}
	return nil
}

// Open returns a reader over the stored artifact and its size.
//
// GetObject does not perform the request: it returns an *minio.Object that
// issues the HTTP GET lazily. Probed against minio-go v7.3.0 and
// minio/minio RELEASE.2025-04-22T22-12-26Z: for an absent key GetObject
// returns a nil error, and only the object's own Stat reports NoSuchKey. So
// Stat is called here, before anything is handed back — which both resolves
// that error eagerly and yields the size the caller needs for
// Content-Length.
func (s *ResultStorage) Open(ctx context.Context, key domain.StorageKey) (io.ReadCloser, int64, error) {
	object, err := s.client.GetObject(ctx, s.bucket, key.String(), minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, s.wrap(err, "open result", key)
	}

	info, err := object.Stat()
	if err != nil {
		_ = object.Close()
		return nil, 0, s.wrap(err, "open result", key)
	}

	return object, info.Size, nil
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
