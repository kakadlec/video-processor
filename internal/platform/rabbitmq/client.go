package rabbitmq

import (
	"errors"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Open dials the broker and completes the AMQP handshake, returning a live
// connection or an error.
//
// This connects, unlike redis.Open and storage.Open, which construct a client
// without touching the network and let unreachability surface on first use.
// AMQP has no lazy client — a connection is only usable after a handshake —
// so there is no object to hand back that would mean anything before
// connecting. Do not wrap this in a deferred-connect shim to match the other
// two adapters: it would move a diagnosable startup failure to an arbitrary
// later point, and the symmetry it buys is cosmetic.
func Open(cfg Config) (*amqp.Connection, error) {
	conn, err := amqp.Dial(cfg.URL)
	if err != nil {
		// cfg.URL is deliberately absent from this message: an AMQP URI
		// carries both the username and the password in its userinfo
		// component, and this error reaches logs.
		return nil, fmt.Errorf("platform/rabbitmq: dial broker: %w", err)
	}
	return conn, nil
}

// Ping performs a round-trip health check by opening a channel and closing it.
//
// (*amqp.Connection).IsClosed() is cheaper and is the wrong primitive: it
// reports whether this process has already observed a close, which is stale
// for exactly the failure a health check exists to catch — a broker that has
// stopped answering without the connection having been torn down yet.
// channel.open/channel.close is a synchronous exchange the broker must answer.
func Ping(conn *amqp.Connection) error {
	if conn == nil {
		return errors.New("platform/rabbitmq: ping: nil connection")
	}

	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("platform/rabbitmq: ping: %w", err)
	}
	if err := ch.Close(); err != nil {
		return fmt.Errorf("platform/rabbitmq: ping: close channel: %w", err)
	}
	return nil
}

// Close releases conn and its underlying socket.
func Close(conn *amqp.Connection) error {
	if conn == nil {
		return nil
	}
	return conn.Close()
}
