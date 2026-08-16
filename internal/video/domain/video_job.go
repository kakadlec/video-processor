package domain

import (
	"errors"
	"time"
)

// ErrVideoJobIDRequired is returned when constructing a VideoJob without a valid VideoJobID.
var ErrVideoJobIDRequired = errors.New("video: video job id is required")

// ErrVideoJobIDGeneratorRequired is returned when NewVideoJob is called without a VideoJobIDGenerator.
var ErrVideoJobIDGeneratorRequired = errors.New("video: video job id generator is required")

// ErrVideoJobUserIDRequired is returned when constructing a VideoJob without a valid UserID.
var ErrVideoJobUserIDRequired = errors.New("video: user id is required")

// ErrOriginalFilenameRequired is returned when constructing a VideoJob without a valid OriginalFilename.
var ErrOriginalFilenameRequired = errors.New("video: original filename is required")

// ErrInvalidJobStatus is returned when constructing a VideoJob with a JobStatus outside the defined set.
var ErrInvalidJobStatus = errors.New("video: invalid job status")

// ErrFrameCountNegative is returned when constructing a VideoJob with a negative FrameCount.
var ErrFrameCountNegative = errors.New("video: frame count must not be negative")

// ErrFrameCountRequiresCompletedStatus is returned when constructing a
// VideoJob with a non-zero FrameCount whose status is not completed.
var ErrFrameCountRequiresCompletedStatus = errors.New("video: non-zero frame count requires completed status")

// ErrErrorReasonRequiresFailedStatus is returned when constructing a
// VideoJob with a non-empty ErrorReason whose status is not failed.
var ErrErrorReasonRequiresFailedStatus = errors.New("video: error reason requires failed status")

// ErrStorageKeyRequiresCompletedStatus is returned when constructing a
// VideoJob whose StorageKey is set but status is not completed, or whose
// status is completed but StorageKey is unset — ddd-architecture's "StorageKey
// for the result is set only on completion" scenario ties the two together.
var ErrStorageKeyRequiresCompletedStatus = errors.New("video: storage key and completed status must be set together")

// VideoJob is the Video Processing bounded context's aggregate root.
// FrameCount and ErrorReason are aggregate-validated primitive fields rather
// than standalone value objects: their invariants are cross-field (they
// depend on JobStatus), so a standalone type could only ever enforce part of
// the rule. RestoreVideoJob additionally enforces the same kind of
// cross-field consistency between StorageKey and JobStatus, per
// ddd-architecture's "StorageKey for the result is set only on completion"
// scenario — StorageKey itself remains a standalone value object (it still
// owns its own non-empty invariant), only the completed<->set pairing is
// checked here.
type VideoJob struct {
	id               VideoJobID
	userID           UserID
	originalFilename OriginalFilename
	storageKey       StorageKey
	frameCount       int
	errorReason      string
	status           JobStatus
	createdAt        time.Time
}

// NewVideoJob creates a brand-new VideoJob, minting its VideoJobID through
// the supplied generator. It always produces status pending, FrameCount 0,
// an empty ErrorReason, and an unset StorageKey.
func NewVideoJob(generator VideoJobIDGenerator, userID UserID, filename OriginalFilename, createdAt time.Time) (*VideoJob, error) {
	if generator == nil {
		return nil, ErrVideoJobIDGeneratorRequired
	}
	return RestoreVideoJob(generator.NewVideoJobID(), userID, filename, StorageKey{}, 0, "", JobStatusPending, createdAt)
}

// RestoreVideoJob reconstructs a VideoJob from already-known, already-validated values, e.g. from storage.
func RestoreVideoJob(id VideoJobID, userID UserID, filename OriginalFilename, storageKey StorageKey, frameCount int, errorReason string, status JobStatus, createdAt time.Time) (*VideoJob, error) {
	if id.IsZero() {
		return nil, ErrVideoJobIDRequired
	}
	if userID.IsZero() {
		return nil, ErrVideoJobUserIDRequired
	}
	if filename.IsZero() {
		return nil, ErrOriginalFilenameRequired
	}
	if !status.IsValid() {
		return nil, ErrInvalidJobStatus
	}
	if frameCount < 0 {
		return nil, ErrFrameCountNegative
	}
	if frameCount != 0 && status != JobStatusCompleted {
		return nil, ErrFrameCountRequiresCompletedStatus
	}
	if errorReason != "" && status != JobStatusFailed {
		return nil, ErrErrorReasonRequiresFailedStatus
	}
	if storageKey.IsZero() == (status == JobStatusCompleted) {
		return nil, ErrStorageKeyRequiresCompletedStatus
	}

	return &VideoJob{
		id:               id,
		userID:           userID,
		originalFilename: filename,
		storageKey:       storageKey,
		frameCount:       frameCount,
		errorReason:      errorReason,
		status:           status,
		createdAt:        createdAt,
	}, nil
}

// ID returns the job's opaque identifier.
func (j *VideoJob) ID() VideoJobID {
	return j.id
}

// UserID returns the job's owning user.
func (j *VideoJob) UserID() UserID {
	return j.userID
}

// OriginalFilename returns the validated source filename.
func (j *VideoJob) OriginalFilename() OriginalFilename {
	return j.originalFilename
}

// StorageKey returns the job's result storage key, unset unless completed.
func (j *VideoJob) StorageKey() StorageKey {
	return j.storageKey
}

// FrameCount returns the number of extracted frames, 0 unless completed.
func (j *VideoJob) FrameCount() int {
	return j.frameCount
}

// ErrorReason returns the failure reason, empty unless failed.
func (j *VideoJob) ErrorReason() string {
	return j.errorReason
}

// Status returns the job's current lifecycle status.
func (j *VideoJob) Status() JobStatus {
	return j.status
}

// CreatedAt returns when the job was created.
func (j *VideoJob) CreatedAt() time.Time {
	return j.createdAt
}
