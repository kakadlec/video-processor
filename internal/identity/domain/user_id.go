package domain

// UserID identifies a user without exposing Identity's aggregate to other contexts.
// Its concrete representation is owned by the infrastructure adapter.
type UserID string

func (id UserID) String() string { return string(id) }
