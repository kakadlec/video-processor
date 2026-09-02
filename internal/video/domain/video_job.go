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
// VideoJob whose ErrorReason is set but status is not failed, or whose
// status is failed but ErrorReason is empty — the canonical aggregate
// scenario requires a failed job to always carry a non-empty reason, so
// corrupted persistence data must not be able to enter the domain as a
// valid aggregate.
var ErrErrorReasonRequiresFailedStatus = errors.New("video: error reason and failed status must be set together")

// ErrStorageKeyRequiresCompletedStatus is returned when constructing a
// VideoJob whose StorageKey is set but status is not completed, or whose
// status is completed but StorageKey is unset — ddd-architecture's "StorageKey
// for the result is set only on completion" scenario ties the two together.
var ErrStorageKeyRequiresCompletedStatus = errors.New("video: storage key and completed status must be set together")

// ErrInvalidStatusTransition is returned by a VideoJob transition method
// (Enqueue, StartProcessing, Complete, Fail) when the job's current status
// cannot legally make that transition.
var ErrInvalidStatusTransition = errors.New("video: invalid status transition")

// ErrFailureReasonRequired is returned by Fail when called with an empty reason.
var ErrFailureReasonRequired = errors.New("video: failure reason is required")

// ErrSourceKeyRequiredToEnqueue is returned by Enqueue when the job has no
// source key. A job with no stored source cannot be processed, so queueing it
// would produce a dispatch no worker can act on.
var ErrSourceKeyRequiredToEnqueue = errors.New("video: source key is required to enqueue")

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
	// sourceKey names the uploaded video in object storage. It is NOT
	// storageKey, which names the result zip and is set only on completion —
	// overloading one for the other is the confusion this field exists to
	// prevent. It may be empty: POST /api/video-jobs creates a job from a
	// filename with no stored object at all. What such a job cannot do is be
	// enqueued; see Enqueue.
	sourceKey StorageKey
	// contentHash is the SHA-256 of the uploaded bytes, hex-encoded. It is
	// the second half of the IdempotencyKey the submitting request derived,
	// persisted so a component that only has the job — the worker — can
	// rebuild that key without the request that made it. It is empty for a
	// job created without an upload, and deliberately not paired with any
	// status: see RestoreVideoJob.
	contentHash string
	storageKey  StorageKey
	frameCount  int
	errorReason string
	status      JobStatus
	createdAt   time.Time
	// leaseEpoch counts how many times this job has been requeued after
	// being abandoned, and doubles as the fence identifying the holder of
	// the current run: a terminal write carrying a superseded epoch is
	// refused. It is deliberately not paired with any status — every status
	// is reachable at every epoch, and the column ships with a default that
	// must keep pre-existing rows loadable, exactly as sourceKey and
	// contentHash do.
	leaseEpoch int64
}

// NewVideoJob creates a brand-new VideoJob, minting its VideoJobID through
// the supplied generator. It always produces status pending, FrameCount 0,
// an empty ErrorReason, and an unset StorageKey.
func NewVideoJob(generator VideoJobIDGenerator, userID UserID, filename OriginalFilename, sourceKey StorageKey, contentHash string, createdAt time.Time) (*VideoJob, error) {
	if generator == nil {
		return nil, ErrVideoJobIDGeneratorRequired
	}
	return RestoreVideoJob(generator.NewVideoJobID(), userID, filename, sourceKey, contentHash, StorageKey{}, 0, "", JobStatusPending, createdAt, 0)
}

