package webhook_test

import (
	"strings"
	"testing"

	"video-processor/internal/notification/infrastructure/webhook"
)

// The default is the whole point of having one parser: two composition roots
// read this variable, and a default written twice is a default that can
// disagree with itself.
func TestLoadDestinationPolicyFromEnv_DefaultsToRefusingInsecureDestinations(t *testing.T) {
	t.Setenv(webhook.EnvAllowInsecureDestinations, "")

	policy, err := webhook.LoadDestinationPolicyFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if policy.AllowsInsecure() {
		t.Error("an unset variable relaxed the policy; https-only is the default posture")
	}
}

func TestLoadDestinationPolicyFromEnv_ReadsTheRelaxation(t *testing.T) {
	for _, value := range []string{"true", "TRUE", "1", "t"} {
		t.Setenv(webhook.EnvAllowInsecureDestinations, value)

		policy, err := webhook.LoadDestinationPolicyFromEnv()
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", value, err)
		}
		if !policy.AllowsInsecure() {
			t.Errorf("%q did not relax the policy", value)
		}
	}

	for _, value := range []string{"false", "FALSE", "0", "f"} {
		t.Setenv(webhook.EnvAllowInsecureDestinations, value)

		policy, err := webhook.LoadDestinationPolicyFromEnv()
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", value, err)
		}
		if policy.AllowsInsecure() {
			t.Errorf("%q relaxed the policy", value)
		}
	}
}

// Refused rather than defaulted, and the error names the variable: guessing
// either way is wrong for a security posture, and an operator who wrote
// "yes" gets told which variable to fix.
func TestLoadDestinationPolicyFromEnv_RefusesAValueThatIsNotABoolean(t *testing.T) {
	t.Setenv(webhook.EnvAllowInsecureDestinations, "yes")

	policy, err := webhook.LoadDestinationPolicyFromEnv()
	if err == nil {
		t.Fatalf("a non-boolean value was accepted, yielding a policy with insecure=%t", policy.AllowsInsecure())
	}
	if !strings.Contains(err.Error(), webhook.EnvAllowInsecureDestinations) {
		t.Errorf("error %q does not name the variable", err)
	}
	if policy.AllowsInsecure() {
		t.Error("the refused policy was returned relaxed; a caller ignoring the error would run open")
	}
}
