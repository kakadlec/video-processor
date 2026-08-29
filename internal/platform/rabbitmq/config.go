package rabbitmq

import (
	"errors"
	"os"
)

// ErrURLRequired is returned when no broker URL is configured. Startup must
// fail clearly rather than silently running without a message broker.
var ErrURLRequired = errors.New("platform/rabbitmq: RABBITMQ_URL environment variable is required")

// Config holds AMQP connection configuration.
//
// One URL field rather than a decomposed host/port/user/vhost set: the URI is
// AMQP's own addressing form, carrying the scheme that selects TLS, the
// credentials, and the virtual host. Splitting those apart would only
// reassemble a URI at startup from parts the operator already had in one.
type Config struct {
	URL string
}

// LoadConfigFromEnv reads the broker URL from the environment.
func LoadConfigFromEnv() (Config, error) {
	url := os.Getenv("RABBITMQ_URL")
	if url == "" {
		return Config{}, ErrURLRequired
	}
	return Config{URL: url}, nil
}
