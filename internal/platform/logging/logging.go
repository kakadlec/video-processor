// Package logging builds the structured logger each composition root installs
// as its process-wide default. It holds handler construction, severity
// parsing, and the service/instance identity binding, and it imports no HTTP
// framework and no bounded context.
package logging

import (
	"io"
	"log/slog"
	"os"
	"strconv"
	"sync/atomic"
)

// The identity fields every record carries, named once so the roots, the
// tests, and the filters an operator writes cannot drift apart.
const (
	FieldService  = "service"
	FieldInstance = "instance"
)

// unknownHost stands in when the operating system cannot name the host. An
// empty segment would read as "every process is one instance", which is the
// opposite of what the field exists for.
const unknownHost = "unknown-host"

// constructions breaks the tie between two loggers built inside one process.
// The hostname separates machines and the process identifier separates
// processes on one machine; neither separates two constructions, which is the
// collision a test can actually exercise.
var constructions atomic.Uint64

// New builds the process logger: JSON records on standard output, at the given
// severity, carrying this process's service and instance identity.
func New(service string, level slog.Level) *slog.Logger {
	return newLogger(os.Stdout, service, level, os.Hostname)
}

// NewBootstrap builds a logger at the default severity carrying the same
// identity fields as New. It exists for the one failure that happens before a
// configured logger can exist — a severity value that will not parse — so no
// composition root needs an unstructured path to report it.
func NewBootstrap(service string) *slog.Logger {
	return New(service, DefaultLevel)
}

func newLogger(w io.Writer, service string, level slog.Level, hostname func() (string, error)) *slog.Logger {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(handler).With(
		slog.String(FieldService, service),
		slog.String(FieldInstance, instanceID(hostname)),
	)
}

// instanceID is resolved here, once per construction, and never per record.
func instanceID(hostname func() (string, error)) string {
	host, err := hostname()
	if err != nil || host == "" {
		host = unknownHost
	}
	return host + "-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(constructions.Add(1), 10)
}
