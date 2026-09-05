package webhook

import (
	"fmt"
	"os"
	"strconv"

	"video-processor/internal/notification/domain"
)

// EnvAllowInsecureDestinations names the variable that relaxes the
// destination policy. One variable, and one parser for it, read by both
// composition roots: cmd/api judges a destination when it is registered and
// cmd/notifier judges the address when it is dialled, and a deployment where
// those two disagree either stores destinations it can never deliver to or
// refuses at dial what it accepted at write time.
const EnvAllowInsecureDestinations = "NOTIFICATION_ALLOW_INSECURE_DESTINATIONS"

// LoadDestinationPolicyFromEnv builds the policy a process runs under.
//
// The default is the restrictive one, and it is expressed here rather than
// in each composition root so that neither can drift from it: an unset
// variable means https-only, globally-reachable-unicast-only.
//
// A value that is not a boolean is refused rather than treated as unset. The
// relaxation is a security posture, so the two ways of guessing are both
// wrong: reading "yes" as false leaves a local stack refusing every
// destination with nothing to point at, and reading it as true would let a
// typo open the policy in production.
func LoadDestinationPolicyFromEnv() (domain.DestinationPolicy, error) {
	raw := os.Getenv(EnvAllowInsecureDestinations)
	if raw == "" {
		return domain.NewDestinationPolicy(false), nil
	}
	allowInsecure, err := strconv.ParseBool(raw)
	if err != nil {
		return domain.DestinationPolicy{}, fmt.Errorf(
			"notification: %s must be a boolean, got %q", EnvAllowInsecureDestinations, raw)
	}
	return domain.NewDestinationPolicy(allowInsecure), nil
}
