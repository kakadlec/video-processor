package main

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"

	"video-processor/internal/platform/metrics"
)

// metricsRoutePath is the exposition endpoint's template, a constant for the
// same reason the probe templates are: more than one place names it and they
// have to agree with the router about it.
const metricsRoutePath = "/metrics"

// requestDurationBuckets is identical in all three services, deliberately,
// and reaches 60s rather than taking the client library's default ceiling of
// 10s.
//
// Identical because a histogram is only aggregable across scrape targets when
// every target shares a bucket set: a service with buckets of its own
// produces a family that cannot be summed with the others, and the three
// services sit behind one gateway and answer for one system. So the set is a
// property of the family rather than of whichever routes this binary happens
// to serve.
//
// 60s because the set has to hold the longest legitimate duration anywhere in
// that family, and that is POST /upload on the Video API: it streams the
// request body into the bucket before it answers, so its handler duration
// includes the client's own transfer — a property of the route rather than a
// defect. A set ending at 10s would put every real upload in the overflow
// bucket and report nothing while looking healthy. The services that serve no
// such route pay only for empty buckets.
var requestDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20, 30, 60}

// The two per-request families, declared here because this is the process
// that records them. They are a per-root copy alongside logging.go, probes.go
// and readiness.go, which all three roots carry, and auth.go and
// ratelimit.go, which the two authenticated ones do — for the reason all of
// those are copies: package main cannot import package main. One walk over
// the source enforces the label rule across every copy, which is what keeps
// the duplication from becoming three vocabularies.
var (
	httpRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fiapx_http_requests_total",
			Help: "Requests served, by request method, matched route template and response class.",
		},
		[]string{"method", "route", "status_class"},
	)

	httpRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "fiapx_http_request_duration_seconds",
			Help:    "Time spent serving a request, by request method and matched route template.",
			Buckets: requestDurationBuckets,
		},
		[]string{"method", "route"},
	)
)

func init() {
	metrics.MustRegister(httpRequests, httpRequestDuration)
}

// registerMetricsRoute mounts the exposition endpoint on the engine, beside
// the probes, and so outside any bearer group and any limiter this root
// mounts. The limiter keys on a subject a scrape does not carry and would
// eventually answer 429, which a scraper reads as the service being down; one
// of the three services mounts neither middleware at all, so requiring a
// token would be a policy on two of them and an accident on the third; and it
// would make Identity a dependency of every other service's observability,
// which is the coupling configuration-distributed public keys exist to
// remove.
func registerMetricsRoute(r *gin.Engine) {
	r.GET(metricsRoutePath, gin.WrapH(metrics.Handler()))
}

// routeEntries translates this router's own route table into the shared
// package's representation. The translation happens here rather than there
// because nothing under internal/ imports the HTTP framework, and because
// this is in any case the only place that knows which router it is talking
// about.
//
// It is called once, as the last statement of setupRouter: gin reports the
// table by walking its trees at the moment of the call, so binding it before
// the last route is registered would send a real route to the unmatched value
// for the life of the process.
func routeEntries(r *gin.Engine) []metrics.RouteEntry {
	routes := r.Routes()
	entries := make([]metrics.RouteEntry, 0, len(routes))
	for _, route := range routes {
		entries = append(entries, metrics.RouteEntry{Method: route.Method, Path: route.Path})
	}
	return entries
}

// recordRequest counts the request and observes how long it took. It is
// called from the access-log middleware, which already computes every input,
// and before that middleware's exemption check — so the two probe routes are
// metered while staying unrecorded. The exemption belongs to the access
// record and does not reach this capability: three more route values are a
// handful of series, and metering the probes gives back in the metric a
// signal the record deliberately gave up.
//
// The response class is written as a literal in each branch rather than
// rendered from the numeric code. Rendering it would mean strconv.Itoa at the
// call site, which is exactly the door the label rule closes; the exact code
// is already in this request's access record, which is the artefact to read
// when one response is being investigated.
func recordRequest(c *gin.Context, elapsed time.Duration) {
	httpRequestDuration.WithLabelValues(metrics.Method(c.Request.Method), metrics.Route(c.FullPath())).Observe(elapsed.Seconds())

	switch status := c.Writer.Status(); {
	case status >= 200 && status < 300:
		httpRequests.WithLabelValues(metrics.Method(c.Request.Method), metrics.Route(c.FullPath()), "2xx").Inc()
	case status >= 300 && status < 400:
		httpRequests.WithLabelValues(metrics.Method(c.Request.Method), metrics.Route(c.FullPath()), "3xx").Inc()
	case status >= 400 && status < 500:
		httpRequests.WithLabelValues(metrics.Method(c.Request.Method), metrics.Route(c.FullPath()), "4xx").Inc()
	case status >= 500 && status < 600:
		httpRequests.WithLabelValues(metrics.Method(c.Request.Method), metrics.Route(c.FullPath()), "5xx").Inc()
	default:
		httpRequests.WithLabelValues(metrics.Method(c.Request.Method), metrics.Route(c.FullPath()), "other").Inc()
	}
}

// The rate limiter's decision counter. It is declared here and recorded in
// ratelimit.go, in the two roots that mount a limiter — cmd/identity-api
// mounts none, because its two routes are how a caller obtains a token and
// gating them would be circular.
//
// The fail-open outcome is the reason this family exists. The limiter is
// specified to allow the request when its backing store fails, which
// rate-limiting calls a loss of enforcement rather than of efficiency, and it
// presently produces one warning record per request — a flood at exactly the
// moment nobody can read it, for a question whose answer is a total.
var rateLimitDecisions = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "fiapx_rate_limit_decisions_total",
		Help: "Rate-limit decisions, by outcome. A non-zero failed_open rate means enforcement is currently off.",
	},
	[]string{"decision"},
)

func init() {
	metrics.MustRegister(rateLimitDecisions)
}

// The upload handler's reservation counter, covering every branch that call
// site distinguishes rather than only the ones the feature is named for.
//
// duplicate is what deduplication is for; failed_open means it is silently
// off and repeat uploads are running ffmpeg again; conflict is the 409 a
// request receives when a reservation it did not win never resolved inside
// its bounded wait — ordinarily rare, and a rising rate of it means holders
// are dying between reserving and finalizing.
//
// Four outcomes because the handler takes four decisions. Three would leave
// the family's sum short of the uploads that reached the reservation, with
// nothing saying where the difference went.
var uploadReservations = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "fiapx_upload_idempotency_reservations_total",
		Help: "Idempotency reservations attempted by the upload handler, by the outcome it acted on.",
	},
	[]string{"outcome"},
)

func init() {
	metrics.MustRegister(uploadReservations)
}
