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
		// email stays rejected until an adapter delivers through it.
		{"email rejected", "email", domain.ErrInvalidChannel},
		{"sms rejected", "sms", domain.ErrInvalidChannel},
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