// RestoreVideoJob reconstructs a VideoJob from already-known, already-validated values, e.g. from storage.
// sourceKey is deliberately NOT paired with status here, unlike storageKey
// and errorReason. The source_key column ships with an empty default, and a
// row can legitimately already be sitting in queued or processing — POST
// /upload drives the whole sequence inside one request, so a crash or a
// client disconnect strands one. Pairing the field at reconstitution would
// make those rows unloadable, turning FindByID into a domain error at deploy
// time with no obvious cause. The invariant lives on Enqueue instead.
// contentHash is unpaired for the same reason: its column also ships with an
// empty default, so every row written before it existed must stay loadable.
// leaseEpoch is int64 rather than int so that transposing it with frameCount
// — the only other numeric parameter — is a compile error.
func RestoreVideoJob(id VideoJobID, userID UserID, filename OriginalFilename, sourceKey StorageKey, contentHash string, storageKey StorageKey, frameCount int, errorReason string, status JobStatus, createdAt time.Time, leaseEpoch int64) (*VideoJob, error) {
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
	if (errorReason == "") == (status == JobStatusFailed) {
		return nil, ErrErrorReasonRequiresFailedStatus
	}
	if storageKey.IsZero() == (status == JobStatusCompleted) {
		return nil, ErrStorageKeyRequiresCompletedStatus
	}

	return &VideoJob{
		id:               id,
		userID:           userID,
		originalFilename: filename,
		sourceKey:        sourceKey,
		contentHash:      contentHash,
		storageKey:       storageKey,
		frameCount:       frameCount,
		errorReason:      errorReason,
		status:           status,
		createdAt:        createdAt,
		leaseEpoch:       leaseEpoch,
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

// SourceKey returns the storage key of the uploaded video this job processes,
// unset for a job created without one.
func (j *VideoJob) SourceKey() StorageKey {
	return j.sourceKey
}

// ContentHash returns the hex-encoded SHA-256 of the uploaded bytes, empty
// for a job created without an upload.
func (j *VideoJob) ContentHash() string {
	return j.contentHash
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

// LeaseEpoch returns the job's current fence epoch — the number of times it
// has been requeued after abandonment. Zero for a job that has never been
// recovered.
func (j *VideoJob) LeaseEpoch() int64 {
	return j.leaseEpoch
}

// Enqueue transitions the job from pending to queued. A job with no source
// key is rejected: there is nothing for a worker to fetch, so queueing it
// would produce a dispatch that can only fail.
func (j *VideoJob) Enqueue() error {
	if j.sourceKey.IsZero() {
		return ErrSourceKeyRequiredToEnqueue
	}
	return j.transitionTo(JobStatusQueued)
}

// StartProcessing transitions the job from queued to processing.
func (j *VideoJob) StartProcessing() error {
	return j.transitionTo(JobStatusProcessing)
}

// Requeue transitions an abandoned job from processing back to queued so it
// can be dispatched again, advancing the lease epoch by one. That advance is
// the fence: it is what makes the previous holder's terminal write refuse to
// apply, and it mirrors the lease_epoch = lease_epoch + 1 the conditional
// statement applies, so a caller has the advanced value without a second read.
//
// A job with no source key is rejected with the same sentinel Enqueue uses:
// a re-dispatch naming no stored object is a message no worker can act on.
//
// It deliberately does not route through Enqueue. The two edges differ in
// origin status and in who may walk them — a submitter queues a pending job,
// only the recovery sweep requeues a processing one.
//
// The origin status is checked here rather than left to the transition
// table, which cannot express it: the table's queued row is reachable from
// both pending and processing, so a table-only Requeue would accept a
// pending job and advance its epoch for an abandonment that never happened.
func (j *VideoJob) Requeue() error {
	if j.status != JobStatusProcessing {
		return ErrInvalidStatusTransition
	}
	if j.sourceKey.IsZero() {
		return ErrSourceKeyRequiredToEnqueue
	}
	if err := j.transitionTo(JobStatusQueued); err != nil {
		return err
	}
	j.leaseEpoch++
	return nil
}

// Complete transitions the job from processing to completed, recording its
// result storage key and extracted frame count.
func (j *VideoJob) Complete(storageKey StorageKey, frameCount int) error {
	if storageKey.IsZero() {
		return ErrStorageKeyRequiresCompletedStatus
	}
	if frameCount < 0 {
		return ErrFrameCountNegative
	}
	if err := j.transitionTo(JobStatusCompleted); err != nil {
		return err
	}
	j.storageKey = storageKey
	j.frameCount = frameCount
	return nil
}

// Fail transitions the job from processing to failed, recording a non-empty
// failure reason.
func (j *VideoJob) Fail(reason string) error {
	if reason == "" {
		return ErrFailureReasonRequired
	}
	if err := j.transitionTo(JobStatusFailed); err != nil {
		return err
	}
	j.errorReason = reason
	return nil
}

// transitionTo moves the job to next if the state machine allows it from
// the job's current status, leaving the job unchanged otherwise.
func (j *VideoJob) transitionTo(next JobStatus) error {
	if !j.status.CanTransitionTo(next) {
		return ErrInvalidStatusTransition
	}
	j.status = next
	return nil
}
