package application

import (
	"fmt"
	"math"
	"time"
)

// The documented defaults for the delivery budget.
//
// Every term has one, and that is a requirement rather than a convenience:
// the maximum time a claimant can hold a claim is computed from all of them,
// and the reclaim bound is validated against that computation. A term with
// no default would make the validation unreproducible.
const (
	DefaultDeliveryMaxAttempts    = 3
	DefaultDeliveryTimeout        = 5 * time.Second
	DefaultDeliveryInitialBackoff = 2 * time.Second

	DefaultResolveMaxAttempts    = 3
	DefaultResolveTimeout        = 2 * time.Second
	DefaultResolveInitialBackoff = 1 * time.Second

	DefaultReclaimBound = 120 * time.Second
)

// reclaimBoundSafetyFactor is how much headroom the reclaim bound must have
// over the longest a claimant can legitimately hold a claim.
//
// Two to three, not ten. The claim token fences the database write, but it
// cannot recall an HTTP request already on the wire: a bound shorter than
// the budget grants a second consumer the claim mid-flight and the receiver
// gets two requests in *normal* operation, not only after a crash. Generous
// headroom is not free in the other direction either — at prefetch 1 the
// same value bounds how long an abandoned claim stalls the queue.
const reclaimBoundSafetyFactor = 2

// DeliveryConfig is the whole budget for handling one event: how many times
// a delivery is attempted, how long each attempt may take, how long the
// waits between them are, the same three terms for recording the outcome,
// and the bound after which an unresolved claim may be taken over.
//
// The last term is not independent of the others, which is why they live in
// one struct with one validator rather than being read separately wherever
// each is needed.
type DeliveryConfig struct {
	MaxAttempts    int
	Timeout        time.Duration
	InitialBackoff time.Duration

	ResolveMaxAttempts    int
	ResolveTimeout        time.Duration
	ResolveInitialBackoff time.Duration

	ReclaimBound time.Duration
}

// DefaultDeliveryConfig returns the documented defaults. It satisfies
// Validate, and a composition root that overrides a term is expected to
// re-run Validate over the result.
func DefaultDeliveryConfig() DeliveryConfig {
	return DeliveryConfig{
		MaxAttempts:           DefaultDeliveryMaxAttempts,
		Timeout:               DefaultDeliveryTimeout,
		InitialBackoff:        DefaultDeliveryInitialBackoff,
		ResolveMaxAttempts:    DefaultResolveMaxAttempts,
		ResolveTimeout:        DefaultResolveTimeout,
		ResolveInitialBackoff: DefaultResolveInitialBackoff,
		ReclaimBound:          DefaultReclaimBound,
	}
}

// AttemptBudget is the longest the delivery attempts alone can take: every
// attempt running to its timeout, plus every wait between them.
func (c DeliveryConfig) AttemptBudget() time.Duration {
	return budget(c.MaxAttempts, c.Timeout, c.InitialBackoff)
}

// ResolveBudget is the same arithmetic over the terms that record the
// outcome.
func (c DeliveryConfig) ResolveBudget() time.Duration {
	return budget(c.ResolveMaxAttempts, c.ResolveTimeout, c.ResolveInitialBackoff)
}

// MaxClaimHold is the longest a claimant can hold a claim: it attempts, and
// then it records what happened, and only then is it done with the row.
//
// It is written as arithmetic over the configured terms rather than as a
// constant so that lowering a default moves the floor with it, which a
// constant transcribed once would not.
func (c DeliveryConfig) MaxClaimHold() time.Duration {
	return addDuration(c.AttemptBudget(), c.ResolveBudget())
}

// maxDuration is what the arithmetic below saturates at rather than
// wrapping. A doubling wait reaches it quickly — the default 2s backoff
// overflows an int64 nanosecond count at around 33 attempts — and a wrapped
// total is worse than a refused configuration: it is negative, so every
// comparison it feeds silently inverts and a reclaim bound far below the
// real hold passes validation.
const maxDuration = time.Duration(math.MaxInt64)

// budget totals n attempts of the given timeout with a doubling wait between
// them. n attempts have n-1 waits, not n: nothing is waited for before the
// first attempt or after the last.
//
// It saturates rather than wrapping; Validate is what refuses a saturated
// budget, because only there is there something to report it to.
func budget(attempts int, timeout, initialBackoff time.Duration) time.Duration {
	if attempts <= 0 || timeout < 0 || initialBackoff < 0 {
		return 0
	}
	total := mulDuration(timeout, attempts)
	wait := initialBackoff
	for i := 0; i < attempts-1 && total < maxDuration; i++ {
		total = addDuration(total, wait)
		wait = mulDuration(wait, 2)
	}
	return total
}

// addDuration and mulDuration saturate at maxDuration. Both take
// non-negative operands only; budget clamps its inputs before calling them.
func addDuration(a, b time.Duration) time.Duration {
	if a < 0 || b < 0 {
		return 0
	}
	if b > maxDuration-a {
		return maxDuration
	}
	return a + b
}

func mulDuration(d time.Duration, n int) time.Duration {
	if d <= 0 || n <= 0 {
		return 0
	}
	if d > maxDuration/time.Duration(n) {
		return maxDuration
	}
	return d * time.Duration(n)
}

// Validate refuses a configuration whose reclaim bound is shorter than
// reclaimBoundSafetyFactor times the longest a claimant can hold a claim,
// and refuses any non-positive term — a term at zero would silently remove
// itself from the computation the bound is checked against.
//
// A composition root calls this at startup and treats a failure as fatal.
// A startup check rather than a comment, because these are separately
// tunable variables and a comment does not survive someone lowering one.
func (c DeliveryConfig) Validate() error {
	if c.MaxAttempts <= 0 {
		return fmt.Errorf("notification: delivery max attempts must be positive, got %d", c.MaxAttempts)
	}
	if c.ResolveMaxAttempts <= 0 {
		return fmt.Errorf("notification: resolve max attempts must be positive, got %d", c.ResolveMaxAttempts)
	}
	for _, term := range []struct {
		name  string
		value time.Duration
	}{
		{"delivery timeout", c.Timeout},
		{"delivery initial backoff", c.InitialBackoff},
		{"resolve timeout", c.ResolveTimeout},
		{"resolve initial backoff", c.ResolveInitialBackoff},
		{"reclaim bound", c.ReclaimBound},
	} {
		if term.value <= 0 {
			return fmt.Errorf("notification: %s must be positive, got %s", term.name, term.value)
		}
	}

	hold := c.MaxClaimHold()
	floor := mulDuration(hold, reclaimBoundSafetyFactor)
	// A saturated hold or floor means the configured terms describe a budget
	// no duration can hold, so there is no floor left to check the bound
	// against. Refusing is the only honest answer: saturating quietly would
	// accept a bound the arithmetic can no longer justify.
	if hold >= maxDuration || floor >= maxDuration {
		return fmt.Errorf(
			"notification: the attempt budget is not representable as a duration (max attempts %d, timeout %s, initial backoff %s; resolve %d, %s, %s), so no reclaim bound can be validated against it",
			c.MaxAttempts, c.Timeout, c.InitialBackoff,
			c.ResolveMaxAttempts, c.ResolveTimeout, c.ResolveInitialBackoff)
	}
	if c.ReclaimBound < floor {
		return fmt.Errorf(
			"notification: reclaim bound %s is below %s, which is %d times the %s a claimant can hold a claim under the configured attempt budget",
			c.ReclaimBound, floor, reclaimBoundSafetyFactor, hold)
	}
	return nil
}
