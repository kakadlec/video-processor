package application

import "time"

type Clock interface {
	Now() time.Time
}
