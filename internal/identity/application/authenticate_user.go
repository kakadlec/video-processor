package application

import (
	"context"
	"errors"
	"time"

	"video-processor/internal/identity/domain"
)

// ErrAuthenticationFailed is the single external failure shape for both an
// unknown email and an incorrect password — the two must be indistinguishable
// to the caller.
var ErrAuthenticationFailed = errors.New("identity: authentication failed")

// AccessTokenTTL is the fixed lifetime of an issued access token.
const AccessTokenTTL = 15 * time.Minute

// AuthenticateUserInput carries the caller-supplied login credentials.
type AuthenticateUserInput struct {
	Email    string
	Password string
}

// AuthenticateUserResult is the token response returned after successful authentication.
type AuthenticateUserResult struct {
	AccessToken string
	ExpiresAt   time.Time
}

// AuthenticateUser verifies credentials and issues a bearer access token. It
// depends only on domain ports, so it is agnostic to the concrete password
// and token implementations.
type AuthenticateUser struct {
	users     domain.UserRepository
	passwords domain.PasswordHasher
	tokens    domain.TokenIssuer
	clock     Clock
}

// NewAuthenticateUser wires the AuthenticateUser use case to its ports.
func NewAuthenticateUser(users domain.UserRepository, passwords domain.PasswordHasher, tokens domain.TokenIssuer, clock Clock) *AuthenticateUser {
	return &AuthenticateUser{users: users, passwords: passwords, tokens: tokens, clock: clock}
}

// Execute runs the authentication use case.
func (uc *AuthenticateUser) Execute(ctx context.Context, input AuthenticateUserInput) (AuthenticateUserResult, error) {
	email, err := domain.NewEmail(input.Email)
	if err != nil {
		return AuthenticateUserResult{}, ErrAuthenticationFailed
	}

	user, err := uc.users.FindByNormalizedEmail(ctx, email.NormalizedForLookup())
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			return AuthenticateUserResult{}, ErrAuthenticationFailed
		}
		return AuthenticateUserResult{}, err
	}

	if err := uc.passwords.Compare(user.PasswordHash(), input.Password); err != nil {
		return AuthenticateUserResult{}, ErrAuthenticationFailed
	}

	expiresAt := uc.clock.Now().Add(AccessTokenTTL)
	token, err := uc.tokens.Issue(user.ID(), expiresAt)
	if err != nil {
		return AuthenticateUserResult{}, err
	}

	return AuthenticateUserResult{AccessToken: token, ExpiresAt: expiresAt}, nil
}
