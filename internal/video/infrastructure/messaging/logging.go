package messaging

import "log/slog"

// The components this package's records are attributed to, and the closed
// set of dial-loop lifecycle values the relay and the consumer share. The
// prefix a message used to carry becomes a field, so a reader filters on
// phase rather than on a message substring.
const (
	componentOutboxRelay = "outbox_relay"
	componentJobConsumer = "job_consumer"
	phaseStarted         = "started"
	phaseConnected       = "connected"
	phaseConnectionLost  = "connection_lost"
	phaseStopped         = "stopped"
)

// logger returns the process logger with component bound. It resolves the
// default per call rather than binding one at package scope: the composition
// root installs the process logger inside main, long after this package's
// variables are initialized.
func logger(component string) *slog.Logger {
	return slog.Default().With(slog.String("component", component))
}
