package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// These tests install their own process-wide default logger and replace gin's
// package-level writers. Both are shared state, exactly as the log.SetOutput
// captures they replace were, so none of them is parallel-safe.

// captureRecords installs a JSON logger over a buffer as the process-wide
// default for the duration of the test.
func captureRecords(t *testing.T) *bytes.Buffer {
	t.Helper()

	return captureRecordsAt(t, slog.LevelDebug)
}

// captureRecordsAt is captureRecords at a chosen severity, so a test can run
// the process the way an operator who set LOG_LEVEL=error runs it.
func captureRecordsAt(t *testing.T, level slog.Level) *bytes.Buffer {
	t.Helper()

	buffer := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buffer
}

// captureUnstructuredOutput redirects standard error and gin's own package
// writers to a file, so anything written outside the record — the ANSI stack
// block gin.CustomRecovery prints before delegating — is observable rather
// than invisible. It selects release mode for the same reason: gin.New() and
// every route registration write an unstructured [GIN-debug] line to
// gin.DefaultWriter while the mode is debug, which would otherwise land in the
// very buffer whose emptiness is the assertion.
func captureUnstructuredOutput(t *testing.T) func() string {
	t.Helper()

	file, err := os.CreateTemp(t.TempDir(), "unstructured")
	if err != nil {
		t.Fatalf("failed to create the capture file: %v", err)
	}

	previousMode := gin.Mode()
	previousStderr := os.Stderr
	previousWriter, previousErrorWriter := gin.DefaultWriter, gin.DefaultErrorWriter

	gin.SetMode(gin.ReleaseMode)
	os.Stderr = file
	gin.DefaultWriter, gin.DefaultErrorWriter = file, file

	t.Cleanup(func() {
		gin.SetMode(previousMode)
		os.Stderr = previousStderr
		gin.DefaultWriter, gin.DefaultErrorWriter = previousWriter, previousErrorWriter
		_ = file.Close()
	})

	return func() string {
		contents, err := os.ReadFile(file.Name())
		if err != nil {
			t.Fatalf("failed to read the capture file: %v", err)
		}
		return string(contents)
	}
}

const loggingProbeRoute = "/probe/:name"

func registerLoggingProbeRoutes(r *gin.Engine) {
	r.GET(loggingProbeRoute, func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	r.GET("/panic", func(c *gin.Context) { panic("a deliberate panic") })
	r.GET("/panic/broken-pipe", func(c *gin.Context) { panic(fmt.Errorf("write tcp: %w", syscall.EPIPE)) })
	r.GET("/panic/aborted", func(c *gin.Context) { panic(http.ErrAbortHandler) })
}

// newLoggingTestRouter mounts this service's global middleware pair, in the
// order and at the position setupRouter mounts it, over probe routes. Anything
// in extra stands where requireBearerAuth stands: behind the access log, which
// is why the subject it establishes reaches the record.
func newLoggingTestRouter(extra ...gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	r.Use(accessLogMiddleware(), recoveryMiddleware())
	r.Use(extra...)
	registerLoggingProbeRoutes(r)
	return r
}

// ginRecoveryBaseline reproduces the response gin.Default() produced before
// this change. RecoveryWithWriter is the code path Recovery() itself takes —
// the writer feeds only the unstructured block, never the branch logic — so
// this router answers a panic exactly as the shipped one did.
func ginRecoveryBaseline() *gin.Engine {
	r := gin.New()
	r.Use(gin.RecoveryWithWriter(io.Discard))
	registerLoggingProbeRoutes(r)
	return r
}

func serveLoggingProbe(r *gin.Engine, method, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
	return recorder
}

type probeResponse struct {
	Status int
	Body   string
	Header http.Header
}

func capturedResponse(t *testing.T, recorder *httptest.ResponseRecorder) probeResponse {
	t.Helper()

	result := recorder.Result()
	defer func() { _ = result.Body.Close() }()

	body, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatalf("failed to read the probe response body: %v", err)
	}
	header := result.Header.Clone()
	header.Del("Date")
	return probeResponse{Status: result.StatusCode, Body: string(body), Header: header}
}

func decodeRecords(t *testing.T, buffer *bytes.Buffer) []map[string]any {
	t.Helper()

	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		if line == "" {
			continue
		}
		record := map[string]any{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("a record is not JSON: %v (%q)", err, line)
		}
		records = append(records, record)
	}
	return records
}

