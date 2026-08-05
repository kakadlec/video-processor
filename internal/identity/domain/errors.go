package domain

import "errors"

var (
	ErrInvalidUserID       = errors.New("invalid user ID")
	ErrInvalidEmail        = errors.New("invalid email")
	ErrInvalidPasswordHash = errors.New("invalid password hash")
	ErrInvalidCreatedAt    = errors.New("invalid creation time")
)
