package main

import "log/slog"

// The components this service's records are attributed to. The prefix a
// message used to carry becomes a field, so an identifier is never a
// substring of the message.
const (
	componentProcessStartup  = "process_startup"
	componentProcessShutdown = "process_shutdown"
	componentHTTPServer      = "http_server"
	componentOutboxRelay     = "outbox_relay"
	componentRateLimit       = "rate_limit"
	componentVideoUpload     = "video_upload"
	componentResultDownload  = "result_download"
	componentResultListing   = "result_listing"
	componentJobCreation     = "job_creation"
	componentJobStatus       = "job_status"
	componentJobListing      = "job_listing"
)

// logger returns the process logger with component bound. It resolves the
// default per call rather than binding one at package scope: main installs the
// process logger long after this package's variables are initialized.
func logger(component string) *slog.Logger {
	return slog.Default().With(slog.String("component", component))
}

// errorText renders err for the error attribute. Several of the idempotency
// cleanup paths report a refusal that carries no error at all — the call
// returned false with a nil error — so calling err.Error() there panics.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
