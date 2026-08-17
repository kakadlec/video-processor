package application

import (
	"context"

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
}

// ProcessVideoJob runs a VideoJob's enqueue/start-processing/extract
// sequence synchronously, in-process, failing the job if extraction errors.
// It deliberately does NOT call CompleteJob itself on success: a caller
// that has further work to do before the job can be considered truly done
// (e.g. cmd/api/video.go recording output artifact ownership) needs the job
// to still be in "processing" — the only status FailJob can validly move it
// out of — so it can fail the job if that further work fails, instead of
// being stuck with an already-"completed" job pointing at an artifact that
// never became reachable. The caller calls CompleteJob itself once it's
// sure the result is durably usable.
type ProcessVideoJob struct {
	enqueue   *EnqueueVideoJob
	start     *StartProcessing
	fail      *FailJob
	extractor domain.FrameExtractor
	idsFor    domain.VideoJobIDParser
}

// NewProcessVideoJob wires the ProcessVideoJob use case to its dependencies.
func NewProcessVideoJob(enqueue *EnqueueVideoJob, start *StartProcessing, fail *FailJob, extractor domain.FrameExtractor, idsFor domain.VideoJobIDParser) *ProcessVideoJob {
	return &ProcessVideoJob{enqueue: enqueue, start: start, fail: fail, extractor: extractor, idsFor: idsFor}
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

	storageKey, frameCount, imageNames, extractErr := uc.extractor.ExtractFrames(ctx, id, videoPath)
	if extractErr != nil {
		reason := extractErr.Error()
		if reason == "" {
			reason = fallbackFailureReason
		}
		if _, err := uc.fail.Execute(ctx, FailJobInput{JobID: jobID, Reason: reason}); err != nil {
			return ProcessVideoJobResult{}, err
		}
		return ProcessVideoJobResult{JobID: jobID, Success: false, FailureReason: reason}, nil
	}

	return ProcessVideoJobResult{
		JobID:      jobID,
		Success:    true,
		FrameCount: frameCount,
		ImageNames: imageNames,
		StorageKey: storageKey.String(),
	}, nil
}