// recordsWithComponent returns every record attributed to component, in the
// order they were emitted. A count of zero is an assertion in its own right —
// the access-record exemption is stated as an absence — and it has to be read
// per component rather than off an empty buffer, because a readiness probe
// that changes verdict writes a record of its own into the same buffer.
func recordsWithComponent(t *testing.T, buffer *bytes.Buffer, component string) []map[string]any {
	t.Helper()

	var found []map[string]any
	for _, record := range decodeRecords(t, buffer) {
		if record["component"] == component {
			found = append(found, record)
		}
	}
	return found
}

func onlyRecord(t *testing.T, buffer *bytes.Buffer, component string) map[string]any {
	t.Helper()

	found := recordsWithComponent(t, buffer, component)
	if len(found) != 1 {
		t.Fatalf("expected exactly one %q record, got %d: %s", component, len(found), buffer.String())
	}
	return found[0]
}

func requireField(t *testing.T, record map[string]any, key string, want any) {
	t.Helper()

	if got, ok := record[key]; !ok || got != want {
		t.Fatalf("record field %q: got %v (present=%t), want %v", key, got, ok, want)
	}
}

func requireNoField(t *testing.T, record map[string]any, key string) {
	t.Helper()

	if got, ok := record[key]; ok {
		t.Fatalf("record carries %q = %v; it must be absent, not empty", key, got)
	}
}

// TestTheAccessRecordCarriesTheRequestFacts holds the field set: method,
// matched route, status, duration, size — and no request path, because the
// request matched a route.
func TestTheAccessRecordCarriesTheRequestFacts(t *testing.T) {
	buffer := captureRecords(t)
	unstructured := captureUnstructuredOutput(t)

	serveLoggingProbe(newLoggingTestRouter(), http.MethodGet, "/probe/one")

	record := onlyRecord(t, buffer, componentHTTPAccess)
	requireField(t, record, "level", "INFO")
	requireField(t, record, "method", http.MethodGet)
	requireField(t, record, "route", loggingProbeRoute)
	requireField(t, record, "status", float64(http.StatusOK))
	requireField(t, record, "size", float64(len("ok")))
	requireNoField(t, record, "path")

	duration, ok := record["duration"].(float64)
	if !ok || duration < 0 {
		t.Fatalf("record field \"duration\": got %v, want a non-negative number", record["duration"])
	}

	if output := unstructured(); output != "" {
		t.Fatalf("serving a request wrote outside the record: %q", output)
	}
}

// TestTheAccessRecordExcludesTheQueryString covers both branches. The unmatched
// one is the load-bearing half: a matched request records no path at all, so a
// query assertion there would keep passing if the path field ever became
// unconditional or were taken from RequestURI.
func TestTheAccessRecordExcludesTheQueryString(t *testing.T) {
	const parameter = "handoff"
	const value = "a-query-string-credential"

	for name, target := range map[string]string{
		"a request that matched a route": "/probe/one?" + parameter + "=" + value,
		"a request that matched nothing": "/no-such-route?" + parameter + "=" + value,
	} {
		t.Run(name, func(t *testing.T) {
			buffer := captureRecords(t)
			captureUnstructuredOutput(t)

			serveLoggingProbe(newLoggingTestRouter(), http.MethodGet, target)

			// Asserted against the raw bytes rather than field by field, so a
			// leak through any field — including one added later — is caught.
			emitted := buffer.String()
			if strings.Contains(emitted, parameter) || strings.Contains(emitted, value) {
				t.Fatalf("the query string reached the record: %s", emitted)
			}
		})
	}
}

