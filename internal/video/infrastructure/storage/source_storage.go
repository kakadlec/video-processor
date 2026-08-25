package storage

import (
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"

	"video-processor/internal/video/domain"
)

// sourceUploadPartSize bounds the buffer PutObject allocates per upload.
//
// A multipart.Part does not report its length, so Put passes size -1. With
// an unknown size and no configured part size, minio-go v7.3.0 routes into
// putObjectMultipartStreamNoLength, which calls OptimalPartInfo(-1, 0) —
// that substitutes 5TiB for the unknown size, divides by the 10000-part
// maximum, and the caller then allocates a single buffer of the result:
// roughly 537MiB, per that function's own comment. Nothing limits
// concurrent uploads here, so that constant would be multiplied by every
// request in flight.
//
// 16MiB is the library's minPartSize, well above the 5MiB floor
// OptimalPartInfo enforces. With a configured part size the library caps the
// object at partSize * 10000 = 160GiB, far beyond anything this service
// accepts.
const sourceUploadPartSize = 16 * 1024 * 1024

var _ domain.SourceStorage = (*SourceStorage)(nil)

// SourceStorage implements domain.SourceStorage against a MinIO bucket. It
// shares the bucket with ResultStorage; the key prefix is what separates the
// two key spaces.
type SourceStorage struct {
	client *minio.Client
	bucket string
}

// NewSourceStorage wires a SourceStorage to an already-opened client and the
// bucket its objects live in.
func NewSourceStorage(client *minio.Client, bucket string) *SourceStorage {
	return &SourceStorage{client: client, bucket: bucket}
}

// Put streams r into the bucket under key.
func (s *SourceStorage) Put(ctx context.Context, key domain.StorageKey, r io.Reader) error {
	if _, err := s.client.PutObject(ctx, s.bucket, key.String(), r, -1, minio.PutObjectOptions{
		PartSize: sourceUploadPartSize,
	}); err != nil {
		return fmt.Errorf("%w: %s: %s", domain.ErrSourceStoreFailed, key.String(), err.Error())
	}
	return nil
}

// Get downloads the stored object to localPath.
func (s *SourceStorage) Get(ctx context.Context, key domain.StorageKey, localPath string) error {
	if err := s.client.FGetObject(ctx, s.bucket, key.String(), localPath, minio.GetObjectOptions{}); err != nil {
		return s.wrap(err, "get source", key)
	}
	return nil
}

// Delete removes the object stored under key.
//
// Probed against minio-go v7.3.0 and minio/minio
// RELEASE.2025-04-22T22-12-26Z: RemoveObject returns a nil error for a key
// that holds no object, so the port's "absent is not an error" contract
// needs no special case here. The NoSuchKey mapping below stays for a
// backend that behaves otherwise.
func (s *SourceStorage) Delete(ctx context.Context, key domain.StorageKey) error {
	if err := s.client.RemoveObject(ctx, s.bucket, key.String(), minio.RemoveObjectOptions{}); err != nil {
		if minio.ToErrorResponse(err).Code == noSuchKeyCode {
			return nil
		}
		return fmt.Errorf("video: delete source %q: %w", key.String(), err)
	}
	return nil
}

// wrap maps a missing object onto the domain's own sentinel and wraps
// everything else, mirroring ResultStorage.wrap.
func (s *SourceStorage) wrap(err error, operation string, key domain.StorageKey) error {
	if minio.ToErrorResponse(err).Code == noSuchKeyCode {
		return fmt.Errorf("%w: %s", domain.ErrSourceNotFound, key.String())
	}
	return fmt.Errorf("video: %s %q: %w", operation, key.String(), err)
}
