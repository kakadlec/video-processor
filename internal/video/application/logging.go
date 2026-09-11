package application

import "log/slog"

// The components this package's records are attributed to. The prefix a
// message used to carry becomes a field, so an identifier is never a
// substring of the message.
const (
	componentJobProcessing = "job_processing"
	componentResultListing = "result_listing"
)

// logger returns the process logger with component bound. It resolves the
// default per call rather than binding one at package scope: the composition
// root installs the process logger inside main, long after this package's
// variables are initialized.
func logger(component string) *slog.Logger {
	return slog.Default().With(slog.String("component", component))
}
