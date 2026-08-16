package application

import "time"

// Clock supplies deterministic creation timestamps to use cases.
type Clock interface {
	Now() time.Time
}
