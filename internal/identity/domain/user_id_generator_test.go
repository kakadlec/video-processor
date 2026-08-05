package domain

import "testing"

// fakeUserIDGenerator is a deterministic UserIDGenerator test double for use
// in unit tests. The concrete UUID-backed adapter is implemented in a later
// PR; production code never references this type.
type fakeUserIDGenerator struct {
	ids []UserID
	n   int
}

func newFakeUserIDGenerator(ids ...UserID) *fakeUserIDGenerator {
	return &fakeUserIDGenerator{ids: ids}
}

func (g *fakeUserIDGenerator) NewUserID() UserID {
	id := g.ids[g.n]
	g.n++
	return id
}

func TestFakeUserIDGenerator_ImplementsPort(t *testing.T) {
	var _ UserIDGenerator = newFakeUserIDGenerator()
}

func TestFakeUserIDGenerator_ReturnsConfiguredIDsInOrder(t *testing.T) {
	first, err := NewUserID("550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatalf("NewUserID() unexpected error: %v", err)
	}
	second, err := NewUserID("550e8400-e29b-41d4-a716-446655440001")
	if err != nil {
		t.Fatalf("NewUserID() unexpected error: %v", err)
	}

	gen := newFakeUserIDGenerator(first, second)

	if got := gen.NewUserID(); !got.Equals(first) {
		t.Fatalf("NewUserID() = %v, want %v", got, first)
	}
	if got := gen.NewUserID(); !got.Equals(second) {
		t.Fatalf("NewUserID() = %v, want %v", got, second)
	}
}
