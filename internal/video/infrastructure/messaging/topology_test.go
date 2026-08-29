package messaging_test

import (
	"strings"
	"testing"
	"time"

	"video-processor/internal/video/infrastructure/messaging"
)

// TestJobDispatchTopology_ReturnsThePinnedValues is the only place the
// production topology is checked: every test in internal/platform/rabbitmq
// runs under test-scoped names and would pass against any values at all.
func TestJobDispatchTopology_ReturnsThePinnedValues(t *testing.T) {
	topo := messaging.JobDispatchTopology()

	for _, tc := range []struct {
		field string
		got   string
		want  string
	}{
		{"Exchange", topo.Exchange, "video.jobs.v1"},
		{"RoutingKey", topo.RoutingKey, "video_job.queued"},
		{"WorkQueue", topo.WorkQueue, "video.jobs.queued.v1"},
		{"DeadExchange", topo.DeadExchange, "video.jobs.dlx"},
		{"DeadQueue", topo.DeadQueue, "video.jobs.dead"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}

	if topo.WorkMaxLength != 10000 {
		t.Errorf("WorkMaxLength = %d, want 10000", topo.WorkMaxLength)
	}
	if topo.DeadMessageTTL != 24*time.Hour {
		t.Errorf("DeadMessageTTL = %v, want 24h", topo.DeadMessageTTL)
	}
	if topo.DeadMaxLength != 10000 {
		t.Errorf("DeadMaxLength = %d, want 10000", topo.DeadMaxLength)
	}
}

// TestJobDispatchTopology_VersionsTheExchangeNotTheRoutingKey asserts the
// property the generation scheme turns on. A direct exchange delivers each
// publish to every queue bound with the matching routing key, so versioning
// only the queue would let a replica that has not been redeployed reach a
// later generation's consumer. Versioning the exchange separates the
// delivery paths while leaving the routing key equal to the outbox
// event_type string.
func TestJobDispatchTopology_VersionsTheExchangeNotTheRoutingKey(t *testing.T) {
	topo := messaging.JobDispatchTopology()

	if !strings.HasSuffix(topo.Exchange, ".v1") {
		t.Errorf("Exchange = %q, want a generation suffix", topo.Exchange)
	}
	if strings.Contains(topo.RoutingKey, ".v") {
		t.Errorf("RoutingKey = %q carries a generation suffix; it must stay equal to the outbox event_type", topo.RoutingKey)
	}
	if strings.HasSuffix(topo.DeadExchange, ".v1") || strings.HasSuffix(topo.DeadQueue, ".v1") {
		t.Errorf("the dead-letter sink is versioned (%q, %q); both generations share it deliberately", topo.DeadExchange, topo.DeadQueue)
	}
}
