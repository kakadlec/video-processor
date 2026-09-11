package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func fixedHostname(host string) func() (string, error) {
	return func() (string, error) { return host, nil }
}

func decodeOne(t *testing.T, out []byte) map[string]any {
	t.Helper()

	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("expected exactly one record, got %d: %s", len(lines), out)
	}
	record := map[string]any{}
	if err := json.Unmarshal(lines[0], &record); err != nil {
		t.Fatalf("record is not JSON: %v (%s)", err, lines[0])
	}
	return record
}

// captureStdout runs fn with standard output redirected and returns what was
// written to it. Not parallel-safe: os.Stdout is process-wide state, and the
// handler captures its writer at construction, so the logger under test must
// be built inside fn.
func captureStdout(t *testing.T, fn func()) []byte {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("open pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = saved }()

	fn()

	os.Stdout = saved
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return out
}

// TestTheRecordCarriesTheAgreedShape pins the shape the rest of the change
// assumes, once, rather than in every package that logs.
func TestTheRecordCarriesTheAgreedShape(t *testing.T) {
	var out bytes.Buffer
	logger := newLogger(&out, ServiceWorker, slog.LevelInfo, fixedHostname("host-a"))

	logger.Info("job accepted", slog.String("job_id", "job-1"))

	record := decodeOne(t, out.Bytes())
	for _, field := range []string{slog.TimeKey, slog.LevelKey, slog.MessageKey, FieldService, FieldInstance} {
		if _, ok := record[field]; !ok {
			t.Errorf("record carries no %q field: %v", field, record)
		}
	}
	if got := record[slog.MessageKey]; got != "job accepted" {
		t.Errorf("message = %v, want %q", got, "job accepted")
	}
	if got := record[slog.LevelKey]; got != "INFO" {
		t.Errorf("level = %v, want INFO", got)
	}
	if got := record[FieldService]; got != ServiceWorker {
		t.Errorf("service = %v, want %q", got, ServiceWorker)
	}
	if got, ok := record[FieldInstance].(string); !ok || !strings.HasPrefix(got, "host-a-") {
		t.Errorf("instance = %v, want a value derived from the hostname", record[FieldInstance])
	}
	stamp, ok := record[slog.TimeKey].(string)
	if !ok {
		t.Fatalf("timestamp is not a string: %v", record[slog.TimeKey])
	}
	if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
		t.Errorf("timestamp %q is not RFC3339: %v", stamp, err)
	}
}

func TestTheThresholdSuppressesLowerSeverities(t *testing.T) {
	var out bytes.Buffer
	logger := newLogger(&out, ServiceWorker, slog.LevelWarn, fixedHostname("host-a"))

	logger.Info("suppressed")
	logger.Warn("emitted")

	record := decodeOne(t, out.Bytes())
	if got := record[slog.MessageKey]; got != "emitted" {
		t.Errorf("message = %v, want the record at or above the threshold", got)
	}
}

// TestNewWritesToStandardOutput pins the destination the spec states as a
// requirement rather than as an implementation detail.
func TestNewWritesToStandardOutput(t *testing.T) {
	out := captureStdout(t, func() {
		New(ServiceVideoAPI, slog.LevelInfo).Info("on stdout")
	})

	record := decodeOne(t, out)
	if got := record[slog.MessageKey]; got != "on stdout" {
		t.Errorf("message = %v, want the record written to standard output", got)
	}
	if got := record[FieldService]; got != ServiceVideoAPI {
		t.Errorf("service = %v, want %q", got, ServiceVideoAPI)
	}
}

// TestNewBootstrapCarriesTheDefaultSeverityAndTheIdentityFields covers the
// constructor a root reports an unparseable LOG_LEVEL through.
func TestNewBootstrapCarriesTheDefaultSeverityAndTheIdentityFields(t *testing.T) {
	out := captureStdout(t, func() {
		logger := NewBootstrap(ServiceNotifier)
		logger.Debug("below the default severity")
		logger.Error("unrecognized severity", slog.String("value", "loud"))
	})

	record := decodeOne(t, out)
	if got := record[slog.LevelKey]; got != "ERROR" {
		t.Errorf("level = %v, want ERROR", got)
	}
	if got := record[FieldService]; got != ServiceNotifier {
		t.Errorf("service = %v, want %q", got, ServiceNotifier)
	}
	if got, ok := record[FieldInstance].(string); !ok || got == "" {
		t.Errorf("instance = %v, want the same identity a normal logger carries", record[FieldInstance])
	}
}

