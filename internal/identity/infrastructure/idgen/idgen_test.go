package idgen_test

import (
	"errors"
	"testing"

	"video-processor/internal/identity/domain"
	"video-processor/internal/identity/infrastructure/idgen"
)

var (
	_ domain.UserIDGenerator = idgen.Adapter{}
	_ domain.UserIDParser    = idgen.Adapter{}
)

func TestAdapter_NewUserID(t *testing.T) {
	adapter := idgen.New()

	a := adapter.NewUserID()
	b := adapter.NewUserID()

	if a.IsZero() || b.IsZero() {
		t.Fatal("NewUserID must never return the zero value")
	}
	if a.Equal(b) {
		t.Fatal("successive calls to NewUserID must produce distinct IDs")
	}
}

func TestAdapter_ParseUserID(t *testing.T) {
	adapter := idgen.New()

	tests := []struct {
		name    string
		value   string
		want    string
		wantErr error
	}{
		{"valid v4 uuid", "3fa85f64-5717-4562-b3fc-2c963f66afa6", "3fa85f64-5717-4562-b3fc-2c963f66afa6", nil},
		{"uppercase normalized to lowercase", "3FA85F64-5717-4562-B3FC-2C963F66AFA6", "3fa85f64-5717-4562-b3fc-2c963f66afa6", nil},
		{"empty string", "", "", domain.ErrInvalidUserID},
		{"not a uuid", "not-a-uuid", "", domain.ErrInvalidUserID},
		{"wrong version (v1, not v4)", "3fa85f64-5717-1562-b3fc-2c963f66afa6", "", domain.ErrInvalidUserID},
		{"wrong variant", "3fa85f64-5717-4562-0000-2c963f66afa6", "", domain.ErrInvalidUserID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := adapter.ParseUserID(tt.value)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ParseUserID(%q) error = %v, want %v", tt.value, err, tt.wantErr)
			}
			if tt.wantErr == nil && id.String() != tt.want {
				t.Fatalf("ParseUserID(%q).String() = %q, want %q", tt.value, id.String(), tt.want)
			}
		})
	}
}
