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
	"syscall"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	notificationdomain "video-processor/internal/notification/domain"
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

func onlyRecord(t *testing.T, buffer *bytes.Buffer, component string) map[string]any {
	t.Helper()

	var found []map[string]any
	for _, record := range decodeRecords(t, buffer) {
		if record["component"] == component {
			found = append(found, record)
		}
	}
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

// TestTheAccessRecordNamesTheAuthenticatedSubject holds the "where one exists"
// half of the field set, together with the absence that is its other half. The
// subject is available because the record is written after the chain has run,
// even though the method beside it reached the middleware before any of it.
func TestTheAccessRecordNamesTheAuthenticatedSubject(t *testing.T) {
	const subject = "3fa85f64-5717-4562-b3fc-2c963f66afa6"

	t.Run("an authenticated request", func(t *testing.T) {
		buffer := captureRecords(t)
		captureUnstructuredOutput(t)

		userID := testUserID(t, subject)
		serveLoggingProbe(newLoggingTestRouter(func(c *gin.Context) {
			c.Set(authenticatedUserIDKey, userID)
			c.Next()
		}), http.MethodGet, "/probe/one")

		requireField(t, onlyRecord(t, buffer, componentHTTPAccess), "user_id", subject)
	})

	t.Run("an unauthenticated request", func(t *testing.T) {
		buffer := captureRecords(t)
		captureUnstructuredOutput(t)

		serveLoggingProbe(newLoggingTestRouter(), http.MethodGet, "/probe/one")

		requireNoField(t, onlyRecord(t, buffer, componentHTTPAccess), "user_id")
	})
}

// TestTheServiceRouterMountsTheAccessLog drives the real setupRouter rather
// than a reconstruction of it. Everything above builds its own engine, so a
// root that stopped mounting the pair would leave every one of those tests
// green — gin.Default() still compiles, and an unused middleware is not an
// error.
func TestTheServiceRouterMountsTheAccessLog(t *testing.T) {
	buffer := captureRecords(t)
	unstructured := captureUnstructuredOutput(t)

	auth, _ := newTestAuthenticatorWithTokens(t)
	recorder := httptest.NewRecorder()
	setupRouter(auth, newTestNotificationModuleWithPolicy(newInMemoryPreferenceRepository(), notificationdomain.NewDestinationPolicy(false)), alwaysAllowRateLimiter{}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/no-such-route", nil))

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
