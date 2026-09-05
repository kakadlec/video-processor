package application_test

import (
	"strings"
	"testing"
	"time"

	"video-processor/internal/notification/application"
)

// The arithmetic the reclaim bound is validated against, pinned at the
// documented defaults. n attempts have n-1 waits between them, not n, which
// is the term most easily got wrong — and getting it wrong moves the floor
// the startup check enforces.
func TestDeliveryConfig_BudgetsAtTheDocumentedDefaults(t *testing.T) {
	config := application.DefaultDeliveryConfig()

	// 3 × 5s + 2s + 4s
	if got := config.AttemptBudget(); got != 21*time.Second {
		t.Errorf("AttemptBudget() = %s, want 21s", got)
	}
	// 3 × 2s + 1s + 2s
	if got := config.ResolveBudget(); got != 9*time.Second {
		t.Errorf("ResolveBudget() = %s, want 9s", got)
	}
	if got := config.MaxClaimHold(); got != 30*time.Second {
		t.Errorf("MaxClaimHold() = %s, want 30s", got)
	}
	if err := config.Validate(); err != nil {
		t.Errorf("the documented defaults do not validate: %v", err)
	}
}

// Both sides of the threshold. The floor is twice the hold, so at the
// defaults it is 60s: 60s clears it and 59s does not.
func TestDeliveryConfig_ValidatesTheReclaimBoundAgainstTheAttemptBudget(t *testing.T) {
	atBound := application.DefaultDeliveryConfig()
	atBound.ReclaimBound = 60 * time.Second
	if err := atBound.Validate(); err != nil {
		t.Errorf("a bound exactly at the floor was refused: %v", err)
	}

	below := application.DefaultDeliveryConfig()
	below.ReclaimBound = 59 * time.Second
	err := below.Validate()
	if err == nil {
		t.Fatal("a bound below the floor was accepted: a second consumer would be granted the claim mid-flight")
	}
	// The message names both values, so an operator who lowered one term
	// can see which pair disagrees.
	for _, want := range []string{"59s", "1m0s", "30s"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

// The floor moves with the budget rather than being a transcribed constant:
// doubling the per-attempt timeout raises it, and a bound that cleared it
// before no longer does.
func TestDeliveryConfig_TheFloorFollowsTheConfiguredTerms(t *testing.T) {
	config := application.DefaultDeliveryConfig()
	config.Timeout = 30 * time.Second
	config.ReclaimBound = application.DefaultReclaimBound

	if err := config.Validate(); err == nil {
		t.Errorf("MaxClaimHold() = %s, yet the default bound of %s was accepted",
			config.MaxClaimHold(), application.DefaultReclaimBound)
	}
}

func TestDeliveryConfig_RefusesNonPositiveTerms(t *testing.T) {
	cases := map[string]func(*application.DeliveryConfig){
		"max attempts":            func(c *application.DeliveryConfig) { c.MaxAttempts = 0 },
		"timeout":                 func(c *application.DeliveryConfig) { c.Timeout = 0 },
		"initial backoff":         func(c *application.DeliveryConfig) { c.InitialBackoff = 0 },
		"resolve max attempts":    func(c *application.DeliveryConfig) { c.ResolveMaxAttempts = -1 },
		"resolve timeout":         func(c *application.DeliveryConfig) { c.ResolveTimeout = 0 },
		"resolve initial backoff": func(c *application.DeliveryConfig) { c.ResolveInitialBackoff = 0 },
		"reclaim bound":           func(c *application.DeliveryConfig) { c.ReclaimBound = 0 },
	}
	for name, mutate := range cases {
		config := application.DefaultDeliveryConfig()
		mutate(&config)
		if err := config.Validate(); err == nil {
			t.Errorf("%s at zero was accepted; it would silently remove itself from the hold computation", name)
		}
	}
}

// A doubling wait overflows an int64 nanosecond count quickly: at the
// default 2s backoff, the 33rd attempt's wait alone is around 272 years.
// Wrapped, the total goes negative, the floor computed from it goes
// negative too, and the default 120s bound clears a floor that in truth
// exceeds any duration — a second consumer would reclaim a row whose first
// claimant is still legitimately waiting.
func TestDeliveryConfig_RefusesABudgetNoDurationCanHold(t *testing.T) {
	config := application.DefaultDeliveryConfig()
	config.MaxAttempts = 33

	if got := config.AttemptBudget(); got < 0 {
		t.Fatalf("AttemptBudget() = %s, want it saturated rather than wrapped negative", got)
	}
	if got := config.MaxClaimHold(); got < 0 {
		t.Fatalf("MaxClaimHold() = %s, want it saturated rather than wrapped negative", got)
	}
	if err := config.Validate(); err == nil {
		t.Errorf("MaxClaimHold() = %s, yet a bound of %s was accepted",
			config.MaxClaimHold(), config.ReclaimBound)
	}

	// The same on the resolve side, whose terms feed the same total.
	resolving := application.DefaultDeliveryConfig()
	resolving.ResolveMaxAttempts = 64
	if err := resolving.Validate(); err == nil {
		t.Errorf("ResolveBudget() = %s, yet a bound of %s was accepted",
			resolving.ResolveBudget(), resolving.ReclaimBound)
	}

	// And a hold just over half the representable range: the hold itself
	// fits, but the safety factor's product does not, so there is still no
	// floor to check the bound against.
	unrepresentableFloor := application.DefaultDeliveryConfig()
	unrepresentableFloor.MaxAttempts = 1
	unrepresentableFloor.ResolveMaxAttempts = 1
	unrepresentableFloor.Timeout = time.Duration(1) << 62
	unrepresentableFloor.ResolveTimeout = time.Duration(1) << 62
	if err := unrepresentableFloor.Validate(); err == nil {
		t.Errorf("MaxClaimHold() = %s, yet a bound of %s was accepted",
			unrepresentableFloor.MaxClaimHold(), unrepresentableFloor.ReclaimBound)
	}
}
