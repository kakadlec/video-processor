// Package smtp delivers a terminal event to the address its owner
// registered, as one plain-text message through the relay this deployment
// configures. It is the Notification context's second outbound adapter, and
// the only place on the delivery path that names an SMTP client.
//
// It reads no signing secret. Signing is the webhook channel's mechanism,
// and a message sent from here carries no signature at all; the assertion
// that nothing here reaches for one is a test rather than a convention,
// because a value that was never read cannot be observed at runtime.
package smtp

import (
	"fmt"
	"net"
	"os"

	"video-processor/internal/notification/domain"
)

// The relay's configuration. Required at cmd/notifier startup, all of it —
// a notifier that cannot send is a notifier whose e-mail preferences are
// stored and silently never honoured, which is the outcome the closed
// channel set exists to prevent.
//
// cmd/notification-api reads none of this. It validates an address; it
// sends nothing.
const (
	EnvAddr     = "NOTIFICATION_SMTP_ADDR"
	EnvFrom     = "NOTIFICATION_SMTP_FROM"
	EnvUsername = "NOTIFICATION_SMTP_USERNAME"
	EnvPassword = "NOTIFICATION_SMTP_PASSWORD"
)

// Config is the relay this process sends through.
//
// Username and Password are optional together and meaningless apart, which
// is why loading refuses one without the other rather than silently
// authenticating with an empty half.
type Config struct {
	Addr     string
	From     string
	Username string
	Password string
}

// Authenticates reports whether credentials were configured. It is what the
// require-encryption rule keys on: without credentials there is nothing to
// protect, and a local stack with no TLS works unchanged.
func (c Config) Authenticates() bool {
	return c.Username != ""
}

// Host returns the relay's hostname, which is both the TLS server name and
// the realm PLAIN authentication is scoped to.
func (c Config) Host() string {
	host, _, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return c.Addr
	}
	return host
}

// LoadConfigFromEnv reads and validates the relay's configuration.
//
// The sender is validated by the same rule a stored recipient is, through
// the domain's own constructor, rather than by a looser check of its own.
// It is operator-supplied rather than user-supplied, but it lands in a
// message header exactly as the recipient does, so the value that must not
// contain a line break is both of them.
func LoadConfigFromEnv() (Config, error) {
	config := Config{
		Addr:     os.Getenv(EnvAddr),
		From:     os.Getenv(EnvFrom),
		Username: os.Getenv(EnvUsername),
		Password: os.Getenv(EnvPassword),
	}

	if config.Addr == "" {
		return Config{}, fmt.Errorf("notification: %s is required", EnvAddr)
	}
	// Both halves checked, not only that the value splits. SplitHostPort
	// accepts "mail:" and ":1025" — each names no endpoint, and each would
	// pass startup and then fail every delivery as a transport error, which
	// is exactly the late failure validating configuration here exists to
	// prevent.
	host, port, err := net.SplitHostPort(config.Addr)
	if err != nil || host == "" || port == "" {
		return Config{}, fmt.Errorf("notification: %s must be host:port, got %q", EnvAddr, config.Addr)
	}
	if config.From == "" {
		return Config{}, fmt.Errorf("notification: %s is required", EnvFrom)
	}

	channel, err := domain.ParseChannel(domain.ChannelEmail)
	if err != nil {
		return Config{}, fmt.Errorf("notification: %w", err)
	}
	if _, err := domain.NewDestinationFor(channel, config.From); err != nil {
		return Config{}, fmt.Errorf("notification: %s must be a single e-mail address", EnvFrom)
	}

	// Refused rather than half-honoured: authenticating with an empty
	// password is a different request from not authenticating, and a
	// deployment that meant one and got the other would find out from the
	// relay rather than from startup.
	if (config.Username == "") != (config.Password == "") {
		return Config{}, fmt.Errorf("notification: %s and %s must be set together", EnvUsername, EnvPassword)
	}

	return config, nil
}