// TestTheAccessRecordBoundsTheCallerSuppliedFields holds 5.2a and 5.2b
// together, because they are the same objection: the path and the method beside
// it both reach this record before authentication and before the rate limiter.
func TestTheAccessRecordBoundsTheCallerSuppliedFields(t *testing.T) {
	longMethod := strings.Repeat("X", 3000)
	longPath := "/" + strings.Repeat("a", 4096)

	t.Run("an unmatched request", func(t *testing.T) {
		buffer := captureRecords(t)
		captureUnstructuredOutput(t)

		serveLoggingProbe(newLoggingTestRouter(), longMethod, longPath)

		record := onlyRecord(t, buffer, componentHTTPAccess)
		requireField(t, record, "method", unrecognizedMethod)
		requireNoField(t, record, "route")

		path, ok := record["path"].(string)
		if !ok {
			t.Fatalf("an unmatched request recorded no path: %v", record["path"])
		}
		if len(path) != maxRecordedPathLength {
			t.Fatalf("the recorded path is %d bytes, want the fixed bound of %d", len(path), maxRecordedPathLength)
		}
		if !strings.HasPrefix(longPath, path) {
			t.Fatalf("the recorded path %q is not a prefix of the requested one", path)
		}
		if len(buffer.String()) > 4*maxRecordedPathLength {
			t.Fatalf("the record is %d bytes for a %d-byte request; it is not bounded", len(buffer.String()), len(longMethod)+len(longPath))
		}
	})

	t.Run("a matched request carries no path at all", func(t *testing.T) {
		buffer := captureRecords(t)
		captureUnstructuredOutput(t)

		serveLoggingProbe(newLoggingTestRouter(), http.MethodGet, "/probe/one")

		requireNoField(t, onlyRecord(t, buffer, componentHTTPAccess), "path")
	})
}

// TestARecoveredPanicIsRecordedAndAnsweredExactlyAsBefore covers both halves of
// 5.3b on the ordinary branch. The captured pre-change behaviour is 500 with an
// empty body; the comparison against gin's own recovery is what keeps this
// assertion and the response from drifting together silently.
func TestARecoveredPanicIsRecordedAndAnsweredExactlyAsBefore(t *testing.T) {
	buffer := captureRecords(t)
	unstructured := captureUnstructuredOutput(t)

	got := capturedResponse(t, serveLoggingProbe(newLoggingTestRouter(), http.MethodGet, "/panic"))
	before := capturedResponse(t, serveLoggingProbe(ginRecoveryBaseline(), http.MethodGet, "/panic"))

	record := onlyRecord(t, buffer, componentHTTPRecovery)
	requireField(t, record, "level", "ERROR")
	requireField(t, record, "panic", "a deliberate panic")
	requireField(t, record, "broken_pipe", false)
	if stack, ok := record["stack"].(string); !ok || !strings.Contains(stack, "recoveryMiddleware") {
		t.Fatalf("the record carries no usable stack: %v", record["stack"])
	}

	// The access record is still written, and reports the status the recovered
	// request actually answered.
	requireField(t, onlyRecord(t, buffer, componentHTTPAccess), "status", float64(http.StatusInternalServerError))

	if !reflect.DeepEqual(got, before) {
		t.Fatalf("the recovered-panic response changed: got %+v, gin's recovery answers %+v", got, before)
	}
	if got.Status != http.StatusInternalServerError || got.Body != "" {
		t.Fatalf("got %d %q, want the captured baseline 500 with an empty body", got.Status, got.Body)
	}
	if output := unstructured(); output != "" {
		t.Fatalf("a recovered panic wrote a second, unstructured line: %q", output)
	}
}

// TestARecoveredPanicOnABrokenConnectionIsRecordedAndAnsweredExactlyAsBefore
// covers the branch gin takes before it ever reaches a supplied handler, which
// is why this middleware is written rather than delegated: under
// gin.CustomRecovery with a nil writer this class of panic is recorded nowhere.
func TestARecoveredPanicOnABrokenConnectionIsRecordedAndAnsweredExactlyAsBefore(t *testing.T) {
	for name, target := range map[string]string{
		"a write to a closed pipe": "/panic/broken-pipe",
		"an aborted handler":       "/panic/aborted",
	} {
		t.Run(name, func(t *testing.T) {
			buffer := captureRecords(t)
			unstructured := captureUnstructuredOutput(t)

			got := capturedResponse(t, serveLoggingProbe(newLoggingTestRouter(), http.MethodGet, target))
			before := capturedResponse(t, serveLoggingProbe(ginRecoveryBaseline(), http.MethodGet, target))

			record := onlyRecord(t, buffer, componentHTTPRecovery)
			requireField(t, record, "level", "ERROR")
			requireField(t, record, "broken_pipe", true)
			if panicked, ok := record["panic"].(string); !ok || panicked == "" {
				t.Fatalf("the record carries no panic value: %v", record["panic"])
			}

			if !reflect.DeepEqual(got, before) {
				t.Fatalf("the broken-connection response changed: got %+v, gin's recovery answers %+v", got, before)
			}
			if got.Body != "" {
				t.Fatalf("a body was written to a dead connection: %q", got.Body)
			}
			if output := unstructured(); output != "" {
				t.Fatalf("a recovered panic wrote a second, unstructured line: %q", output)
			}
		})
	}
}

