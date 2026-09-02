package application

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"video-processor/internal/video/domain"
)

// fallbackFailureReason is used when FrameExtractor returns an error whose
// message is empty, since VideoJob.Fail rejects an empty reason.
const fallbackFailureReason = "video processing failed"

// storeFailureReason and fetchFailureReason are recorded instead of the
// storage adapter's own error text, which names the object-storage endpoint
// and bucket. The reason is persisted on the job and echoed to the uploader,
// so it must carry no infrastructure detail; the underlying error is logged
// instead.
const (
	storeFailureReason = "failed to store the extracted result"
	fetchFailureReason = "failed to retrieve the uploaded video"
	// abandonedFailureReason is what the recovery sweep records for a job it
	// has given up on: one that has exhausted its requeues, or that carries
	// no source key to re-dispatch. Like the two above it is persisted and
	// echoed to the uploader, so it names no broker, store, or endpoint.
	abandonedFailureReason = "video processing was interrupted and could not be recovered"
)

// leaseRenewInterval is how often a running extraction renews its job lease.
// It must stay comfortably below the adapter's lease TTL
// (internal/video/infrastructure/lease.TTL, 90s): the margin is what absorbs
// a slow Redis round trip or a scheduling delay without a live job appearing
// abandoned. A constant rather than configuration, for the same reason the
// TTL is one — the pair is a correctness margin, not a deployment preference.
//
// The constant is duplicated in spirit rather than imported: the application
// layer does not depend on an infrastructure adapter.
const leaseRenewInterval = 30 * time.Second

// tempDirName is where the source video is downloaded for ffmpeg, matching
// the directory the extractor writes its own frames and zip into.
const tempDirName = "temp"

// localSourceSuffix names the transient copy downloaded for ffmpeg. It sits
// beside the extractor's per-job frame directory rather than inside it, so
// that directory's own RemoveAll cannot delete a file it does not own.
const localSourceSuffix = "_source"

// ProcessVideoJobResult describes the outcome of running a VideoJob's
// start/fetch/extract/store sequence. On success the job is left in
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
	// LeaseEpoch is the fence epoch the claim won and this run held
	// throughout. The caller carries it into its own terminal write.
	LeaseEpoch int64
	// Applied reports whether the failure write this use case made is the
	// one that landed, as opposed to matching a terminal row another actor
	// had already committed. It is meaningful only when Success is false —
	// the caller's cleanup of the source object and the idempotency key is
	// one-shot and rides on it. A successful run makes no terminal write at
	// all; its caller reads CompleteJob's own outcome instead.
	Applied bool
	// ExtractionError is the error that caused Success to be false —
	// from domain.FrameExtractor, from retrieving the source, or from
	// storing the result — unwrapped so a caller can classify it (e.g. via
	// errors.Is against a specific infrastructure adapter's sentinel
	// errors) to choose its own user-facing message, instead of exposing
	// FailureReason's raw text directly. Nil when Success is true.
	ExtractionError error
}

// ProcessVideoJob runs a VideoJob's start-processing/fetch/extract/store
// sequence synchronously, in-process, failing the job if any of those steps
// errors.
//
// It does not enqueue. The pending -> queued transition is the caller's, and
// it moved there because that transition now writes an outbox row the relay
// publishes from: a use case that both queued a job and immediately
// processed it would announce a dispatch for work it had already done.
// Execute therefore expects a job already in queued, and errors on one still
// in pending.
//
// A lost claim is propagated unchanged. When StartProcessing reports
// domain.ErrJobClaimLost — another consumer owns this job, or it is already
// terminal — Execute returns that sentinel and nothing else happens: no
// source is downloaded, no ffmpeg runs, and FailJob is emphatically not
// called. Failing the job here would let a duplicate delivery overwrite the
// winner's outcome, which is the exact damage the claim exists to prevent.
// The caller's only correct response is to drop the dispatch.
//
// Execute takes a source StorageKey rather than a local file path, and that
// is the point of the signature: a path written by the calling HTTP handler
// is only meaningful to a process that shares that handler's filesystem, and
// Phase 6 runs this sequence in cmd/worker, which does not. Do not
// reintroduce a local-path parameter, and do not move the download into the
// ffmpeg adapter — that adapter takes a local path and knows nothing about
// object storage, the same split this project established for the result
// zip.
//
// This use case owns the lifetime of the local copy it downloads. It does
// NOT delete the source object: the caller stored it and reaches exit paths
// this use case never runs on (a duplicate-content conflict, a
// CreateVideoJob failure), so cleanup belongs there, in one place.
//
// It does NOT call CompleteJob itself on success. That split no longer
// exists for the reason it originally did — the caller used to record
// output artifact ownership after this use case returned, and needed the
// job left in "processing" so it could still FailJob if that recording
// failed. Storing the result is this use case's own step now, so a
// successful return already means the artifact is durable, and the caller
// has no fallible step left *before* CompleteJob. The split survives only
// because collapsing it would ripple through every caller and test of this
// use case, which is a separate refactor. Do not reintroduce a
// post-processing failure branch in a caller to justify it.
//
// CompleteJob itself can still fail, leaving a stored object whose job is
// stuck "processing" and which no listing shows. That orphan class is not
// new: the pre-MinIO pipeline produced exactly the same shape when the zip
// was written and the subsequent ownership recording failed. Reconciling it
// needs a recoverable workflow with a worker that can resume or compensate,
// which is Phase 6's queue work, not something a synchronous in-request
// pipeline can do correctly.
type ProcessVideoJob struct {
	start     *StartProcessing
	fail      *FailJob
	extractor domain.FrameExtractor
	sources   domain.SourceStorage
	results   domain.ResultStorage
	leases    domain.JobLeaseStore
	idsFor    domain.VideoJobIDParser
	newTicker LeaseTickerFunc
}

