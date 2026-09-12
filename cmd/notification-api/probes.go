package main

import (
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

// The two probe route templates, as gin reports them from c.FullPath(). They
// are constants rather than literals at the registration site because more
// than one place has to name the same template and agree with the router
// about it.
const (
	healthRoutePath = "/health"
	readyRoutePath  = "/ready"
)

// The probe response bodies, one literal per verdict and nothing that varies.
//
// Fixed is the requirement, not the formatting. A readiness body naming which
// dependency failed would publish this deployment's dependency inventory to
// an unauthenticated caller, and — by repetition — the times at which each
// part of it is degraded. That is the posture the destination policy's single
// sentinel and the download route's byte-identical 404 already take: a
// response whose detail differs per cause is readable by resubmission. The
// status code carries the whole answer; which dependency failed goes to the
// log, whose reader is already inside the deployment.
const (
	probeBodyHealthy  = `{"status":"ok"}`
	probeBodyReady    = `{"status":"ready"}`
	probeBodyNotReady = `{"status":"not ready"}`
)

// probeContentType is the content type both probes answer with. Written out
// rather than reached through c.JSON so that what goes on the wire is the
// literal above and cannot acquire a field by someone extending a struct.
const probeContentType = "application/json; charset=utf-8"

// probeEndpoints serves this process's liveness and readiness probes.
type probeEndpoints struct {
	readiness *readinessChecker

	// The verdict the last answered readiness probe reached, held for the
	// life of the process so that a change of verdict can be recorded once
	// instead of every verdict being recorded every time. It is what the
	// access-record exemption trades the per-probe record for.
	ready atomic.Bool
}

func newProbeEndpoints(readiness *readinessChecker) *probeEndpoints {
	p := &probeEndpoints{readiness: readiness}

	// Ready is the initial verdict, and it is chosen rather than inherited
	// from the zero value. A process that starts not ready reaches its first
	// probe as a transition and is recorded, which is the case that must not
	// be silent; a process that starts ready emits nothing, rather than
	// announcing a recovery from a degradation it never had.
	p.ready.Store(true)
	return p
}

// registerRoutes mounts both probes on the engine. They are registered for GET
// alone: the endpoints are specified as GET, and a prober that issues anything
// else is a prober to fix rather than a route to widen.
func (p *probeEndpoints) registerRoutes(r *gin.Engine) {
	r.GET(healthRoutePath, p.handleHealth)
	r.GET(readyRoutePath, p.handleReady)
}

// handleHealth answers liveness. It consults nothing — no dependency, no
// state of this process beyond its being able to run this function — and that
// is the whole point of it being a second endpoint. The remedy a runtime
// wires to a liveness failure is restart, and restarting a process cannot
// repair a dependency: an endpoint that both consults dependencies and drives
// restarts turns a bounded outage into a crash loop that outlives it.
func (p *probeEndpoints) handleHealth(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, probeContentType, []byte(probeBodyHealthy))
}

// handleReady answers readiness by consulting this service's readiness
// dependencies on every request.
//
// The check is derived from the request's own context, not from a background
// one: the checker bounds each dependency against whatever it is handed, so
// handing it a context the caller cannot cancel would keep the timeout half
// of that bound and silently drop the other half — a prober that gave up and
// disconnected would still be paid for in full.
func (p *probeEndpoints) handleReady(c *gin.Context) {
	// Set before the branch so it holds on both verdicts: a stored answer is
	// an answer for a different moment than the prober asked about, and it is
	// also what keeps the two verdicts identical in everything but the status
	// code and the body.
	c.Header("Cache-Control", "no-store")

	if failing := p.readiness.failingDependencies(c.Request.Context()); len(failing) > 0 {
		p.recordDegradation(failing)
		c.Data(http.StatusServiceUnavailable, probeContentType, []byte(probeBodyNotReady))
		return
	}
	p.recordRecovery()
	c.Data(http.StatusOK, probeContentType, []byte(probeBodyReady))
}

// recordDegradation records the move from ready to not ready, once, naming
// the dependencies that failed.
//
// The swap is atomic rather than a read followed by a write: two probes
// answered concurrently both observe the old verdict, and only the one that
// actually swapped it may record the transition. A read-then-write would
// record the same change twice, which is exactly the noise the access-record
// exemption was granted to avoid.
//
// The names arrive as a slice and are joined into one string attribute. The
// record format admits only typed scalars — slog.Any is denied by name — so
// the slice cannot be passed, and the closed set these names are drawn from
// is two entries long in the largest service, which is why one joined value
// stays readable.
func (p *probeEndpoints) recordDegradation(failing []string) {
	if !p.ready.CompareAndSwap(true, false) {
		return
	}
	logger(componentReadinessProbe).Warn("this service is no longer ready",
		slog.String("dependencies", strings.Join(failing, ",")),
		slog.Int("dependency_count", len(failing)))
}

// recordRecovery records the move from not ready back to ready, once. It
// names no dependency: what recovered is the service, and which check now
// answers is not a fact this probe learned — every check answered.
func (p *probeEndpoints) recordRecovery() {
	if !p.ready.CompareAndSwap(false, true) {
		return
	}
	logger(componentReadinessProbe).Info("this service is ready again")
}
