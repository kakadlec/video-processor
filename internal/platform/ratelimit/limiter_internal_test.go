package ratelimit

import (
	"testing"
	"time"
)

// TestMsToRetryAfter isolates the millisecond-to-Retry-After rounding math
// with deterministic inputs, independent of live Redis timing — an
// implementation that truncated instead of ceiling (e.g. plain integer
// division, or deriving from TTL's whole-second value instead of PTTL's
// milliseconds) would fail the sub-second cases here.
func TestMsToRetryAfter(t *testing.T) {
	cases := []struct {
		name  string
		ttlMs int64
		want  time.Duration
	}{
		{"zero rounds up to minimum 1s", 0, time.Second},
		{"negative (already expired) clamps to minimum 1s", -500, time.Second},
		{"1ms rounds up to 1s", 1, time.Second},
		{"999ms rounds up to 1s", 999, time.Second},
		{"exactly 1000ms stays 1s, does not round up further", 1000, time.Second},
		{"1001ms rounds up to 2s", 1001, 2 * time.Second},
		{"1999ms rounds up to 2s", 1999, 2 * time.Second},
		{"2000ms stays 2s", 2000, 2 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := msToRetryAfter(tc.ttlMs)
			if got != tc.want {
				t.Fatalf("msToRetryAfter(%d) = %v, want %v", tc.ttlMs, got, tc.want)
			}
		})
	}
}
