package application

import (
	"context"
	"errors"
	"time"

	"video-processor/internal/identity/domain"
)

// ErrPasswordTooShort is returned when a registration password does not meet the minimum length policy.
var ErrPasswordTooShort = errors.New("identity: password must be at least 8 characters")

const minPasswordLength = 8

// RegisterUserInput carries the caller-supplied registration fields.
type RegisterUserInput struct {
	Email    string
	Password string
}

// RegisterUserResult is the non-sensitive representation returned after registration.
type RegisterUserResult struct {
	UserID    string
	Email     string
	CreatedAt time.Time
}

// RegisterUser registers a new user: validates input, rejects duplicate
// emails, hashes the password, and persists the resulting User. It depends
// only on domain ports, so it can be tested with fakes and is agnostic to
// the concrete password-hashing algorithm, ID scheme, and storage engine.
type RegisterUser struct {
	users     domain.UserRepository
	ids       domain.UserIDGenerator
	passwords domain.PasswordHasher
	clock     Clock
}

// NewRegisterUser wires the RegisterUser use case to its ports.
func NewRegisterUser(users domain.UserRepository, ids domain.UserIDGenerator, passwords domain.PasswordHasher, clock Clock) *RegisterUser {
	return &RegisterUser{users: users, ids: ids, passwords: passwords, clock: clock}
}

// Execute runs the registration use case.
func (uc *RegisterUser) Execute(ctx context.Context, input RegisterUserInput) (RegisterUserResult, error) {
	email, err := domain.NewEmail(input.Email)
	if err != nil {
		return RegisterUserResult{}, err
	}
	if len(input.Password) < minPasswordLength {
		return RegisterUserResult{}, ErrPasswordTooShort
	}

	_, err = uc.users.FindByNormalizedEmail(ctx, email.NormalizedForLookup())
	switch {
	case err == nil:
		return RegisterUserResult{}, domain.ErrUserAlreadyExists
	case errors.Is(err, domain.ErrUserNotFound):
		// expected path: no existing account, continue registration
	default:
		return RegisterUserResult{}, err
	}

	hash, err := uc.passwords.Hash(input.Password)
	if err != nil {
		return RegisterUserResult{}, err
	}

	user, err := domain.NewUser(uc.ids, email, hash, uc.clock.Now())
	if err != nil {
		return RegisterUserResult{}, err
	}

	if err := uc.users.Create(ctx, user); err != nil {
		return RegisterUserResult{}, err
	}

	return RegisterUserResult{
		UserID:    user.ID().String(),
		Email:     user.Email().String(),
		CreatedAt: user.CreatedAt(),
	}, nil
}
