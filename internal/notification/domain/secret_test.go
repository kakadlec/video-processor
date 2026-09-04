package domain_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"video-processor/internal/notification/domain"
)

const validSecret = "0123456789abcdef" // exactly MinSecretLength bytes

func TestNewSecret(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr error
	}{
		{"minimum length accepted", validSecret, nil},
		{"longer accepted", validSecret + "-and-then-some", nil},
		{"one byte short rejected", "0123456789abcde", domain.ErrInvalidSecret},
		{"empty rejected", "", domain.ErrInvalidSecret},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret, err := domain.NewSecret(tt.raw)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewSecret error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				if !secret.IsZero() {
					t.Fatal("NewSecret returned a non-zero Secret on a rejected value")
				}
				return
			}
			if secret.Reveal() != tt.raw {
				t.Fatalf("Secret.Reveal() = %q, want the supplied value", secret.Reveal())
			}
			if secret.IsZero() {
				t.Fatal("a constructed Secret reported IsZero()")
			}
		})
	}
}

func TestMinSecretLengthIsSixteen(t *testing.T) {
	if domain.MinSecretLength != 16 {
		t.Fatalf("MinSecretLength = %d, want 16", domain.MinSecretLength)
	}
}

// TestSecret_IsNeverRenderedByFmt is the invariant this type exists for.
// Non-disclosure is the whole of the secret's protection — it cannot be
// hashed, because signing needs the original bytes — so an accidental format
// verb must not be able to print it.
func TestSecret_IsNeverRenderedByFmt(t *testing.T) {
	secret, err := domain.NewSecret(validSecret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		t.Run(verb, func(t *testing.T) {
			rendered := fmt.Sprintf(verb, secret)
			if strings.Contains(rendered, validSecret) {
				t.Fatalf("fmt %s rendered the secret: %s", verb, rendered)
			}
		})
	}

	// A pointer must be as safe as a value: fmt follows one at the top
	// level, so a pointer-receiver-only String would leave this printing the
	// struct's contents.
	if rendered := fmt.Sprintf("%+v", &secret); strings.Contains(rendered, validSecret) {
		t.Fatalf("fmt %%+v on a *Secret rendered the secret: %s", rendered)
	}
}

func TestSecret_RefusesToMarshalJSON(t *testing.T) {
	secret, err := domain.NewSecret(validSecret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	encoded, err := json.Marshal(secret)
	if !errors.Is(err, domain.ErrSecretNotSerializable) {
		t.Fatalf("json.Marshal(Secret) error = %v, want ErrSecretNotSerializable", err)
	}
	if strings.Contains(string(encoded), validSecret) {
		t.Fatalf("json.Marshal(Secret) emitted the secret: %s", encoded)
	}
}

func TestSecret_ZeroValue(t *testing.T) {
	var zero domain.Secret
	if !zero.IsZero() {
		t.Fatal("zero-value Secret should report IsZero() == true")
	}
	if zero.Reveal() != "" {
		t.Fatal("zero-value Secret should reveal an empty string")
	}
}