// LeaseTicker is the seam the lease heartbeat ticks on. It exists so a test
// can drive renewal without waiting out a renewal period in wall-clock time;
// the default is time.Ticker.
type LeaseTicker interface {
	Ticks() <-chan time.Time
	Stop()
}

// LeaseTickerFunc builds a LeaseTicker for a period.
type LeaseTickerFunc func(time.Duration) LeaseTicker

// ProcessVideoJobOption customizes the use case at construction.
type ProcessVideoJobOption func(*ProcessVideoJob)

// WithLeaseTicker replaces the heartbeat's ticker source.
func WithLeaseTicker(newTicker LeaseTickerFunc) ProcessVideoJobOption {
	return func(uc *ProcessVideoJob) { uc.newTicker = newTicker }
}

type realLeaseTicker struct{ ticker *time.Ticker }

func (t realLeaseTicker) Ticks() <-chan time.Time { return t.ticker.C }
func (t realLeaseTicker) Stop()                   { t.ticker.Stop() }

func newRealLeaseTicker(d time.Duration) LeaseTicker {
	return realLeaseTicker{ticker: time.NewTicker(d)}
}

// NewProcessVideoJob wires the ProcessVideoJob use case to its dependencies.
func NewProcessVideoJob(start *StartProcessing, fail *FailJob, extractor domain.FrameExtractor, sources domain.SourceStorage, results domain.ResultStorage, leases domain.JobLeaseStore, idsFor domain.VideoJobIDParser, opts ...ProcessVideoJobOption) *ProcessVideoJob {
	uc := &ProcessVideoJob{
		start:     start,
		fail:      fail,
		extractor: extractor,
		sources:   sources,
		results:   results,
		leases:    leases,
		idsFor:    idsFor,
		newTicker: newRealLeaseTicker,
	}
	for _, opt := range opts {
		opt(uc)
	}
	return uc
}

// holdLease takes the job's lease at the epoch the claim won and keeps
// renewing it until the returned stop function is called, which also joins
// the renewing goroutine.
//
// Acquisition and renewal failures are logged and the run continues. The job
// is already protected by the unconditional claim; being invisible to the
// recovery sweep costs at most one duplicated extraction, which the fence
// resolves. That is the fail-open half of the posture — deciding a lease has
// lapsed is the half that fails closed, and it lives in the sweep.
//
// A renewal that finds a different epoch stops renewing rather than
// retrying: this run has been taken over, its terminal write is going to be
// fenced anyway, and continuing would extend the successor's lease.
//
// It does not release the lease. Release happens after the outcome is
// committed, which is the caller's moment, not this one's.
func (uc *ProcessVideoJob) holdLease(ctx context.Context, id domain.VideoJobID, epoch int64) func() {
	if acquired, err := uc.leases.Acquire(ctx, id, epoch); err != nil {
		log.Printf("acquire lease for job %s at epoch %d: %v", id.String(), epoch, err)
	} else if !acquired {
		log.Printf("lease for job %s already held at a newer epoch than %d", id.String(), epoch)
	}

	renewCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := uc.newTicker(leaseRenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-ticker.Ticks():
				renewed, err := uc.leases.Renew(renewCtx, id, epoch)
				if err != nil {
					log.Printf("renew lease for job %s at epoch %d: %v", id.String(), epoch, err)
					continue
				}
				if !renewed {
					log.Printf("lease for job %s no longer held at epoch %d, stopping renewal", id.String(), epoch)
					return
				}
			}
		}
	}()

	return func() {
		cancel()
		<-done
	}
}

