package main

import (
	"net/http"

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
}

func newProbeEndpoints(readiness *readinessChecker) *probeEndpoints {
	return &probeEndpoints{readiness: readiness}
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

	if len(p.readiness.failingDependencies(c.Request.Context())) > 0 {
		c.Data(http.StatusServiceUnavailable, probeContentType, []byte(probeBodyNotReady))
		return
	}
	c.Data(http.StatusOK, probeContentType, []byte(probeBodyReady))
}
