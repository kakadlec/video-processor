package main

import "log/slog"

// The components this service's records are attributed to. The prefix a
// message used to carry becomes a field, so an identifier is never a
// substring of the message.
const (
	componentProcessStartup    = "process_startup"
	componentProcessShutdown   = "process_shutdown"
	componentHTTPServer        = "http_server"
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
