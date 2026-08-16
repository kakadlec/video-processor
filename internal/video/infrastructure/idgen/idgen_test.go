package idgen_test

import (
	"errors"
	"testing"

	"video-processor/internal/video/domain"
	"video-processor/internal/video/infrastructure/idgen"
)

var (
	_ domain.VideoJobIDGenerator = idgen.Adapter{}
	_ domain.VideoJobIDParser    = idgen.Adapter{}
)

func TestAdapter_NewVideoJobID(t *testing.T) {
	adapter := idgen.New()

	a := adapter.NewVideoJobID()
	b := adapter.NewVideoJobID()

	if a.IsZero() || b.IsZero() {
		t.Fatal("NewVideoJobID must never return the zero value")
	}
	if a.Equal(b) {
		t.Fatal("successive calls to NewVideoJobID must produce distinct IDs")
	}
}

func TestAdapter_ParseVideoJobID(t *testing.T) {
	adapter := idgen.New()

	tests := []struct {
		name    string
		value   string
		want    string
		wantErr error
	}{
		{"valid v4 uuid", "3fa85f64-5717-4562-b3fc-2c963f66afa6", "3fa85f64-5717-4562-b3fc-2c963f66afa6", nil},
		{"uppercase normalized to lowercase", "3FA85F64-5717-4562-B3FC-2C963F66AFA6", "3fa85f64-5717-4562-b3fc-2c963f66afa6", nil},
		{"empty string", "", "", domain.ErrInvalidVideoJobID},
		{"not a uuid", "not-a-uuid", "", domain.ErrInvalidVideoJobID},
		{"wrong version (v1, not v4)", "3fa85f64-5717-1562-b3fc-2c963f66afa6", "", domain.ErrInvalidVideoJobID},
		{"wrong variant", "3fa85f64-5717-4562-0000-2c963f66afa6", "", domain.ErrInvalidVideoJobID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := adapter.ParseVideoJobID(tt.value)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ParseVideoJobID(%q) error = %v, want %v", tt.value, err, tt.wantErr)
			}
			if tt.wantErr == nil && id.String() != tt.want {
				t.Fatalf("ParseVideoJobID(%q).String() = %q, want %q", tt.value, id.String(), tt.want)
			}
		})
	}
}
