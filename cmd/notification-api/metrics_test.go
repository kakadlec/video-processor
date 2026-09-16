package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	notificationdomain "video-processor/internal/notification/domain"
	"video-processor/internal/platform/metrics"
)

// These tests read what a family recorded, which is process-global state, and
// they rebind the process-wide route table by building the real router. None
// of them is parallel-safe — the same cost slog.SetDefault already carries
// for the record tests beside them.

// sample is one point of the exposition, flattened out of the parsed
// families so an assertion can be written about a label value without
// naming the client library's wire types.
type sample struct {
	name   string
	labels map[string]string
	value  float64
}

// newMetricsTestRouter builds this service's real router, so every assertion
// below is made against the chain main mounts rather than a reconstruction of
// it — and so the route table these tests bind is the one main binds.
func newMetricsTestRouter(t *testing.T, limiter rateLimiter) (*gin.Engine, testTokens) {
	t.Helper()

	auth, tokens := newTestAuthenticatorWithTokens(t)
	module := newTestNotificationModuleWithPolicy(newInMemoryPreferenceRepository(), notificationdomain.NewDestinationPolicy(false))
	return setupRouter(auth, module, limiter, newChecker()), tokens
}

func serveRequest(t *testing.T, router http.Handler, method, target, authorization string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(method, target, nil)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

// scrapeSamples scrapes the endpoint and parses what came back with the
// client library's own text parser, rather than matching substrings against
// the body. What is asserted is then the exposition as a scraper reads it,
// including its wire form — a body that renders a label wrongly fails here
// and would pass a grep.
func scrapeSamples(t *testing.T, router http.Handler) []sample {
	t.Helper()

	recorder := serveRequest(t, router, http.MethodGet, metricsRoutePath, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("scraping %s answered %d, want %d: %s", metricsRoutePath, recorder.Code, http.StatusOK, recorder.Body.String())
	}

	// The scheme is passed rather than left to the package's global default,
	// which a library this repository does not control would otherwise decide
	// for this test.
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(recorder.Body.String()))
	if err != nil {
		t.Fatalf("the exposition does not parse: %v", err)
	}

	var samples []sample
	for name, family := range families {
		for _, metric := range family.GetMetric() {
			point := sample{name: name, labels: map[string]string{}}
			for _, pair := range metric.GetLabel() {
				point.labels[pair.GetName()] = pair.GetValue()
			}
			switch {
			case metric.GetCounter() != nil:
				point.value = metric.GetCounter().GetValue()
			case metric.GetGauge() != nil:
				point.value = metric.GetGauge().GetValue()
			case metric.GetHistogram() != nil:
				point.value = float64(metric.GetHistogram().GetSampleCount())
			}
			samples = append(samples, point)
		}
	}
	return samples
}

func samplesNamed(samples []sample, name string) []sample {
	var found []sample
	for _, point := range samples {
		if point.name == name {
			found = append(found, point)
		}
	}
	return found
}

// TestAScrapeIsAnsweredWithoutCredentials holds the endpoint's placement.
// This service mounts bearer authentication and the limiter on a group, and
// the endpoint is registered on the engine outside it.
func TestAScrapeIsAnsweredWithoutCredentials(t *testing.T) {
	router, _ := newMetricsTestRouter(t, alwaysAllowRateLimiter{})

	recorder := serveRequest(t, router, http.MethodGet, metricsRoutePath, "")

	if recorder.Code != http.StatusOK {
		t.Fatalf("an unauthenticated scrape answered %d, want %d", recorder.Code, http.StatusOK)
	}
	if len(scrapeSamples(t, router)) == 0 {
		t.Fatal("the exposition carries no sample at all")
	}
}

// TestAScrapeIsNotCountedAgainstARateLimitBudget holds the other half of that
// placement, and it is the half a behavioural test can actually distinguish:
// a limiter that refuses everything must not reach the endpoint. A scraper
// answered 429 reads the service as down.
func TestAScrapeIsNotCountedAgainstARateLimitBudget(t *testing.T) {
	router, tokens := newMetricsTestRouter(t, &fakeRateLimiter{allow: false})
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	if recorder := serveRequest(t, router, http.MethodGet, notificationPreferencesPath, "Bearer "+token); recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("an authenticated route answered %d with the budget exhausted, want %d — the limiter is not refusing anything", recorder.Code, http.StatusTooManyRequests)
	}
	if recorder := serveRequest(t, router, http.MethodGet, metricsRoutePath, ""); recorder.Code != http.StatusOK {
		t.Fatalf("a scrape answered %d with a user's budget exhausted, want %d", recorder.Code, http.StatusOK)
	}
}

