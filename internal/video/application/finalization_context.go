package application

import (
	"context"
	"time"
)

// finalizationTimeout bounds a detached context used to persist a job's
// terminal state, independent of any request context that triggered it.
const finalizationTimeout = 10 * time.Second

// NewFinalizationContext returns a context.Context deliberately independent
// of any request context that may already be canceled — e.g. because it's
// the same context exec.CommandContext used to kill an in-flight ffmpeg
// process on client disconnect, or the request's own context was otherwise
// canceled. Persisting a job's terminal state (CompleteJob/FailJob) must
// still succeed in that case: reusing the canceled context would make the
// persistence write itself fail with "context canceled", leaving the job
// stuck wherever it was (usually "processing") instead of reaching its
// actual final state.
func NewFinalizationContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), finalizationTimeout)
}
