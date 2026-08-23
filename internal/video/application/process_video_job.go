package application

import (
	"context"
	"log"
	"os"

	"video-processor/internal/video/domain"
)

// fallbackFailureReason is used when FrameExtractor returns an error whose
// message is empty, since VideoJob.Fail rejects an empty reason.
const fallbackFailureReason = "video processing failed"

// ProcessVideoJobResult describes the outcome of running a VideoJob's
// enqueue/start/extract sequence. On success the job is left in
// "processing" status, not "completed" — see ProcessVideoJob's doc comment
// for why the caller, not this use case, is responsible for calling
// CompleteJob.
type ProcessVideoJobResult struct {
	JobID         string
	Success       bool
	FrameCount    int
	ImageNames    []string
	StorageKey    string
	FailureReason string
	// ExtractionError is the error that caused Success to be false —
	// from domain.FrameExtractor or from storing the result — unwrapped so
	// a caller can classify it (e.g. via errors.Is against a specific
	// infrastructure adapter's sentinel errors) to choose its own
	// user-facing message, instead of exposing FailureReason's raw text
	// directly. Nil when Success is true.
	ExtractionError error
}

// ProcessVideoJob runs a VideoJob's enqueue/start-processing/extract/store
// sequence synchronously, in-process, failing the job if extraction or
// storage errors.
//
// It does NOT call CompleteJob itself on success. That split no longer
// exists for the reason it originally did — the caller used to record
// output artifact ownership after this use case returned, and needed the
// job left in "processing" so it could still FailJob if that recording
// failed. Storing the result is this use case's own step now, so a
// successful return already means the result is durable and the caller has
// no fallible work left. The split survives only because collapsing it
// would ripple through every caller and test of this use case, which is a
// separate refactor. Do not reintroduce a post-processing failure branch in
// a caller to justify it.
type ProcessVideoJob struct {
	enqueue   *EnqueueVideoJob
	start     *StartProcessing
	fail      *FailJob
	extractor domain.FrameExtractor
	results   domain.ResultStorage
	idsFor    domain.VideoJobIDParser
}

// NewProcessVideoJob wires the ProcessVideoJob use case to its dependencies.
func NewProcessVideoJob(enqueue *EnqueueVideoJob, start *StartProcessing, fail *FailJob, extractor domain.FrameExtractor, results domain.ResultStorage, idsFor domain.VideoJobIDParser) *ProcessVideoJob {
	return &ProcessVideoJob{enqueue: enqueue, start: start, fail: fail, extractor: extractor, results: results, idsFor: idsFor}
}

// Execute runs jobID's enqueue/start-processing/extract sequence against
// the video file at videoPath.
func (uc *ProcessVideoJob) Execute(ctx context.Context, jobID string, videoPath string) (ProcessVideoJobResult, error) {
	id, err := uc.idsFor.ParseVideoJobID(jobID)
	if err != nil {
		return ProcessVideoJobResult{}, err
	}

	if _, err := uc.enqueue.Execute(ctx, jobID); err != nil {
		return ProcessVideoJobResult{}, err
	}
	if _, err := uc.start.Execute(ctx, jobID); err != nil {
		return ProcessVideoJobResult{}, err
	}

	zipPath, frameCount, imageNames, extractErr := uc.extractor.ExtractFrames(ctx, id, videoPath)
	if extractErr != nil {
		return uc.failWith(jobID, extractErr)
	}
	// Registered before the store attempt, not after it: the Put-error path
	// below returns, so a defer set up afterwards would never run and the
	// extracted zip would be left behind on every storage failure.
	defer func() {
		if err := os.Remove(zipPath); err != nil && !os.IsNotExist(err) {
			log.Printf("remove extracted zip %s for job %s: %v", zipPath, jobID, err)
		}
	}()

	storageKey := domain.ResultStorageKey(id)
	if err := uc.results.Put(ctx, storageKey, zipPath); err != nil {
		return uc.failWith(jobID, err)
	}

	return ProcessVideoJobResult{
		JobID:      jobID,
		Success:    true,
		FrameCount: frameCount,
		ImageNames: imageNames,
		StorageKey: storageKey.String(),
	}, nil
}

// failWith marks jobID failed for cause and reports it as an unsuccessful
// result. It is shared by the extraction and storage failure paths: a
// result that could not be stored is no more usable than one that could not
// be extracted.
func (uc *ProcessVideoJob) failWith(jobID string, cause error) (ProcessVideoJobResult, error) {
	reason := cause.Error()
	if reason == "" {
		reason = fallbackFailureReason
	}
	// A detached context, not the request's: cause may itself be the result
	// of that context being canceled (exec.CommandContext killing ffmpeg, or
	// an aborted upload), and this write must still succeed so the job
	// reaches "failed" rather than being stuck wherever it was.
	finalizeCtx, cancel := NewFinalizationContext()
	defer cancel()
	if _, err := uc.fail.Execute(finalizeCtx, FailJobInput{JobID: jobID, Reason: reason}); err != nil {
		return ProcessVideoJobResult{}, err
	}
	return ProcessVideoJobResult{JobID: jobID, Success: false, FailureReason: reason, ExtractionError: cause}, nil
}
