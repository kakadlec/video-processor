package application

import "time"

// Clock supplies the current time. Use cases depend on this instead of
// calling time.Now() directly so tests can control CreatedAt/ExpiresAt deterministically.
type Clock interface {
	Now() time.Time
}
