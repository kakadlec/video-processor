package cache

import (
	"github.com/prometheus/client_golang/prometheus"

	"video-processor/internal/platform/metrics"
)

// The status cache's lookup counter, declared here because this is the
// package that records it.
//
// The hit ratio is the only evidence this decorator earns its complexity, and
// the error rate reports the health of a dependency the readiness probes
// deliberately refuse to consult: every feature over Redis fails open, so an
// unreachable Redis makes this system slower rather than wrong, and a
// readiness check on it would take the service out of rotation for a
// degradation it is designed to survive.
//
// This package is linked by cmd/worker as well as by cmd/video-api, so the
// counter is incremented in a process that exposes no endpoint and serves it
// to nobody. That is accepted rather than worked around: the cost is an
// atomic increment on a path that already makes a Redis round trip, and the
// benefit is that exposing it later is the addition of a handler rather than
// a re-instrumentation.
var statusCacheLookups = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "fiapx_job_status_cache_lookups_total",
		Help: "Job status cache lookups, by outcome.",
	},
	[]string{"outcome"},
)

func init() {
	metrics.MustRegister(statusCacheLookups)
}
