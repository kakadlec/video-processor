package application

import (
	"context"

	"video-processor/internal/video/domain"
)

// fallbackFailureReason is used when FrameExtractor returns an error whose
// message is empty, since VideoJob.Fail rejects an empty reason.
const fallbackFailureReason = "video processing failed"

// ProcessVideoJobResult describes the outcome of running a VideoJob's full
// processing sequence.
type ProcessVideoJobResult struct {
	JobID         string
	Success       bool
	FrameCount    int
	ImageNames    []string
	StorageKey    string
	FailureReason string
}

// ProcessVideoJob runs a VideoJob's full processing sequence synchronously,
// in-process: enqueue, start processing, extract frames, then complete or
// fail depending on the extraction outcome. It composes the four standalone
// transition use cases rather than mutating the aggregate directly, so each
// step stays independently usable (e.g. by a future queue-driven worker).
type ProcessVideoJob struct {
	enqueue   *EnqueueVideoJob
	start     *StartProcessing
	complete  *CompleteJob
	fail      *FailJob
	extractor domain.FrameExtractor
	idsFor    domain.VideoJobIDParser
}

// NewProcessVideoJob wires the ProcessVideoJob use case to its dependencies.
func NewProcessVideoJob(enqueue *EnqueueVideoJob, start *StartProcessing, complete *CompleteJob, fail *FailJob, extractor domain.FrameExtractor, idsFor domain.VideoJobIDParser) *ProcessVideoJob {
	return &ProcessVideoJob{enqueue: enqueue, start: start, complete: complete, fail: fail, extractor: extractor, idsFor: idsFor}
}

// Execute runs jobID's full processing sequence against the video file at
// videoPath.
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

	if _, err := uc.complete.Execute(ctx, CompleteJobInput{
		JobID:      jobID,
		StorageKey: storageKey.String(),
		FrameCount: frameCount,
	}); err != nil {
		return ProcessVideoJobResult{}, err
	}

	return ProcessVideoJobResult{
		JobID:      jobID,
		Success:    true,
		FrameCount: frameCount,
		ImageNames: imageNames,
		StorageKey: storageKey.String(),
	}, nil
}
