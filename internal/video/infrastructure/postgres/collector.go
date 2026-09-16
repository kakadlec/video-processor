package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// collectTimeout bounds each collection, and it is strictly below any
// plausible scrape timeout — the same relationship the readiness checks state
// against their prober, and wrong in the same silent direction. With the
// scraper's timeout the larger, a slow database produces a failed family the
// scraper records; with this bound the larger, the scraper abandons every
// request and the endpoint appears wholly unavailable for a reason that has
// nothing to do with the endpoint.
const collectTimeout = 2 * time.Second

// PipelineCollector reports how much work is in flight and how much is
// committed and unannounced, by querying at scrape time.
//
// It is built on the undecorated repository, and that is a compile-time
// property rather than a convention: the cache decorator does not carry the
// aggregate methods, so it cannot be passed here. A cached count would be a
// count from a different moment than the one the scraper asked about, which
// is the failure the object-storage reachability check already refuses and
// which is least visible in a level.
type PipelineCollector struct {
	repo *Repository
}

// NewPipelineCollector builds the collector over the pool the exposing
// process already holds.
func NewPipelineCollector(repo *Repository) *PipelineCollector {
	return &PipelineCollector{repo: repo}
}

var (
	jobsInStateDesc = prometheus.NewDesc(
		"fiapx_video_jobs_in_state",
		"Jobs currently in each in-flight state, saturating at 10000.",
		[]string{"state"},
		nil,
	)
	oldestJobInStateDesc = prometheus.NewDesc(
		"fiapx_video_jobs_oldest_in_state_age_seconds",
		"Age of the oldest job in each in-flight state. Absent while the state is empty.",
		[]string{"state"},
		nil,
	)
	unpublishedOutboxDesc = prometheus.NewDesc(
		"fiapx_video_job_outbox_unpublished",
		"Committed but unpublished outbox events per relayed event type, saturating at 10000.",
		[]string{"event_type"},
		nil,
	)
	oldestUnpublishedOutboxDesc = prometheus.NewDesc(
		"fiapx_video_job_outbox_oldest_unpublished_age_seconds",
		"Age of the oldest unpublished outbox event per relayed event type. Absent while there is none.",
		[]string{"event_type"},
		nil,
	)
)

// Describe reports the four families this collector produces.
func (c *PipelineCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- jobsInStateDesc
	ch <- oldestJobInStateDesc
	ch <- unpublishedOutboxDesc
	ch <- oldestUnpublishedOutboxDesc
}

// Collect queries on scrape. It reports no cached, background-refreshed or
// last-known value, and on a failure it emits no sample for the affected
// families and reports the failure to the scraper, so the scrape itself is
// seen to have failed.
//
// It does not emit zero on failure, and that is the whole decision: a queued
// count reading zero means nothing is waiting, which is the single most
// reassuring statement this endpoint can make, and emitting it because a
// query timed out would turn a database outage into a green dashboard.
//
// The converse holds with equal force and is why the aggregates return an
// entry per member of their closed sets: absence is how failure is reported,
// so emptiness must not also be absence. A successful collection of an empty
// state is a count of zero and no age at all, and that pair is the signature
// of idle rather than a gap.
func (c *PipelineCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
	defer cancel()

	c.collectInFlight(ctx, ch)
	c.collectOutbox(ctx, ch)
}

func (c *PipelineCollector) collectInFlight(ctx context.Context, ch chan<- prometheus.Metric) {
	entries, err := c.repo.InFlightJobAggregate(ctx)
	if err == nil {
		err = requireKeys(entries, InFlightStates())
	}
	if err != nil {
		ch <- prometheus.NewInvalidMetric(jobsInStateDesc, err)
		ch <- prometheus.NewInvalidMetric(oldestJobInStateDesc, err)
		return
	}
	byKey := index(entries)

	// One block per member of the closed set, each passing its own label as a
	// literal. Rendering the key into the label instead would be exactly the
	// non-literal value the label rule refuses, and writing the set out here
	// is what makes it closed in the source rather than in a comment.
	queued := byKey[InFlightStateQueued]
	ch <- prometheus.MustNewConstMetric(jobsInStateDesc, prometheus.GaugeValue, float64(queued.Count), "queued")
	if queued.OldestAgeValid {
		ch <- prometheus.MustNewConstMetric(oldestJobInStateDesc, prometheus.GaugeValue, queued.OldestAge, "queued")
	}

	processing := byKey[InFlightStateProcessing]
	ch <- prometheus.MustNewConstMetric(jobsInStateDesc, prometheus.GaugeValue, float64(processing.Count), "processing")
	if processing.OldestAgeValid {
		ch <- prometheus.MustNewConstMetric(oldestJobInStateDesc, prometheus.GaugeValue, processing.OldestAge, "processing")
	}
}

func (c *PipelineCollector) collectOutbox(ctx context.Context, ch chan<- prometheus.Metric) {
	entries, err := c.repo.UnpublishedOutboxAggregate(ctx)
	if err == nil {
		err = requireKeys(entries, RelayedEventTypes())
	}
	if err != nil {
		ch <- prometheus.NewInvalidMetric(unpublishedOutboxDesc, err)
		ch <- prometheus.NewInvalidMetric(oldestUnpublishedOutboxDesc, err)
		return
	}
	byKey := index(entries)

	queued := byKey[videoJobQueuedEventType]
	ch <- prometheus.MustNewConstMetric(unpublishedOutboxDesc, prometheus.GaugeValue, float64(queued.Count), "video_job.queued.v2")
	if queued.OldestAgeValid {
		ch <- prometheus.MustNewConstMetric(oldestUnpublishedOutboxDesc, prometheus.GaugeValue, queued.OldestAge, "video_job.queued.v2")
	}

	completed := byKey[videoJobCompletedEventType]
	ch <- prometheus.MustNewConstMetric(unpublishedOutboxDesc, prometheus.GaugeValue, float64(completed.Count), "video_job.completed.v1")
	if completed.OldestAgeValid {
		ch <- prometheus.MustNewConstMetric(oldestUnpublishedOutboxDesc, prometheus.GaugeValue, completed.OldestAge, "video_job.completed.v1")
	}

	failed := byKey[videoJobFailedEventType]
	ch <- prometheus.MustNewConstMetric(unpublishedOutboxDesc, prometheus.GaugeValue, float64(failed.Count), "video_job.failed.v1")
	if failed.OldestAgeValid {
		ch <- prometheus.MustNewConstMetric(oldestUnpublishedOutboxDesc, prometheus.GaugeValue, failed.OldestAge, "video_job.failed.v1")
	}
}

func index(entries []Aggregate) map[string]Aggregate {
	byKey := make(map[string]Aggregate, len(entries))
	for _, entry := range entries {
		byKey[entry.Key] = entry
	}
	return byKey
}

// requireKeys reconciles what the statement returned with the closed set the
// emission writes out. A key the statement did not return would otherwise be
// reported as a zero — the one value that must never be invented — so the
// disagreement is reported to the scraper as a failed collection instead.
func requireKeys(entries []Aggregate, keys []string) error {
	byKey := index(entries)
	for _, key := range keys {
		if _, ok := byKey[key]; !ok {
			return fmt.Errorf("video: the aggregate returned no entry for one of its closed set's members")
		}
	}
	return nil
}
