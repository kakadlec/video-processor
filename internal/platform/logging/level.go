package logging

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// LevelEnvVar names the optional per-process severity variable. Exported so
// the five composition roots agree on one name rather than on five copies of a
// literal; reading it is the root's job, not this package's.
const LevelEnvVar = "LOG_LEVEL"

// DefaultLevel is the severity a process runs at when LevelEnvVar is absent.
const DefaultLevel = slog.LevelInfo

// ErrUnknownLevel reports a configured severity this package will not parse.
var ErrUnknownLevel = errors.New("logging: unrecognized severity")

// ParseLevel maps a configured severity to a level. An absent value yields the
// default; an unrecognized one is refused rather than silently downgraded to
// it, because a process running at a severity nobody asked for is a worse
// outcome than one that refuses to start.
func ParseLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return DefaultLevel, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return DefaultLevel, fmt.Errorf("%w: %q", ErrUnknownLevel, raw)
	}
}
