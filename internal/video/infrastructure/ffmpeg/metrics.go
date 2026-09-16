package ffmpeg

import (
	"github.com/prometheus/client_golang/prometheus"

	"video-processor/internal/platform/metrics"
)

// extractionDurationBuckets is sized for real 1fps frame extraction rather
// than an HTTP request's: a job over several minutes of footage can
// legitimately spend minutes here, and cmd/*/metrics.go's 60s-ceiling
// request-duration buckets would put every real job in one overflow bucket.
var extractionDurationBuckets = []float64{1, 5, 10, 30, 60, 120, 300, 600, 1800}

// extractionDuration is the only metric this package records, and this is
// the site docs/roadmap.md's expose-worker-and-notifier-metrics row names:
// no HTTP service calls ExtractFrames (see this file's neighbour's own doc
// comment), so every sample this family ever receives in production comes
// from cmd/worker.
//
// Labeled by outcome so a slow failure -- ffmpeg hanging on bad input,
// rather than failing fast -- is visible in the same family as a slow
// success, rather than only in cmd/worker/metrics.go's disposition counter.
var extractionDuration = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "fiapx_video_extraction_duration_seconds",
		Help:    "Time spent running ffmpeg and packaging its output, by outcome.",
		Buckets: extractionDurationBuckets,
	},
	[]string{"outcome"},
)

func init() {
	metrics.MustRegister(extractionDuration)
}
