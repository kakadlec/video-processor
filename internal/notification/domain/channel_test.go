package domain_test

import (
	"errors"
	"testing"

	"video-processor/internal/notification/domain"
)

func TestParseChannel(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr error
	}{
		{"webhook accepted", domain.ChannelWebhook, nil},
		{"email accepted", domain.ChannelEmail, nil},
		{"sms rejected", "sms", domain.ErrInvalidChannel},
		{"slack rejected", "slack", domain.ErrInvalidChannel},
		{"email casing is significant", "Email", domain.ErrInvalidChannel},
		{"casing is significant", "Webhook", domain.ErrInvalidChannel},
		{"empty rejected", "", domain.ErrInvalidChannel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel, err := domain.ParseChannel(tt.raw)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ParseChannel(%q) error = %v, want %v", tt.raw, err, tt.wantErr)
			}
			if tt.wantErr != nil {
				if !channel.IsZero() {
					t.Fatalf("ParseChannel(%q) returned %q on a rejected value", tt.raw, channel)
				}
				return
			}
			if channel.String() != tt.raw {
				t.Fatalf("ParseChannel(%q).String() = %q", tt.raw, channel.String())
			}
		})
	}
}

func TestChannel_ZeroValue(t *testing.T) {
	var zero domain.Channel
	if !zero.IsZero() {
		t.Fatal("zero-value Channel should report IsZero() == true")
	}
}

// The set is closed, and this is what says so: a value being absent from the
// table above proves only that nobody wrote a row for it, while asserting the
// accepted set exhaustively fails the day a third constant is added without a
// decision about it.
func TestParseChannel_AcceptsExactlyTheClosedSet(t *testing.T) {
	accepted := []string{domain.ChannelWebhook, domain.ChannelEmail}

	for _, raw := range accepted {
		if _, err := domain.ParseChannel(raw); err != nil {
			t.Fatalf("ParseChannel(%q) = %v, want it accepted", raw, err)
		}
	}

	// Every ASCII string of one or two lowercase letters stands in for "some
	// other plausible value": none of them is in the set, so all must be
	// refused. Cheap, and it catches a default branch that fell through.
	for a := byte('a'); a <= 'z'; a++ {
		for _, raw := range []string{string(a), string([]byte{a, a})} {
			if _, err := domain.ParseChannel(raw); !errors.Is(err, domain.ErrInvalidChannel) {
				t.Fatalf("ParseChannel(%q) = %v, want ErrInvalidChannel", raw, err)
			}
		}
	}
}

func TestChannel_Signs(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{domain.ChannelWebhook, true},
		{domain.ChannelEmail, false},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			channel, err := domain.ParseChannel(tt.raw)
			if err != nil {
				t.Fatalf("ParseChannel(%q) = %v", tt.raw, err)
			}
			if got := channel.Signs(); got != tt.want {
				t.Fatalf("Channel(%q).Signs() = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

// The zero value never reaches Signs through ParseChannel, but it does reach
// it through a struct literal in a test or a future caller. Not signing is
// the safe answer: it is what keeps a channel that was never parsed from
// being treated as one whose deliveries are signed.
func TestChannel_ZeroValueDoesNotSign(t *testing.T) {
	var zero domain.Channel
	if zero.Signs() {
		t.Fatal("zero-value Channel should not report Signs() == true")
	}
}

// AllChannels is what a composition root is exhaustive over, so it has to
// equal the set ParseChannel accepts rather than a list maintained beside
// it. Both directions: every value it returns parses, and every accepted
// value appears in it.
func TestAllChannels_EqualsTheAcceptedSet(t *testing.T) {
	all := domain.AllChannels()

	seen := make(map[string]bool, len(all))
	for _, channel := range all {
		if channel.IsZero() {
			t.Fatal("AllChannels returned a zero-value Channel")
		}
		if _, err := domain.ParseChannel(channel.String()); err != nil {
			t.Fatalf("AllChannels returned %q, which ParseChannel refuses", channel)
		}
		if seen[channel.String()] {
			t.Fatalf("AllChannels returned %q twice", channel)
		}
		seen[channel.String()] = true
	}

	for _, accepted := range []string{domain.ChannelWebhook, domain.ChannelEmail} {
		if !seen[accepted] {
			t.Fatalf("AllChannels omits %q, which ParseChannel accepts", accepted)
		}
	}
}

// A caller editing the returned slice must not edit it for the next one.
func TestAllChannels_ReturnsAFreshSlice(t *testing.T) {
	first := domain.AllChannels()
	first[0] = domain.Channel{}

	for _, channel := range domain.AllChannels() {
		if channel.IsZero() {
			t.Fatal("AllChannels shares its backing array between calls")
		}
	}
}
