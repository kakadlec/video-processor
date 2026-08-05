package domain

import "time"

// PasswordHash is an opaque, already-hashed credential value. The domain
// never receives or stores a plaintext password: hashing is performed by
// an infrastructure password-hasher port before a PasswordHash is created.
type PasswordHash struct {
	value string
}

// NewPasswordHash wraps an already-hashed password value.
func NewPasswordHash(value string) (PasswordHash, error) {
	if value == "" {
		return PasswordHash{}, ErrEmptyPasswordHash
	}
	return PasswordHash{value: value}, nil
}

// String returns the underlying hash value.
func (h PasswordHash) String() string {
	return h.value
}

// IsZero reports whether h is the zero value (never validated).
func (h PasswordHash) IsZero() bool {
	return h.value == ""
}

// User is the Identity bounded context's aggregate root.
type User struct {
	id           UserID
	email        Email
	passwordHash PasswordHash
	createdAt    time.Time
}

// NewUser constructs a User aggregate, enforcing its invariants: a valid
// UserID, a valid normalized Email, a non-empty PasswordHash, and a
// non-zero creation timestamp.
func NewUser(id UserID, email Email, passwordHash PasswordHash, createdAt time.Time) (*User, error) {
	if id.IsZero() {
		return nil, ErrInvalidUserID
	}
	if email.IsZero() {
		return nil, ErrInvalidEmail
	}
	if passwordHash.IsZero() {
		return nil, ErrEmptyPasswordHash
	}
	if createdAt.IsZero() {
		return nil, ErrInvalidCreatedAt
	}

	return &User{
		id:           id,
		email:        email,
		passwordHash: passwordHash,
		createdAt:    createdAt,
	}, nil
}

// ID returns the user's opaque identifier.
func (u *User) ID() UserID {
	return u.id
}

// Email returns the user's normalized email.
func (u *User) Email() Email {
	return u.email
}

// PasswordHash returns the user's stored password hash.
func (u *User) PasswordHash() PasswordHash {
	return u.passwordHash
}

// CreatedAt returns the user's creation timestamp.
func (u *User) CreatedAt() time.Time {
	return u.createdAt
}
