package logging

// The canonical service identifiers, fixed here so the spec's scenarios, the
// output-shape test, and the filters an operator writes cannot drift apart.
// These are the binary names, which are also the deployment's own service
// names; they carry no cmd/ prefix, which would name where the source lives
// rather than what is running.
const (
	ServiceIdentityAPI     = "identity-api"
	ServiceVideoAPI        = "video-api"
	ServiceNotificationAPI = "notification-api"
	ServiceWorker          = "worker"
	ServiceNotifier        = "notifier"
)

// Services returns the canonical set, in the order the roots are documented.
func Services() []string {
	return []string{
		ServiceIdentityAPI,
		ServiceVideoAPI,
		ServiceNotificationAPI,
		ServiceWorker,
		ServiceNotifier,
	}
}
