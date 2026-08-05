package domain

// UserIDGenerator creates IDs without coupling the domain to a UUID library.
type UserIDGenerator interface {
	Generate() (UserID, error)
}