func TestParseLevelDefaultsToInformationalWhenAbsent(t *testing.T) {
	level, err := ParseLevel("")
	if err != nil {
		t.Fatalf("ParseLevel(%q) = %v, want no error", "", err)
	}
	if level != slog.LevelInfo {
		t.Errorf("ParseLevel(%q) = %v, want %v", "", level, slog.LevelInfo)
	}
}

func TestParseLevelRefusesAnUnparseableValue(t *testing.T) {
	_, err := ParseLevel("loud")
	if err == nil {
		t.Fatal("ParseLevel(\"loud\") returned no error; an unparseable severity must be refused rather than replaced by the default")
	}
	if !errors.Is(err, ErrUnknownLevel) {
		t.Errorf("error %v does not wrap ErrUnknownLevel", err)
	}
	if !strings.Contains(err.Error(), "loud") {
		t.Errorf("error %q does not name the offending value", err)
	}
}

func TestParseLevelRecognizesEverySeverity(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"INFO":    slog.LevelInfo,
		" warn ":  slog.LevelWarn,
		"warning": slog.LevelWarn,
		"Error":   slog.LevelError,
	}
	for raw, want := range cases {
		got, err := ParseLevel(raw)
		if err != nil {
			t.Errorf("ParseLevel(%q) = %v, want no error", raw, err)
			continue
		}
		if got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", raw, got, want)
		}
	}
}

// TestTheInstanceSurvivesAnUnavailableHostname covers the fallback: a field
// that silently becomes empty is worse than an absent one, because it reads as
// "all one instance".
func TestTheInstanceSurvivesAnUnavailableHostname(t *testing.T) {
	failing := func() (string, error) { return "", errors.New("no hostname") }

	var first, second bytes.Buffer
	newLogger(&first, ServiceWorker, slog.LevelInfo, failing).Info("one")
	newLogger(&second, ServiceWorker, slog.LevelInfo, failing).Info("two")

	a, _ := decodeOne(t, first.Bytes())[FieldInstance].(string)
	b, _ := decodeOne(t, second.Bytes())[FieldInstance].(string)
	if a == "" || b == "" {
		t.Fatalf("instance is empty with no hostname available: %q, %q", a, b)
	}
	if !strings.HasPrefix(a, unknownHost) {
		t.Errorf("instance %q does not say the hostname was unavailable", a)
	}
	if a == b {
		t.Errorf("two loggers share the instance value %q with no hostname available", a)
	}
}

// TestTwoLoggersOnOneHostCarryDifferentInstances covers the collision the
// field exists to prevent: two processes of one binary started directly on one
// machine share a hostname.
func TestTwoLoggersOnOneHostCarryDifferentInstances(t *testing.T) {
	var first, second bytes.Buffer
	newLogger(&first, ServiceWorker, slog.LevelInfo, fixedHostname("one-host")).Info("one")
	newLogger(&second, ServiceWorker, slog.LevelInfo, fixedHostname("one-host")).Info("two")

	a, _ := decodeOne(t, first.Bytes())[FieldInstance].(string)
	b, _ := decodeOne(t, second.Bytes())[FieldInstance].(string)
	if a == b {
		t.Errorf("two loggers with the same service and hostname carry the same instance %q", a)
	}
}

// TestTheInstanceIsResolvedOnceRatherThanPerRecord holds the other half of the
// identity rule: the hostname lookup is a startup cost, not a per-record one.
func TestTheInstanceIsResolvedOnceRatherThanPerRecord(t *testing.T) {
	calls := 0
	counting := func() (string, error) {
		calls++
		return "host-a", nil
	}

	var out bytes.Buffer
	logger := newLogger(&out, ServiceWorker, slog.LevelInfo, counting)
	logger.Info("one")
	logger.Info("two")
	logger.Info("three")

	if calls != 1 {
		t.Errorf("hostname resolved %d times, want once at construction", calls)
	}
}