// TestTheServiceRouterMountsTheAccessLog drives the real setupRouter rather
// than a reconstruction of it. Everything above builds its own engine, so a
// root that stopped mounting the pair would leave every one of those tests
// green — gin.Default() still compiles, and an unused middleware is not an
// error.
func TestTheServiceRouterMountsTheAccessLog(t *testing.T) {
	buffer := captureRecords(t)
	unstructured := captureUnstructuredOutput(t)

	recorder := httptest.NewRecorder()
	setupRouter(newTestIdentityModule(t), newChecker()).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/no-such-route", nil))

	record := onlyRecord(t, buffer, componentHTTPAccess)
	requireField(t, record, "method", http.MethodGet)
	requireField(t, record, "path", "/no-such-route")

	if output := unstructured(); output != "" {
		t.Fatalf("the service router wrote outside the record: %q", output)
	}
}

// serveErrorProbe serves one request through the server newHTTPServer builds.
// It serves on an ephemeral listener rather than through ListenAndServe:
// Serve ignores Addr, so the real construction is driven without binding the
// port the deployment uses. Shutdown is awaited before the caller reads the
// buffer, so the record net/http wrote on a connection goroutine is ordered
// before the read.
func serveErrorProbe(t *testing.T, handler http.Handler) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to open a listener: %v", err)
	}

	server := newHTTPServer(handler)
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = server.Serve(listener)
	}()

	response, err := http.Get("http://" + listener.Addr().String() + "/")
	if err != nil {
		t.Fatalf("the probe request failed: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("the probe server did not shut down: %v", err)
	}
	<-served
}

// unparseableContentLength provokes an error net/http reports through
// Server.ErrorLog itself. It is this provocation and not an obvious one
// because the obvious ones do not reach that logger at all: a malformed
// request line, an invalid header name and a TLS handshake against this plain
// listener are each answered with a status and no report, and a panic raised
// inside a handler is taken by the recovery middleware before net/http sees
// it. An unparseable response Content-Length is parsed by net/http on the
// connection goroutine before the status line is flushed, which also orders
// the report ahead of the client's response.
func unparseableContentLength() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "not-a-number")
		w.WriteHeader(http.StatusOK)
	})
}

// TestTheHTTPServerRecordsItsOwnErrorsAtErrorSeverity drives newHTTPServer,
// the construction main serves through, rather than a server the test
// configures: a correctly configured test server would prove nothing about a
// root that left the field nil.
func TestTheHTTPServerRecordsItsOwnErrorsAtErrorSeverity(t *testing.T) {
	t.Run("the report is recorded at error severity", func(t *testing.T) {
		buffer := captureRecordsAt(t, slog.LevelDebug)

		serveErrorProbe(t, unparseableContentLength())

		record := onlyRecord(t, buffer, componentHTTPServer)
		requireField(t, record, "level", "ERROR")
		if message, ok := record["msg"].(string); !ok || !strings.Contains(message, "invalid Content-Length") {
			t.Fatalf("the record does not carry what net/http reported: %v", record["msg"])
		}
	})

	t.Run("the report survives a process running at error severity", func(t *testing.T) {
		// The half that matters. The standard log bridge slog.SetDefault
		// installs is pinned at info, so with ErrorLog left nil every report
		// net/http makes is discarded in exactly the configuration an
		// operator chooses to cut noise.
		buffer := captureRecordsAt(t, slog.LevelError)

		serveErrorProbe(t, unparseableContentLength())

		onlyRecord(t, buffer, componentHTTPServer)
	})
}

// newProbeAccessRouter builds this service's real router over a caller-supplied
// readiness checker. The exemption keys on the matched route template, so it
// can only be exercised against the router that registers the probes: against
// the engine the rest of this file builds, a request for the liveness path
// matches nothing at all and is recorded with a path like any other unmatched
// request — and a test written there would pass against an exemption keyed on
// the request path, which is the wrong key.
func newProbeAccessRouter(t *testing.T, readiness *readinessChecker) *gin.Engine {
	t.Helper()

	return setupRouter(newTestIdentityModule(t), readiness)
}