// TestTheRouteAndMethodLabelsAreBoundedByTheRouteTable asserts the cardinality
// ceiling rather than arguing for it. Distinct unmatched paths and
// unrecognized methods are the position from which an unbounded series count
// is cheapest to create: the values are caller-supplied and reach the
// middleware before authentication and before the limiter.
func TestTheRouteAndMethodLabelsAreBoundedByTheRouteTable(t *testing.T) {
	router, _ := newMetricsTestRouter(t, alwaysAllowRateLimiter{})

	before := len(samplesNamed(scrapeSamples(t, router), "fiapx_http_requests_total"))

	for _, path := range []string{"/a", "/b", "/c", "/d/e", "/f/g/h", "/../i", "/j%20k", "/l?m=n"} {
		serveRequest(t, router, http.MethodGet, path, "")
	}
	for _, method := range []string{http.MethodDelete, http.MethodPatch, http.MethodHead, "PROPFIND"} {
		serveRequest(t, router, method, "/o", "")
	}

	after := scrapeSamples(t, router)
	requests := samplesNamed(after, "fiapx_http_requests_total")

	// Twelve requests at eight distinct paths and five distinct methods. What
	// they may add is one series: the unmatched route and the unrecognized
	// method at one response class. The scrape that read `before` added one
	// of its own, so the bound is two.
	if grown := len(requests) - before; grown > 2 {
		t.Fatalf("twelve requests at eight distinct unmatched paths added %d series; the route label is not bounded by the route table", grown)
	}

	// And the values themselves are members of the table or the two
	// fallbacks, which is the claim the count above is evidence for rather
	// than a restatement of it.
	registered := map[string]bool{metrics.UnmatchedRoute: true}
	methods := map[string]bool{metrics.UnrecognizedMethod: true}
	for _, route := range router.Routes() {
		registered[route.Path] = true
		methods[route.Method] = true
	}
	for _, point := range requests {
		if !registered[point.labels["route"]] {
			t.Errorf("a route label is neither a registered template nor the fallback: %q", point.labels["route"])
		}
		if !methods[point.labels["method"]] {
			t.Errorf("a method label is neither a registered method nor the fallback: %q", point.labels["method"])
		}
	}
}

// TestNoLabelValueLooksLikeAnIdentifier drives the endpoint after real
// authenticated traffic, so there are identifiers in the process to leak, and
// then asserts none reached a label. A subject, a bearer token and a
// destination URL all passed through the chain that records these families.
func TestNoLabelValueLooksLikeAnIdentifier(t *testing.T) {
	router, tokens := newMetricsTestRouter(t, alwaysAllowRateLimiter{})
	userID, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	serveRequest(t, router, http.MethodGet, notificationPreferencesPath, "Bearer "+token)
	serveRequest(t, router, http.MethodGet, "/"+userID.String(), "")

	for _, point := range scrapeSamples(t, router) {
		for name, value := range point.labels {
			requireNotAnIdentifier(t, point.name, name, value)
		}
	}
}

// requireNotAnIdentifier fails on the shapes this capability forbids in a
// label: a UUID, an address, a URL, a storage key. It judges the shape rather
// than a known list of values, so a leak of an identifier no test minted is
// caught too.
func requireNotAnIdentifier(t *testing.T, family, label, value string) {
	t.Helper()

	// The runtime collectors carry a version string and a path-shaped
	// nothing; they are the library's own series and carry no value of ours.
	if strings.HasPrefix(family, "go_") || strings.HasPrefix(family, "process_") {
		return
	}
	switch {
	case strings.Count(value, "-") == 4 && len(value) == 36:
		t.Errorf("%s{%s=%q} looks like a UUID", family, label, value)
	case strings.Contains(value, "@"):
		t.Errorf("%s{%s=%q} looks like an e-mail address", family, label, value)
	case strings.Contains(value, "://"):
		t.Errorf("%s{%s=%q} looks like a URL", family, label, value)
	case strings.HasPrefix(value, "uploads/") || strings.HasPrefix(value, "frames_"):
		t.Errorf("%s{%s=%q} looks like a storage key", family, label, value)
	case len(value) == 64 && !strings.ContainsAny(value, "/ -"):
		t.Errorf("%s{%s=%q} looks like a content hash", family, label, value)
	}
}

