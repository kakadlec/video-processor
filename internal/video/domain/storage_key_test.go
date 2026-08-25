package domain_test

import (
	"errors"
	"strings"
	"testing"

	"video-processor/internal/video/domain"
)

func TestNewStorageKey(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr error
	}{
		{"non-empty value accepted", "outputs/frames_123.zip", nil},
		{"empty string rejected", "", domain.ErrInvalidStorageKey},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, err := domain.NewStorageKey(tt.value)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewStorageKey(%q) error = %v, want %v", tt.value, err, tt.wantErr)
			}
			if tt.wantErr == nil && k.String() != tt.value {
				t.Fatalf("NewStorageKey(%q).String() = %q, want %q", tt.value, k.String(), tt.value)
			}
		})
	}
}

func TestStorageKey_IsZero(t *testing.T) {
	var zero domain.StorageKey
	if !zero.IsZero() {
		t.Fatal("zero-value StorageKey should report IsZero() == true")
	}

	k, err := domain.NewStorageKey("outputs/frames_123.zip")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if k.IsZero() {
		t.Fatal("valid StorageKey should report IsZero() == false")
	}
}

// echoVideoJobIDParser validates nothing beyond the domain's own non-empty
// invariant, so these tests exercise ResultStorageKey/VideoJobIDFromStorageKey
// rather than a particular ID scheme's format rules.
type echoVideoJobIDParser struct{}

func (echoVideoJobIDParser) ParseVideoJobID(value string) (domain.VideoJobID, error) {
	return domain.NewVideoJobID(value)
}

func TestResultStorageKey_RoundTripsAndCarriesNoPathSeparator(t *testing.T) {
	jobID, err := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	key := domain.ResultStorageKey(jobID)
	if key.IsZero() {
		t.Fatal("expected a non-zero result storage key")
	}
	// The key is used verbatim as GET /download/:filename's single path
	// segment, so a separator here would break the route match after
	// percent-encoding — see the constant's own comment.
	if strings.Contains(key.String(), "/") {
		t.Fatalf("key = %q, must not contain a path separator", key.String())
	}

	recovered, err := domain.VideoJobIDFromStorageKey(key, echoVideoJobIDParser{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !recovered.Equal(jobID) {
		t.Fatalf("recovered id = %q, want %q", recovered.String(), jobID.String())
	}
}

func TestVideoJobIDFromStorageKey_RejectsMalformedKeys(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"wrong prefix", "output_3fa85f64-5717-4562-b3fc-2c963f66afa6.zip"},
		{"wrong suffix", "frames_3fa85f64-5717-4562-b3fc-2c963f66afa6.tar"},
		{"no embedded id", "frames_.zip"},
		{"unrelated value", "not-a-key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := domain.NewStorageKey(tt.key)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if _, err := domain.VideoJobIDFromStorageKey(key, echoVideoJobIDParser{}); !errors.Is(err, domain.ErrInvalidStorageKey) {
				t.Fatalf("error = %v, want it to wrap ErrInvalidStorageKey", err)
			}
		})
	}
}

func TestVideoJobIDFromStorageKey_PropagatesParserRejection(t *testing.T) {
	key := domain.ResultStorageKey(mustVideoJobID(t, "not-a-uuid"))
	parser := stubVideoJobIDParser{err: domain.ErrInvalidVideoJobID}

	if _, err := domain.VideoJobIDFromStorageKey(key, parser); !errors.Is(err, domain.ErrInvalidStorageKey) {
		t.Fatalf("error = %v, want it to wrap ErrInvalidStorageKey", err)
	}
}

func mustVideoJobID(t *testing.T, value string) domain.VideoJobID {
	t.Helper()
	id, err := domain.NewVideoJobID(value)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return id
}

func TestSourceStorageKey_CarriesTheUploadsPrefixAndUploadID(t *testing.T) {
	key := domain.SourceStorageKey("upload-1", "movie.mp4")

	if got, want := key.String(), "uploads/upload-1_movie.mp4"; got != want {
		t.Fatalf("SourceStorageKey = %q, want %q", got, want)
	}
}

// TestSourceStorageKey_TakesOnlyTheBaseName guards the one place a
// user-supplied filename reaches a derived key. A source key is an opaque
// object name rather than a filesystem path, so traversal is not the risk —
// escaping the uploads/ prefix is, since that prefix is what separates
// source objects from result objects sharing the same bucket.
func TestSourceStorageKey_TakesOnlyTheBaseName(t *testing.T) {
	for _, filename := range []string{"../../etc/passwd", "/etc/passwd", "nested/dir/movie.mp4"} {
		key := domain.SourceStorageKey("upload-1", filename).String()
		if !strings.HasPrefix(key, "uploads/upload-1_") {
			t.Fatalf("SourceStorageKey(%q) = %q, want the uploads/upload-1_ prefix", filename, key)
		}
		if strings.Count(key, "/") != 1 {
			t.Fatalf("SourceStorageKey(%q) = %q, want exactly one separator (the prefix's own)", filename, key)
		}
	}
}

// TestSourceAndResultKeysNeverCollide pins the separation the shared bucket
// depends on: results must stay flat because app.js uses a result key as a
// single URL path segment, while sources take a prefix precisely because no
// route exposes them.
func TestSourceAndResultKeysNeverCollide(t *testing.T) {
	source := domain.SourceStorageKey("upload-1", "movie.mp4").String()
	result := domain.ResultStorageKey(mustVideoJobID(t, "3fa85f64-5717-4562-b3fc-2c963f66afa6")).String()

	if source == result {
		t.Fatalf("source and result keys collided on %q", source)
	}
	if strings.Contains(result, "/") {
		t.Fatalf("result key %q must contain no separator — app.js uses it verbatim as a path segment", result)
	}
	if !strings.HasPrefix(source, "uploads/") {
		t.Fatalf("source key %q must carry the uploads/ prefix", source)
	}
}
