package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// refusingValue declines to be marshalled, the way
// internal/notification/domain.Secret does. Declared here rather than imported:
// this package may not import a bounded context, and the behaviour being
// pinned is the handler's, not that type's.
type refusingValue struct{}

func (refusingValue) MarshalJSON() ([]byte, error) {
	return nil, errors.New("refused")
}

type carrier struct {
	Sibling  string
	Refusing refusingValue
}

// TestAMarshalFailureCostsTheAttributeAndNotTheRecord pins the JSONHandler
// behaviour the design's decision 2 rests on. It was measured rather than
// assumed — a Go release could change it, and the rationale would silently
// stop being true — so the claim is asserted in all four of its parts:
// the attribute becomes an error marker, the siblings nested inside it are
// lost, the record is still emitted, and logging continues afterwards.
//
// slog.Any is used deliberately, in a test file the source walk skips: it is
// the call form the walk forbids in non-test sources, and reaching this
// failure requires it.
func TestAMarshalFailureCostsTheAttributeAndNotTheRecord(t *testing.T) {
	var out bytes.Buffer
	logger := newLogger(&out, ServiceNotifier, slog.LevelInfo, fixedHostname("host-a"))

	logger.Info("carrying a value the encoder refuses",
		slog.Any("carrier", carrier{Sibling: "sibling-value"}),
		slog.String("kept", "kept-value"),
	)
	logger.Info("the next record", slog.String("kept", "still-here"))

	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("expected two records, got %d: %s", len(lines), out.Bytes())
	}

	first := map[string]any{}
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatalf("the record carrying the refused value is not JSON: %v (%s)", err, lines[0])
	}

	// The record is emitted, in full, rather than discarded.
	if got := first[slog.MessageKey]; got != "carrying a value the encoder refuses" {
		t.Errorf("message = %v, want the record emitted despite the refusal", got)
	}
	if got := first[FieldService]; got != ServiceNotifier {
		t.Errorf("service = %v, want the identity fields to survive the refusal", got)
	}

	// The attribute becomes an error marker.
	marker, ok := first["carrier"].(string)
	if !ok || !strings.Contains(marker, "!ERROR") {
		t.Errorf("carrier = %v, want an error marker in the attribute's place", first["carrier"])
	}

	// Its nested siblings are lost with it — the diagnostic cost decision 2
	// accepts, and the reason the no-arbitrary-value rule exists rather than
	// a redaction rule.
	if strings.Contains(string(lines[0]), "sibling-value") {
		t.Errorf("the sibling nested in the refused attribute survived: %s", lines[0])
	}

	// A top-level sibling attribute does survive.
	if got := first["kept"]; got != "kept-value" {
		t.Errorf("kept = %v, want the attributes beside the refused one to survive", got)
	}

	// Logging continues.
	second := map[string]any{}
	if err := json.Unmarshal(lines[1], &second); err != nil {
		t.Fatalf("the record after the refusal is not JSON: %v (%s)", err, lines[1])
	}
	if got := second["kept"]; got != "still-here" {
		t.Errorf("kept = %v, want logging to continue normally after a refusal", got)
	}
}
