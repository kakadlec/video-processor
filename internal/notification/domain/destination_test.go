package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/notification/domain"
)

func TestNewDestination(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr error
	}{
		{"https accepted", "https://example.test/hooks/1", nil},
		{"http accepted for the TLS-less compose stack", "http://app:8080/hooks", nil},
		{"host and port accepted", "https://example.test:8443/hooks", nil},
		{"query string accepted", "https://example.test/hooks?token=abc", nil},
		{"empty rejected", "", domain.ErrInvalidDestination},
		{"relative path rejected", "/hooks/1", domain.ErrInvalidDestination},
		{"schemeless host rejected", "example.test/hooks", domain.ErrInvalidDestination},
		{"ftp rejected", "ftp://example.test/hooks", domain.ErrInvalidDestination},
		{"file rejected", "file:///etc/passwd", domain.ErrInvalidDestination},
		{"javascript rejected", "javascript:alert(1)", domain.ErrInvalidDestination},
		{"scheme with no host rejected", "http:///hooks", domain.ErrInvalidDestination},
		{"bare scheme rejected", "https://", domain.ErrInvalidDestination},
		{"port-only authority rejected", "http://:8080/hooks", domain.ErrInvalidDestination},
		{"port-only https authority rejected", "https://:443", domain.ErrInvalidDestination},
		{"unparseable rejected", "http://exa mple.test/%zz", domain.ErrInvalidDestination},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			destination, err := domain.NewDestination(tt.raw)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewDestination(%q) error = %v, want %v", tt.raw, err, tt.wantErr)
			}
			if tt.wantErr != nil {
				if !destination.IsZero() {
					t.Fatalf("NewDestination(%q) returned %q on a rejected value", tt.raw, destination)
				}
				return
			}
			if destination.String() != tt.raw {
				t.Fatalf("NewDestination(%q).String() = %q", tt.raw, destination.String())
			}
			if destination.IsZero() {
				t.Fatalf("NewDestination(%q) reported IsZero()", tt.raw)
			}
		})
	}
}

func TestDestination_ZeroValue(t *testing.T) {
	var zero domain.Destination
	if !zero.IsZero() {
		t.Fatal("zero-value Destination should report IsZero() == true")
	}
}

func mustChannel(t *testing.T, raw string) domain.Channel {
	t.Helper()
	channel, err := domain.ParseChannel(raw)
	if err != nil {
		t.Fatalf("ParseChannel(%q) = %v", raw, err)
	}
	return channel
}

func TestNewDestinationFor_Email(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr error
	}{
		{"plain address accepted", "user@example.test", nil},
		{"plus tag accepted", "user+video-jobs@example.test", nil},
		{"subdomain accepted", "user@mail.example.test", nil},
		{"empty rejected", "", domain.ErrInvalidDestination},
		{"display name rejected", "Someone <user@example.test>", domain.ErrInvalidDestination},
		{"angle brackets alone rejected", "<user@example.test>", domain.ErrInvalidDestination},
		{"comment syntax rejected", "user@example.test (Someone)", domain.ErrInvalidDestination},
		{"quoted local part rejected", `"us er"@example.test`, domain.ErrInvalidDestination},
		{"two addresses rejected", "a@example.test, b@example.test", domain.ErrInvalidDestination},
		{"no domain rejected", "user", domain.ErrInvalidDestination},
		{"no local part rejected", "@example.test", domain.ErrInvalidDestination},
		{"a URL is not an address", "https://example.test/hooks", domain.ErrInvalidDestination},

		// The header-injection cases. A destination carrying a line break
		// that reached a message header would let its registrant add
		// recipients, headers, or a body of their own, so each of these is
		// refused at registration rather than sanitised at send time.
		{"carriage return rejected", "user@example.test\r", domain.ErrInvalidDestination},
		{"line feed rejected", "user@example.test\n", domain.ErrInvalidDestination},
		{"CRLF injected header rejected", "user@example.test\r\nBcc: someone@elsewhere.test", domain.ErrInvalidDestination},
		{"NUL rejected", "user@example.test\x00", domain.ErrInvalidDestination},
		{"leading space rejected", " user@example.test", domain.ErrInvalidDestination},
		{"trailing space rejected", "user@example.test ", domain.ErrInvalidDestination},

		// net/mail accepts an RFC 6532 UTF-8 address and net/smtp negotiates
		// no SMTPUTF8, so accepting one would store an address the adapter
		// can never put in an envelope.
		{"non-ASCII local part rejected", "usuário@example.test", domain.ErrInvalidDestination},
		{"non-ASCII domain rejected", "user@exâmple.test", domain.ErrInvalidDestination},
	}

	channel := mustChannel(t, domain.ChannelEmail)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			destination, err := domain.NewDestinationFor(channel, tt.raw)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewDestinationFor(email, %q) error = %v, want %v", tt.raw, err, tt.wantErr)
			}
			if tt.wantErr != nil {
				if !destination.IsZero() {
					t.Fatalf("NewDestinationFor(email, %q) returned %q on a rejected value", tt.raw, destination)
				}
				return
			}
			if destination.String() != tt.raw {
				t.Fatalf("NewDestinationFor(email, %q).String() = %q", tt.raw, destination.String())
			}
		})
	}
}

func TestNewDestinationFor_EmailLengthBound(t *testing.T) {
	const domainPart = "@example.test"
	fill := func(n int) string {
		local := make([]byte, n-len(domainPart))
		for i := range local {
			local[i] = 'a'
		}
		return string(local) + domainPart
	}

	if _, err := domain.NewDestinationFor(mustChannel(t, domain.ChannelEmail), fill(254)); err != nil {
		t.Fatalf("a 254-byte address was rejected: %v", err)
	}
	if _, err := domain.NewDestinationFor(mustChannel(t, domain.ChannelEmail), fill(255)); !errors.Is(err, domain.ErrInvalidDestination) {
		t.Fatalf("a 255-byte address was accepted, error = %v", err)
	}
}

func TestNewDestinationFor_Webhook(t *testing.T) {
	channel := mustChannel(t, domain.ChannelWebhook)

	if _, err := domain.NewDestinationFor(channel, "https://example.test/hooks/1"); err != nil {
		t.Fatalf("a URL was rejected for the webhook channel: %v", err)
	}
	// The complement of "a URL is not an address": each channel's rule
	// refuses the other channel's value, which is the whole reason the
	// constructor takes a channel.
	if _, err := domain.NewDestinationFor(channel, "user@example.test"); !errors.Is(err, domain.ErrInvalidDestination) {
		t.Fatalf("an address was accepted for the webhook channel, error = %v", err)
	}
}

// The default branch is the fail-closed half of the pairing: a Channel that
// was never parsed matches neither case, and must be refused rather than
// falling through to whichever rule happens to be written first.
func TestNewDestinationFor_ZeroChannelIsRefused(t *testing.T) {
	var zero domain.Channel

	for _, raw := range []string{"https://example.test/hooks", "user@example.test"} {
		if _, err := domain.NewDestinationFor(zero, raw); !errors.Is(err, domain.ErrInvalidDestination) {
			t.Fatalf("NewDestinationFor(zero, %q) error = %v, want ErrInvalidDestination", raw, err)
		}
	}
}
