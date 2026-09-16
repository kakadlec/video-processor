package main

import (
	"github.com/prometheus/client_golang/prometheus"

	"video-processor/internal/platform/metrics"
)

// dispatchOutcomes counts every dispatch handle() resolves to, by outcome
// and by the disposition it reports to the consumer -- the two together are
// how fix-worker-dependency-outage-disposition's decision table is measured
// rather than argued, per docs/roadmap.md's
// expose-worker-and-notifier-metrics row.
//
// outcome is the finer of the two labels: each value names exactly one
// return statement in handle(), so a rising dead-letter rate can be
// attributed to a cause without reading logs. disposition collapses that
// into the three values the consumer itself acts on (ack/reject/requeue),
// because that is the number an operator compares against queue depth and
// the dead-letter queue's own growth.
//
// Both labels are literals at every call site in handle() -- never routed
// through a shared helper taking either as a parameter, which
// internal/platform/metrics/disclosure_test.go's label rule would refuse:
// it judges the argument expression at the WithLabelValues call itself, not
// what a caller several frames up assigned it from.
var dispatchOutcomes = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "fiapx_worker_dispatch_outcomes_total",
		Help: "Dispatches handle() has resolved, by outcome and the disposition reported to the consumer.",
	},
	[]string{"outcome", "disposition"},
)

func init() {
	metrics.MustRegister(dispatchOutcomes)
}

// recoverySweepActions counts what the recovery sweep did with a job it
// confirmed unleased, and recoverySweepRequeueEpoch reports which of the
// domain.MaxJobRequeues chances a requeued job had just spent -- together,
// docs/roadmap.md's "how often a lease was found missing and a job
// requeued, and how many requeues were spent of the three a job gets".
var recoverySweepActions = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "fiapx_worker_recovery_sweep_actions_total",
		Help: "Jobs the recovery sweep confirmed unleased, by what it did about it.",
	},
	[]string{"action"},
)

// recoverySweepRequeueEpoch observes the lease_epoch a job carried right
// after the sweep requeued it -- 1, 2 or 3 at the domain.MaxJobRequeues
// default -- so a fleet approaching that bound is visible before it starts
// failing jobs outright instead of requeueing them. Unlabeled: the epoch is
// the value under observation, not a dimension to slice by.
var recoverySweepRequeueEpoch = prometheus.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "fiapx_worker_recovery_sweep_requeue_epoch",
		Help:    "The lease_epoch a job held right after the recovery sweep requeued it.",
		Buckets: []float64{1, 2, 3},
	},
)

func init() {
	metrics.MustRegister(recoverySweepActions, recoverySweepRequeueEpoch)
}
