package domain

// UserIDGenerator generates new, unique UserID values for User aggregates
// being created. It exists so the domain/application layer can obtain
// identifiers without depending on a concrete generation strategy (e.g. a
// UUID library) — that strategy is an infrastructure concern injected
// through this port.
type UserIDGenerator interface {
	NewUserID() UserID
}
