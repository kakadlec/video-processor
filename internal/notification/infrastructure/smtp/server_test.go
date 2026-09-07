package smtp

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"testing"
)

// fakeRelay is a scripted SMTP listener: enough of the protocol for one
// message, with every verdict overridable so a test can choose where the
// conversation fails.
//
// A real relay is not substitutable here. What this package has to be shown
// doing — bounding a conversation that never answers, refusing to
// authenticate without encryption, classifying a reply code without
// recording its text — are all properties of how it reacts to a server, so
// the server is the thing under control.
type fakeRelay struct {
	// greet, rcpt and data are the replies sent at each step. An empty
	// string means the default success reply.
	greet string
	rcpt  string
	data  string

	// silent completes the TCP handshake and then never sends a greeting,
	// which is the case the connection deadline exists for: the dialer has
	// already returned by then, so nothing else bounds the read.
	silent bool

	// advertiseSTARTTLS makes the EHLO response offer the extension. The
	// fake never actually negotiates TLS — a test that reaches StartTLS is
	// testing that this client tried, not that a handshake succeeded.
	advertiseSTARTTLS bool

	addr     string
	listener net.Listener

	mu       sync.Mutex
	received []string
	envelope []string
}

func startFakeRelay(t *testing.T, relay *fakeRelay) *fakeRelay {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("unexpected error listening: %v", err)
	}
	relay.listener = listener
	relay.addr = listener.Addr().String()
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go relay.serve(conn)
		}
	}()

	return relay
}

func (r *fakeRelay) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	if r.silent {
		// Held open without a byte written, so the client is blocked on the
		// greeting read. Closing when the client goes away is what ends it.
		buffer := make([]byte, 1)
		_, _ = conn.Read(buffer)
		return
	}

	reader := bufio.NewReader(conn)
	write := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }

	write(orDefault(r.greet, "220 fake relay ready"))

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.TrimSpace(line))
		r.record(strings.TrimSpace(line))

		switch {
		case strings.HasPrefix(command, "EHLO"), strings.HasPrefix(command, "HELO"):
			if r.advertiseSTARTTLS {
				write("250-fake relay")
				write("250 STARTTLS")
				continue
			}
			write("250 fake relay")
		case strings.HasPrefix(command, "STARTTLS"):
			// Accepted at the protocol level and then abandoned: the client
			// under test will fail its handshake, which is the observable
			// this fake exists to produce.
			write("220 ready to start TLS")
			return
		case strings.HasPrefix(command, "AUTH"):
			write("235 authenticated")
		case strings.HasPrefix(command, "MAIL FROM"), strings.HasPrefix(command, "RCPT TO"):
			if strings.HasPrefix(command, "RCPT TO") {
				write(orDefault(r.rcpt, "250 ok"))
				continue
			}
			write("250 ok")
		case strings.HasPrefix(command, "DATA"):
			write("354 send it")
			var body strings.Builder
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimSpace(dataLine) == "." {
					break
				}
				body.WriteString(dataLine)
			}
			r.recordEnvelope(body.String())
			write(orDefault(r.data, "250 queued"))
		case strings.HasPrefix(command, "QUIT"):
			write("221 bye")
			return
		default:
			write("250 ok")
		}
	}
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func (r *fakeRelay) record(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.received = append(r.received, line)
}

func (r *fakeRelay) recordEnvelope(body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.envelope = append(r.envelope, body)
}

// commands returns every command line the relay saw, joined, so a test can
// assert on the conversation without locking.
func (r *fakeRelay) commands() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.received, "\n")
}

// message returns the one message body the relay accepted, or "" if none.
func (r *fakeRelay) message() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.envelope) == 0 {
		return ""
	}
	return r.envelope[0]
}