// TestAProbeYieldsNoAccessRecordAndIsStillServed holds the exemption and the
// trap it sets in one assertion. The natural implementation — returning from
// the access middleware as soon as the matched route is a probe template —
// never calls c.Next(), so the probe handler never runs: every probe then
// answers 200 with an empty body and none of the headers the handler sets,
// which satisfies both "a probe yields no access record" and "a probe answers
// 200" and fails only an assertion that reads what came back. The exemption
// suppresses the record, not the chain.
func TestAProbeYieldsNoAccessRecordAndIsStillServed(t *testing.T) {
	for name, probe := range map[string]struct {
		path    string
		failing bool
		status  int
		body    string
	}{
		"liveness":             {path: healthRoutePath, status: http.StatusOK, body: probeBodyHealthy},
		"readiness, ready":     {path: readyRoutePath, status: http.StatusOK, body: probeBodyReady},
		"readiness, not ready": {path: readyRoutePath, failing: true, status: http.StatusServiceUnavailable, body: probeBodyNotReady},
	} {
		t.Run(name, func(t *testing.T) {
			buffer := captureRecords(t)
			captureUnstructuredOutput(t)

			dependency := readinessProbeOK
			if probe.failing {
				dependency = readinessProbeFailing
			}
			router := newProbeAccessRouter(t, newChecker(readinessCheck{name: dependencyDatabase, probe: dependency}))

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, probe.path, nil))

			if recorder.Code != probe.status {
				t.Fatalf("%s answered %d, want %d", probe.path, recorder.Code, probe.status)
			}
			if got := recorder.Body.String(); got != probe.body {
				t.Fatalf("%s answered body %q, want %q — the exemption suppresses the record, not the chain", probe.path, got, probe.body)
			}
			if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("%s answered Cache-Control %q, want %q — the probe handler did not run", probe.path, got, "no-store")
			}

			if records := recordsWithComponent(t, buffer, componentHTTPAccess); len(records) != 0 {
				t.Fatalf("a probe yielded %d access records: %s", len(records), buffer.String())
			}
		})
	}
}

// TestEveryRouteOtherThanAProbeStillYieldsAnAccessRecord is the boundary the
// exemption has to be held to rather than its existence: an assertion that a
// probe yields no record passes against a middleware that was removed
// entirely. The rows carrying another method against a probe path are the
// discriminating ones — they match no route, so the record must be written,
// and an exemption keyed on the request path would suppress it.
func TestEveryRouteOtherThanAProbeStillYieldsAnAccessRecord(t *testing.T) {
	for name, request := range map[string]struct {
		method string
		target string
		route  string
		path   string
	}{
		"a matched route":                      {method: http.MethodPost, target: "/api/auth/login", route: "/api/auth/login"},
		"a request that matched no route":      {method: http.MethodGet, target: "/no-such-route", path: "/no-such-route"},
		"the liveness path on another method":  {method: http.MethodPost, target: healthRoutePath, path: healthRoutePath},
		"the readiness path on another method": {method: http.MethodPost, target: readyRoutePath, path: readyRoutePath},
	} {
		t.Run(name, func(t *testing.T) {
			buffer := captureRecords(t)
			captureUnstructuredOutput(t)

			router := newProbeAccessRouter(t, newChecker())
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(request.method, request.target, nil))

			record := onlyRecord(t, buffer, componentHTTPAccess)
			if request.route != "" {
				requireField(t, record, "route", request.route)
				return
			}
			requireField(t, record, "path", request.path)
			requireNoField(t, record, "route")
		})
	}
}

// TestAPanicWhileServingAProbeIsStillRecorded pins the other boundary the
// skip's placement in the chain can get wrong. What is exempt is the routine
// per-request record and not the report of a failure, so the recovery record
// survives a probe route and the access record does not.
//
// The panicking handler is registered at the real probe template on an engine
// of its own: the exemption keys on the matched route, so a handler registered
// under any other path would not exercise it at all.
func TestAPanicWhileServingAProbeIsStillRecorded(t *testing.T) {
	for _, route := range []string{healthRoutePath, readyRoutePath} {
		t.Run(route, func(t *testing.T) {
			buffer := captureRecords(t)
			unstructured := captureUnstructuredOutput(t)

			r := gin.New()
			r.Use(accessLogMiddleware(), recoveryMiddleware())
			r.GET(route, func(c *gin.Context) { panic("a deliberate panic") })

			recorder := httptest.NewRecorder()
			r.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, route, nil))

			record := onlyRecord(t, buffer, componentHTTPRecovery)
			requireField(t, record, "level", "ERROR")
			requireField(t, record, "route", route)
			requireField(t, record, "panic", "a deliberate panic")

			if records := recordsWithComponent(t, buffer, componentHTTPAccess); len(records) != 0 {
				t.Fatalf("a panicking probe yielded %d access records; the exemption covers the routine record, not the report of a failure", len(records))
			}
			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("a panicking probe answered %d, want 500", recorder.Code)
			}
			if output := unstructured(); output != "" {
				t.Fatalf("a recovered panic wrote outside the record: %q", output)
			}
		})
	}
}