// Execute runs jobID's start-processing/fetch/extract/store sequence against
// the source video stored under sourceKey.
func (uc *ProcessVideoJob) Execute(ctx context.Context, jobID string, sourceKey domain.StorageKey) (ProcessVideoJobResult, error) {
	id, err := uc.idsFor.ParseVideoJobID(jobID)
	if err != nil {
		return ProcessVideoJobResult{}, err
	}

	claim, err := uc.start.Execute(ctx, jobID)
	if err != nil {
		return ProcessVideoJobResult{}, err
	}
	epoch := claim.LeaseEpoch

	// The lease starts the moment the claim is won and stops before this
	// function returns, so no goroutine outlives the run it belongs to.
	stopLease := uc.holdLease(ctx, id, epoch)
	defer stopLease()

	videoPath, err := localSourcePath(id)
	if err != nil {
		log.Printf("build local source path for job %s: %v", jobID, err)
		return uc.failWith(jobID, epoch, err, fetchFailureReason)
	}
	if err := uc.sources.Get(ctx, sourceKey, videoPath); err != nil {
		log.Printf("fetch source %s for job %s: %v", sourceKey.String(), jobID, err)
		return uc.failWith(jobID, epoch, err, fetchFailureReason)
	}
	// Registered before extraction, not after: the extraction-error path
	// below returns, so a defer set up afterwards would never run and the
	// downloaded video would be left behind on every failure.
	defer func() {
		if err := os.Remove(videoPath); err != nil && !os.IsNotExist(err) {
			log.Printf("remove local source %s for job %s: %v", videoPath, jobID, err)
		}
	}()

	zipPath, frameCount, imageNames, extractErr := uc.extractor.ExtractFrames(ctx, id, videoPath)
	if extractErr != nil {
		return uc.failWith(jobID, epoch, extractErr, extractErr.Error())
	}
	// Same ordering rule as the source copy above, for the same reason: the
	// Put-error path returns.
	defer func() {
		if err := os.Remove(zipPath); err != nil && !os.IsNotExist(err) {
			log.Printf("remove extracted zip %s for job %s: %v", zipPath, jobID, err)
		}
	}()

	storageKey := domain.ResultStorageKey(id)
	if err := uc.results.Put(ctx, storageKey, zipPath); err != nil {
		log.Printf("store result %s for job %s: %v", storageKey.String(), jobID, err)
		return uc.failWith(jobID, epoch, err, storeFailureReason)
	}

	return ProcessVideoJobResult{
		JobID:      jobID,
		Success:    true,
		FrameCount: frameCount,
		ImageNames: imageNames,
		StorageKey: storageKey.String(),
		LeaseEpoch: epoch,
	}, nil
}

// localSourcePath names the transient copy downloaded for ffmpeg.
//
// The name is built entirely from the job's own identifier — no part of it
// derives from the uploaded filename — and carries no extension. ffmpeg
// detects input format by probing content, and every container this system
// accepts has an unambiguous signature, so an extension buys nothing and
// would cost a sanitization obligation. The confinement check mirrors the
// extractor's own; it is unreachable for a parsed VideoJobID and exists so
// the path handed to os.Remove and to ffmpeg is validated rather than
// assumed.
func localSourcePath(jobID domain.VideoJobID) (string, error) {
	path := filepath.Clean(filepath.Join(tempDirName, jobID.String()+localSourceSuffix))
	if !strings.HasPrefix(path, tempDirName+string(os.PathSeparator)) {
		return "", fmt.Errorf("video: local source path %q escapes %s/", path, tempDirName)
	}
	return path, nil
}

// failWith marks jobID failed with reason and reports it as an unsuccessful
// result. It is shared by the fetch, extraction, and storage failure paths:
// a result that could not be produced is no more usable than one that could
// not be stored. reason is passed separately from cause because the paths
// differ in what may be persisted — extraction echoes ffmpeg's own message,
// as it always has, while the storage paths must not leak endpoint or
// bucket.
//
// The write carries the caller's epoch and can be refused by the fence. That
// refusal is returned as domain.ErrJobFenced and nothing else is attempted:
// not a retry without the fence, not a reload, and emphatically not a report
// of a failed job — this run no longer owns the job, and the actor that does
// will record its own outcome. The deferred cleanup of the local copy still
// runs, because it is registered above this call.
func (uc *ProcessVideoJob) failWith(jobID string, epoch int64, cause error, reason string) (ProcessVideoJobResult, error) {
	if reason == "" {
		reason = fallbackFailureReason
	}
	// A detached context, not the request's: cause may itself be the result
	// of that context being canceled (exec.CommandContext killing ffmpeg, or
	// an aborted upload), and this write must still succeed so the job
	// reaches "failed" rather than being stuck wherever it was.
	finalizeCtx, cancel := NewFinalizationContext()
	defer cancel()
	failed, err := uc.fail.Execute(finalizeCtx, FailJobInput{JobID: jobID, Reason: reason, LeaseEpoch: epoch})
	if err != nil {
		return ProcessVideoJobResult{}, err
	}
	return ProcessVideoJobResult{
		JobID:           jobID,
		Success:         false,
		FailureReason:   reason,
		ExtractionError: cause,
		LeaseEpoch:      epoch,
		Applied:         failed.Applied,
	}, nil
}
