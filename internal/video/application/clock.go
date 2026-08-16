package application

import "time"

// Clock supplies the current time. Use cases depend on this instead of
// calling time.Now() directly so tests can control CreatedAt deterministically.
// It intentionally duplicates internal/identity/application.Clock: importing
// another context's application package is forbidden by the dependency rules.
type Clock interface {
	Now() time.Time
}