// TestAScrapeYieldsAnAccessRecord is the half of this change a reviewer will
// assume went the other way. The probe exemption is not extended to this
// endpoint: its justification rests on volume from a prober arriving at a
// fixed interval forever, and no scraper exists in this repository, so
// claiming that volume would be reasoning from a consumer that does not
// exist.
func TestAScrapeYieldsAnAccessRecord(t *testing.T) {
	buffer := captureRecords(t)
	router, _ := newMetricsTestRouter(t, alwaysAllowRateLimiter{})

	serveRequest(t, router, http.MethodGet, metricsRoutePath, "")

	record := onlyRecord(t, buffer, componentHTTPAccess)
	requireField(t, record, "route", metricsRoutePath)
	requireField(t, record, "method", http.MethodGet)
	requireField(t, record, "status", float64(http.StatusOK))
}

// TestAProbeIsMeteredThoughItIsNotRecorded holds the pair the other way
// round. The per-request metric carries no exemption list of its own: three
// more route values are a handful of series, an exemption list is a mechanism
// that grows, and metering the probes returns in the metric a signal the
// access record deliberately gave up.
func TestAProbeIsMeteredThoughItIsNotRecorded(t *testing.T) {
	buffer := captureRecords(t)
	router, _ := newMetricsTestRouter(t, alwaysAllowRateLimiter{})

	serveRequest(t, router, http.MethodGet, healthRoutePath, "")

	if records := recordsWithComponent(t, buffer, componentHTTPAccess); len(records) != 0 {
		t.Fatalf("a probe yielded %d access records, want 0", len(records))
	}
	if !hasRouteLabel(scrapeSamples(t, router), "fiapx_http_requests_total", healthRoutePath) {
		t.Fatalf("the liveness probe is not counted under %q", healthRoutePath)
	}
}

func hasRouteLabel(samples []sample, family, route string) bool {
	for _, point := range samplesNamed(samples, family) {
		if point.labels["route"] == route {
			return true
		}
	}
	return false
}

// TestTheRouteTableIsBoundToThisRoutersOwnRoutes reconciles the binding with
// the router. A BindRoutes call placed before the last registration silently
// binds a short table and sends real routes to the unmatched value for the
// life of the process, which no assertion about one route would catch.
func TestTheRouteTableIsBoundToThisRoutersOwnRoutes(t *testing.T) {
	router, _ := newMetricsTestRouter(t, alwaysAllowRateLimiter{})

	for _, route := range router.Routes() {
		if resolved := metrics.Route(route.Path); resolved != route.Path {
			t.Errorf("a registered route resolves to %q rather than to itself: %s %s", resolved, route.Method, route.Path)
		}
		if resolved := metrics.Method(route.Method); resolved != route.Method {
			t.Errorf("a registered method resolves to %q rather than to itself: %s %s", resolved, route.Method, route.Path)
		}
	}
	if metrics.Route(metricsRoutePath) != metricsRoutePath {
		t.Fatalf("the exposition endpoint is not in the table it binds, so every scrape would be counted as unmatched traffic")
	}
}

// counterValue reads one series of a counter family out of the exposition.
// The registry is process-wide, so these tests read a delta rather than an
// absolute: every other test in this binary records into the same families.
func counterValue(t *testing.T, router http.Handler, family, label, value string) float64 {
	t.Helper()

	for _, point := range samplesNamed(scrapeSamples(t, router), family) {
		if point.labels[label] == value {
			return point.value
		}
	}
	return 0
}

// TestEveryRateLimitDecisionIsCounted covers all three branches, and the
// third is the reason the family exists. A test that exercised only allowed
// and denied would pass with the fail-open branch uninstrumented — the branch
// on which abuse control is off right now, which is a total rather than the
// per-request warning it presently produces.
func TestEveryRateLimitDecisionIsCounted(t *testing.T) {
	const family = "fiapx_rate_limit_decisions_total"

	for _, tc := range []struct {
		decision string
		limiter  rateLimiter
	}{
		{decision: "allowed", limiter: alwaysAllowRateLimiter{}},
		{decision: "denied", limiter: &fakeRateLimiter{allow: false}},
		{decision: "failed_open", limiter: &fakeRateLimiter{err: errRateLimitStoreDown}},
	} {
		t.Run(tc.decision, func(t *testing.T) {
			router, tokens := newMetricsTestRouter(t, tc.limiter)
			_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

			before := counterValue(t, router, family, "decision", tc.decision)
			serveRequest(t, router, http.MethodGet, notificationPreferencesPath, "Bearer "+token)
			after := counterValue(t, router, family, "decision", tc.decision)

			if after-before != 1 {
				t.Fatalf("one request produced %g %s decision(s), want 1", after-before, tc.decision)
			}
		})
	}
}

var errRateLimitStoreDown = errors.New("the rate-limit store is unreachable")