// togglableReadinessProbe returns a dependency check whose verdict the caller
// controls, so one router can be driven across a failure and a recovery.
func togglableReadinessProbe() (func(context.Context) error, func(bool)) {
	var failing atomic.Bool
	probe := func(context.Context) error {
		if failing.Load() {
			return errProbeDependencyDown
		}
		return nil
	}
	return probe, failing.Store
}

// TestTheReadinessVerdictIsRecordedOncePerTransition is what the access-record
// exemption traded the per-probe record for, and the count is the whole
// assertion: a probe answered every few seconds forever must record a change
// of verdict and not a verdict. Nine probes spanning one failure and one
// recovery yield exactly two records.
//
// The run opens with three probes that are ready and expects silence from
// them, which pins the initial verdict: a process that starts ready has not
// recovered from anything, so announcing that it had would be a record of an
// event that never happened.
func TestTheReadinessVerdictIsRecordedOncePerTransition(t *testing.T) {
	buffer := captureRecords(t)
	captureUnstructuredOutput(t)

	probe, setFailing := togglableReadinessProbe()
	router := newProbeAccessRouter(t, newChecker(readinessCheck{name: dependencyDatabase, probe: probe}))

	probeRepeatedly := func(want int) {
		t.Helper()
		for i := 0; i < 3; i++ {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, readyRoutePath, nil))
			if recorder.Code != want {
				t.Fatalf("%s answered %d, want %d", readyRoutePath, recorder.Code, want)
			}
		}
	}

	probeRepeatedly(http.StatusOK)
	setFailing(true)
	probeRepeatedly(http.StatusServiceUnavailable)
	setFailing(false)
	probeRepeatedly(http.StatusOK)

	records := recordsWithComponent(t, buffer, componentReadinessProbe)
	if len(records) != 2 {
		t.Fatalf("nine probes across one failure and one recovery yielded %d records, want 2: %s", len(records), buffer.String())
	}

	requireField(t, records[0], "level", "WARN")
	requireField(t, records[0], "dependencies", dependencyDatabase)
	requireField(t, records[0], "dependency_count", float64(1))

	requireField(t, records[1], "level", "INFO")
	requireNoField(t, records[1], "dependencies")
}

// TestTwoConcurrentProbesObservingOneTransitionRecordItOnce is what makes the
// compare-and-swap load-bearing rather than decorative: a read followed by a
// write has both callers observe the old verdict and both record the change.
//
// The dependency check holds every caller at a barrier until all of them have
// arrived, so all of them are provably past the read before any reaches the
// swap. The barrier alone does not discriminate, which is why the width and
// the repetition are both here and both calibrated rather than guessed: the
// window a read-then-write leaves open is two instructions wide, and two
// callers released once simply serialize. Eight callers across four thousand
// rounds catch the read-then-write form on every run, in about a fifth of a
// second. Each round gets its own router, so each observes the transition
// afresh; the correct implementation can never record twice, so nothing here
// is probabilistic in the direction that would flake.
func TestTwoConcurrentProbesObservingOneTransitionRecordItOnce(t *testing.T) {
	buffer := captureRecords(t)
	captureUnstructuredOutput(t)

	const callers = 8
	for round := 0; round < 4000; round++ {
		arrived := make(chan struct{}, callers)
		release := make(chan struct{})
		probe := func(context.Context) error {
			arrived <- struct{}{}
			<-release
			return errProbeDependencyDown
		}
		router := newProbeAccessRouter(t, newChecker(readinessCheck{name: dependencyDatabase, probe: probe}))

		var wg sync.WaitGroup
		for i := 0; i < callers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, readyRoutePath, nil))
			}()
		}

		for i := 0; i < callers; i++ {
			<-arrived
		}
		close(release)
		wg.Wait()

		if records := recordsWithComponent(t, buffer, componentReadinessProbe); len(records) != 1 {
			t.Fatalf("round %d: %d concurrent probes observing one transition yielded %d records, want 1: %s",
				round, callers, len(records), buffer.String())
		}
		buffer.Reset()
	}
}
