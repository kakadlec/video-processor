package main

import "log/slog"

// The components this process's records are attributed to. The prefix a
// message used to carry becomes a field, so an identifier is never a
// substring of the message; the video: worker: segments are dropped, because
// both are derivable from the service name the logger already binds.
const (
	componentProcessStartup  = "process_startup"
	componentProcessShutdown = "process_shutdown"
	componentJobConsumer     = "job_consumer"
	componentJobDispatch     = "job_dispatch"
	componentJobCleanup      = "job_cleanup"
	componentTerminalRelay   = "terminal_relay"
	componentRecoverySweeper = "recovery_sweeper"
)

// logger returns the process logger with component bound. It resolves the
// default per call rather than binding one at package scope: main installs the
// process logger long after this package's variables are initialized.
func logger(component string) *slog.Logger {
	return slog.Default().With(slog.String("component", component))
}
