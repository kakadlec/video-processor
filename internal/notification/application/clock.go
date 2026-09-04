package application

import "time"

// Clock supplies the current time. Use cases depend on this instead of
// calling time.Now() directly so tests can control timestamps
// deterministically. It intentionally duplicates
// internal/identity/application.Clock and internal/video/application.Clock:
// importing another context's application package is forbidden by the
// dependency rules, and internal/notification/dependency_rules_test.go fails
// the build on an attempt.
type Clock interface {
	Now() time.Time
}
