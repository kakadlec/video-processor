package domain

import "errors"

var ErrInvalidUserID = errors.New("video: invalid user id")

type UserID struct {
	value string
}

func NewUserID(value string) (UserID, error) {
	if value == "" {
		return UserID{}, ErrInvalidUserID
	}
	return UserID{value: value}, nil
}

func (id UserID) String() string {
	return id.value
}

func (id UserID) IsZero() bool {
	return id.value == ""
}

func (id UserID) Equal(other UserID) bool {
	return id.value == other.value
}
