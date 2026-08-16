package domain

import (
	"errors"
	"time"
)

var (
	ErrVideoJobIDRequired          = errors.New("video: video job id is required")
	ErrVideoJobIDGeneratorRequired = errors.New("video: video job id generator is required")
	ErrVideoJobUserIDRequired      = errors.New("video: user id is required")
	ErrOriginalFilenameRequired    = errors.New("video: original filename is required")
	ErrInvalidJobStatus            = errors.New("video: invalid job status")
	ErrInvalidFrameCount           = errors.New("video: invalid frame count")
	ErrInvalidErrorReason          = errors.New("video: invalid error reason")
	ErrInvalidResultStorageKey     = errors.New("video: invalid result storage key")
)

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

func NewVideoJob(generator VideoJobIDGenerator, userID UserID, filename OriginalFilename, createdAt time.Time) (*VideoJob, error) {
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
	if !status.IsValid() {
		return nil, ErrInvalidJobStatus
	}
	if frameCount < 0 || (status != JobStatusCompleted && frameCount != 0) {
		return nil, ErrInvalidFrameCount
	}
	if (status == JobStatusFailed && errorReason == "") || (status != JobStatusFailed && errorReason != "") {
		return nil, ErrInvalidErrorReason
	}
	if (status == JobStatusCompleted && storageKey.IsZero()) || (status != JobStatusCompleted && !storageKey.IsZero()) {
		return nil, ErrInvalidResultStorageKey
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

func (j *VideoJob) ID() VideoJobID                     { return j.id }
func (j *VideoJob) UserID() UserID                     { return j.userID }
func (j *VideoJob) OriginalFilename() OriginalFilename { return j.originalFilename }
func (j *VideoJob) StorageKey() StorageKey             { return j.storageKey }
func (j *VideoJob) FrameCount() int                    { return j.frameCount }
func (j *VideoJob) ErrorReason() string                { return j.errorReason }
func (j *VideoJob) Status() JobStatus                  { return j.status }
func (j *VideoJob) CreatedAt() time.Time               { return j.createdAt }
