package application_test

import (
	"context"
	"sync"
	"time"

	"video-processor/internal/identity/domain"
)

// fakeUserRepository is an in-memory domain.UserRepository used to unit test
// use cases without depending on any real persistence adapter.
type fakeUserRepository struct {
	mu      sync.Mutex
	byID    map[string]*domain.User
	byEmail map[string]*domain.User
}

func newFakeUserRepository() *fakeUserRepository {
	return &fakeUserRepository{
		byID:    make(map[string]*domain.User),
		byEmail: make(map[string]*domain.User),
	}
}

func (r *fakeUserRepository) Create(_ context.Context, user *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := user.Email().NormalizedForLookup()
	if _, exists := r.byEmail[key]; exists {
		return domain.ErrUserAlreadyExists
	}
	r.byID[user.ID().String()] = user
	r.byEmail[key] = user
	return nil
}

func (r *fakeUserRepository) FindByID(_ context.Context, id domain.UserID) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	user, ok := r.byID[id.String()]
	if !ok {
		return nil, domain.ErrUserNotFound
	}
	return user, nil
}

func (r *fakeUserRepository) FindByNormalizedEmail(_ context.Context, normalizedEmail string) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	user, ok := r.byEmail[normalizedEmail]
	if !ok {
		return nil, domain.ErrUserNotFound
	}
	return user, nil
}

// fakePasswordHasher is a deterministic, non-cryptographic stand-in for the
// real adaptive password hasher (bcrypt/argon2), which lands in the
// infrastructure adapter PR.
type fakePasswordHasher struct{}

func (fakePasswordHasher) Hash(plaintext string) (domain.PasswordHash, error) {
	return domain.NewPasswordHash("hashed:" + plaintext)
}

func (fakePasswordHasher) Compare(hash domain.PasswordHash, plaintext string) error {
	if hash.String() != "hashed:"+plaintext {
		return domain.ErrPasswordMismatch
	}
	return nil
}

// fakeTokenIssuer is a deterministic stand-in for the real JWT issuer.
type fakeTokenIssuer struct {
	err error
}

func (f fakeTokenIssuer) Issue(userID domain.UserID, _ time.Time) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "token-for-" + userID.String(), nil
}

// fakeUserIDGenerator always returns the same pre-set UserID, for deterministic assertions.
type fakeUserIDGenerator struct {
	id domain.UserID
}

func (f fakeUserIDGenerator) NewUserID() domain.UserID {
	return f.id
}

// fakeClock always returns the same pre-set time, for deterministic assertions.
type fakeClock struct {
	now time.Time
}

func (f fakeClock) Now() time.Time {
	return f.now
}
