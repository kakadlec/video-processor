package domain

import "errors"

var (
	// ErrInvalidUserID indicates a UserID value is empty or not a well-formed UUID.
	ErrInvalidUserID = errors.New("identity: invalid user id")

	// ErrInvalidEmail indicates an email value violates the identity email policy.
	ErrInvalidEmail = errors.New("identity: invalid email")

	// ErrEmptyPasswordHash indicates a User was constructed without a password hash.
	ErrEmptyPasswordHash = errors.New("identity: password hash must not be empty")

	// ErrInvalidCreatedAt indicates a User was constructed without a creation timestamp.
	ErrInvalidCreatedAt = errors.New("identity: created at must not be zero")
)
