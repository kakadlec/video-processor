package main

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"video-processor/internal/platform/metrics"
	videodomain "video-processor/internal/video/domain"
	videomessaging "video-processor/internal/video/infrastructure/messaging"
	videopostgres "video-processor/internal/video/infrastructure/postgres"
)

// familySample reads one label combination's value out of the process
// registry, by family name, the same way
// internal/video/infrastructure/ffmpeg's own metrics_test.go and the three
// HTTP services' metrics_test.go files read a recorded family -- through the
// client library's own parsed type, never named directly, so no new import
// is needed to reach it. Like every other test in this package that reads
// process-global registry state, callers of this helper are not
// parallel-safe.
//
// A counter's value is its GetCounter().GetValue(); a histogram's is its
// GetHistogram().GetSampleCount(), summed across every matching series --
// there is at most one label combination for the unlabeled
// recoverySweepRequeueEpoch, so summing is a no-op there and exact for a
// labeled family like dispatchOutcomes.
func familySample(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()

	families, err := metrics.Gatherer().Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	var total float64
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, sample := range family.GetMetric() {
			got := make(map[string]string, len(labels))
			for _, pair := range sample.GetLabel() {
				got[pair.GetName()] = pair.GetValue()
			}
			match := true
			for wantName, wantValue := range labels {
				if got[wantName] != wantValue {
					match = false
					break
				}
			}
			if !match {
				continue
			}
			if h := sample.GetHistogram(); h != nil {
				total += float64(h.GetSampleCount())
				continue
			}
			total += sample.GetCounter().GetValue()
		}
	}
	return total
}

// TestHandle_RecordsDispatchOutcome pins one representative branch of
// handle()'s disposition table against dispatchOutcomes, per
// docs/roadmap.md's expose-worker-and-notifier-metrics row: a dead-lettered
// dispatch naming an unknown job is counted under ("job_not_found",
// "reject") and nowhere else.
func TestHandle_RecordsDispatchOutcome(t *testing.T) {
	env := newWorkerTestEnv(t, envOptions{})
	labels := map[string]string{"outcome": "job_not_found", "disposition": "reject"}
	before := familySample(t, "fiapx_worker_dispatch_outcomes_total", labels)

	body, err := json.Marshal(videomessaging.JobQueuedMessage{
		Type:        videopostgres.VideoJobQueuedEventType,
		JobID:       uuid.NewString(),
		UserID:      uuid.NewString(),
		SourceKey:   videodomain.SourceStorageKey(uuid.NewString(), "input.mp4").String(),
		ContentHash: testContentHash,
		OccurredAt:  time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("marshal dispatch: %v", err)
	}

	var inFlight atomic.Pointer[string]
	if got := env.deps.handle(context.Background(), body, &inFlight); got != videomessaging.Reject {
		t.Fatalf("disposition = %v, want Reject", got)
	}

	if got := familySample(t, "fiapx_worker_dispatch_outcomes_total", labels); got != before+1 {
		t.Fatalf(`fiapx_worker_dispatch_outcomes_total{outcome="job_not_found",disposition="reject"} = %v, want %v`, got, before+1)
	}
}

// TestSweep_RecordsRequeueActionAndEpoch pins the recovery sweep's two
// families against the same scenario
// TestSweep_RequeuesAnAbandonedJobAndDispatchesItAgain drives:
// docs/roadmap.md's "how often a lease was found missing and a job
// requeued, and how many requeues were spent of the three a job gets".
func TestSweep_RecordsRequeueActionAndEpoch(t *testing.T) {
	env := newWorkerTestEnv(t, envOptions{})
	job, _ := seedQueuedJob(t, env, generateTestVideo(t, 1))
	ctx := context.Background()

	claimSeededJob(t, env, job)
	dropLease(t, env, job)

	beforeAction := familySample(t, "fiapx_worker_recovery_sweep_actions_total", map[string]string{"action": "requeued"})
	beforeEpoch := familySample(t, "fiapx_worker_recovery_sweep_requeue_epoch", nil)

	sweepTwice(ctx, sweeperFor(t, env, job))

	if got := familySample(t, "fiapx_worker_recovery_sweep_actions_total", map[string]string{"action": "requeued"}); got != beforeAction+1 {
		t.Fatalf(`fiapx_worker_recovery_sweep_actions_total{action="requeued"} = %v, want %v`, got, beforeAction+1)
	}
	if got := familySample(t, "fiapx_worker_recovery_sweep_requeue_epoch", nil); got != beforeEpoch+1 {
		t.Fatalf("fiapx_worker_recovery_sweep_requeue_epoch sample count = %v, want %v", got, beforeEpoch+1)
	}
}
