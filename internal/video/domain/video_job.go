package domain

import (
	"errors"
	"time"
)

var (
	ErrVideoJobIDRequired          = errors.New("video: video job id is required")
	ErrVideoJobIDGeneratorRequired = errors.New("video: video job id generator is required")
	ErrVideoJobUserIDRequired      = errors.New("video: video job user id is required")
	ErrOriginalFilenameRequired    = errors.New("video: original filename is required")
	ErrInvalidJobStatus            = errors.New("video: invalid job status")
	ErrInvalidFrameCount           = errors.New("video: frame count must not be negative")
	ErrFrameCountBeforeCompletion  = errors.New("video: frame count must be zero before completion")
	ErrStorageKeyRequired          = errors.New("video: storage key is required for a completed job")
	ErrStorageKeyBeforeCompletion  = errors.New("video: storage key must be unset before completion")
	ErrErrorReasonRequired         = errors.New("video: error reason is required for a failed job")
	ErrErrorReasonBeforeFailure    = errors.New("video: error reason must be empty before failure")
)

// VideoJob is the aggregate root for one video-processing request.
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

// NewVideoJob creates a pending job with a freshly minted identifier.
func NewVideoJob(
	generator VideoJobIDGenerator,
	userID UserID,
	filename OriginalFilename,
	createdAt time.Time,
) (*VideoJob, error) {
	if generator == nil {
		return nil, ErrVideoJobIDGeneratorRequired
	}
	return RestoreVideoJob(
		generator.NewVideoJobID(),
		userID,
		filename,
		StorageKey{},
		0,
		"",
		JobStatusPending,
		createdAt,
	)
}

// RestoreVideoJob reconstructs a job while enforcing aggregate invariants.
func RestoreVideoJob(
	id VideoJobID,
	userID UserID,
	filename OriginalFilename,
	storageKey StorageKey,
	frameCount int,
	errorReason string,
	status JobStatus,
	createdAt time.Time,
) (*VideoJob, error) {
	if id.IsZero() {
		return nil, ErrVideoJobIDRequired
	}
	if userID.IsZero() {
		return nil, ErrVideoJobUserIDRequired
	}
	if filename.IsZero() {
		return nil, ErrOriginalFilenameRequired
	}
	if !status.isValid() {
		return nil, ErrInvalidJobStatus
	}
	if frameCount < 0 {
		return nil, ErrInvalidFrameCount
	}
	if status != JobStatusCompleted && frameCount != 0 {
		return nil, ErrFrameCountBeforeCompletion
	}
	if status == JobStatusCompleted && storageKey.IsZero() {
		return nil, ErrStorageKeyRequired
	}
	if status != JobStatusCompleted && !storageKey.IsZero() {
		return nil, ErrStorageKeyBeforeCompletion
	}
	if status == JobStatusFailed && errorReason == "" {
		return nil, ErrErrorReasonRequired
	}
	if status != JobStatusFailed && errorReason != "" {
		return nil, ErrErrorReasonBeforeFailure
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

func (job *VideoJob) ID() VideoJobID { return job.id }

func (job *VideoJob) UserID() UserID { return job.userID }

func (job *VideoJob) OriginalFilename() OriginalFilename { return job.originalFilename }

func (job *VideoJob) StorageKey() StorageKey { return job.storageKey }

func (job *VideoJob) FrameCount() int { return job.frameCount }

func (job *VideoJob) ErrorReason() string { return job.errorReason }

func (job *VideoJob) Status() JobStatus { return job.status }

func (job *VideoJob) CreatedAt() time.Time { return job.createdAt }
