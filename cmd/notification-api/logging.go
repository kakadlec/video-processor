package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

// The components this service's records are attributed to. The prefix a
// message used to carry becomes a field, so an identifier is never a
// substring of the message.
const (
	componentProcessStartup    = "process_startup"
	componentProcessShutdown   = "process_shutdown"
	componentHTTPServer        = "http_server"
	componentHTTPAccess        = "http_access"
	componentHTTPRecovery      = "http_recovery"
	componentRateLimit         = "rate_limit"
	componentPreferenceListing = "preference_listing"
	componentPreferenceWrite   = "preference_write"
)

// logger returns the process logger with component bound. It resolves the
// default per call rather than binding one at package scope: main installs the
// process logger long after this package's variables are initialized.
func logger(component string) *slog.Logger {
	return slog.Default().With(slog.String("component", component))
}

// maxRecordedPathLength bounds the one caller-supplied string an access record
// may carry. Only an unmatched request records its path, and there the value is
// arbitrary text of arbitrary length.
const maxRecordedPathLength = 256

// unrecognizedMethod replaces a request method that is not one of the
// recognized ones. The method is caller-supplied on the request line and
// reaches the record before authentication and before the rate limiter, so
// bounding the path while recording the method beside it verbatim would leave
// the record unbounded through the field next to the one that was bounded.
const unrecognizedMethod = "UNRECOGNIZED"

var recognizedMethods = map[string]bool{
	http.MethodGet:     true,
	http.MethodHead:    true,
	http.MethodPost:    true,
	http.MethodPut:     true,
	http.MethodPatch:   true,
	http.MethodDelete:  true,
	http.MethodConnect: true,
	http.MethodOptions: true,
	http.MethodTrace:   true,
}

// accessRecordExempt reports whether a request that matched this route
// template is exempt from the access record. It is a closed list of exactly
// the two probe route templates, and there is deliberately no mechanism — no
// middleware option, no configurable exclusion list, no per-route opt-out — by
// which any further route can join it: a general mechanism would let a later
// route stop being recorded without that ever being reviewed as a change to
// this capability, and the value of the rule it excepts is that it has no
// escape hatch.
//
// What the two excluded records would contain is the justification. A liveness
// probe consults nothing, so its record varies in no field; a readiness
// probe's verdict does vary, but the informative event is a change of verdict,
// which is recorded at the moment it happens, at a severity that reflects it,
// naming the dependency that failed — which an access record cannot do at all.
//
// It is keyed on the matched route template rather than on the request path:
// a request for one of these paths that matched no route — another method,
// say — is a request like any other and is recorded like one.
func accessRecordExempt(route string) bool {
	return route == healthRoutePath || route == readyRoutePath
}

// accessLogMiddleware emits one record per request through the process logger,
// replacing gin's own access log. It is mounted on the engine rather than on a
// group: it has to see the requests that matched no route, which is also the
// only case in which it records a path.
func accessLogMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		// After the chain, not before it: what is exempt is the record, not
		// the request. The recovery middleware and the handler both sit
		// behind this call, so returning ahead of it would answer every probe
		// from the middleware itself.
		if accessRecordExempt(c.FullPath()) {
			return
		}

		record := requestLocation(logger(componentHTTPAccess), c).With(
			slog.String("method", recordedMethod(c.Request.Method)),
			slog.Int("status", c.Writer.Status()),
			slog.Duration("duration", time.Since(start)),
			slog.Int("size", responseSize(c)),
		)
		// The record is written after the chain has run, so the subject
		// requireBearerAuth established is available here even though the
		// method beside it reached this middleware before any of it.
		if userID, ok := authenticatedUserID(c); ok {
			record = record.With(slog.String("user_id", userID.String()))
		}
		record.Info("an HTTP request was served")
	}
}

// recoveryMiddleware records every recovered panic and preserves the response
// each branch produced before, replacing gin's own recovery. It is written here
// rather than delegated to gin.CustomRecovery, which builds a log.Logger over
// gin.DefaultErrorWriter and prints an ANSI-coloured block to it before calling
// the supplied handler; passing a nil writer silences that block but skips the
// handler entirely on the broken-pipe branch, leaving that class of panic
// recorded nowhere.
func recoveryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}

			// gin's own branch test, kept: a panic raised because the client
			// is already gone must not try to write a status to a dead
			// connection.
			err, isError := recovered.(error)
			brokenPipe := isError && (errors.Is(err, syscall.EPIPE) ||
				errors.Is(err, syscall.ECONNRESET) ||
				errors.Is(err, http.ErrAbortHandler))

			requestLocation(logger(componentHTTPRecovery), c).Error("a handler panicked",
				slog.String("method", recordedMethod(c.Request.Method)),
				slog.Bool("broken_pipe", brokenPipe),
				slog.String("panic", fmt.Sprint(recovered)),
				slog.String("stack", string(debug.Stack())))

			if brokenPipe {
				_ = c.Error(err)
				c.Abort()
				return
			}
			c.AbortWithStatus(http.StatusInternalServerError)
		}()

		c.Next()
	}
}

// requestLocation binds the matched route, or the bounded request path when
// nothing matched — never both. The route template is bounded by the router's
// own definition; for a matched request the path adds only the parameter
// values, every one of which the handler that used it already records.
func requestLocation(base *slog.Logger, c *gin.Context) *slog.Logger {
	if route := c.FullPath(); route != "" {
		return base.With(slog.String("route", route))
	}
	return base.With(slog.String("path", boundedPath(c.Request.URL.Path)))
}

// boundedPath truncates a path to the fixed bound. It is given
// c.Request.URL.Path rather than RequestURI, so the query string this record
// excludes cannot re-enter through it.
func boundedPath(path string) string {
	if len(path) <= maxRecordedPathLength {
		return path
	}
	// URL.Path is percent-decoded, so cutting on a byte boundary can split a
	// multi-byte rune; the fragment is dropped rather than written into the
	// record as an invalid sequence.
	return strings.ToValidUTF8(path[:maxRecordedPathLength], "")
}

func recordedMethod(method string) string {
	if recognizedMethods[method] {
		return method
	}
	return unrecognizedMethod
}

// responseSize reports the bytes written. gin's writer reports -1 until
// something is, which is the same nothing as zero for this record.
func responseSize(c *gin.Context) int {
	if size := c.Writer.Size(); size > 0 {
		return size
	}
	return 0
}
