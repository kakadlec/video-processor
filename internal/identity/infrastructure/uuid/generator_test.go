package uuid

import (
	"testing"

	"video-processor/internal/identity/domain"
)

func TestGeneratorCreatesUUIDv4UserIDs(t *testing.T) {
	generator := NewGenerator()
	first, err := generator.Generate()
	if err != nil {
		t.Fatalf("first Generate() error = %v", err)
	}
	second, err := generator.Generate()
	if err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}
	if first == second {
		t.Fatalf("generated duplicate IDs: %q", first)
	}
	if _, err := Parse(first.String()); err != nil {
		t.Fatalf("Parse(generated ID) error = %v", err)
	}
}

func TestParseAcceptsUUIDv4AndCanonicalizesIt(t *testing.T) {
	id, err := Parse("550E8400-E29B-41D4-A716-446655440000")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got, want := id.String(), "550e8400-e29b-41d4-a716-446655440000"; got != want {
		t.Fatalf("Parse() = %q, want %q", got, want)
	}
}

func TestParseRejectsInvalidOrNonV4UUIDs(t *testing.T) {
	for _, value := range []string{"", "not-a-uuid", "550e8400-e29b-11d4-a716-446655440000"} {
		t.Run(value, func(t *testing.T) {
			if _, err := Parse(value); err != domain.ErrInvalidUserID {
				t.Fatalf("Parse(%q) error = %v, want %v", value, err, domain.ErrInvalidUserID)
			}
		})
	}
}
