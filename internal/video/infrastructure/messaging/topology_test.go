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
		{"Exchange", topo.Exchange, "video.jobs.v2"},
		{"RoutingKey", topo.RoutingKey, "video_job.queued.v2"},
		{"WorkQueue", topo.WorkQueue, "video.jobs.queued.v2"},
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

// TestJobDispatchTopology_CarriesTheGenerationOnTheRoutingKey asserts the
// property the generation scheme actually turns on, and it replaces the
// assertion that versioning the *exchange* was enough.
//
// It is not. The two generations do not first meet at the broker; they meet
// in the database, at the shared video_job_outbox table every replica's
// relay claims from. That claim filters on event_type and nothing else, so
// an old replica still running during a rolling deploy will happily claim a
// new job's row and publish it into its own exchange — where its own inline
// handler finishes the job and then deletes the source object out from
// under the new worker's running ffmpeg. An already-deployed relay cannot be
// taught a new predicate, so the only thing that stops it is a literal it
// can never match: the generation has to be in the event_type string itself.
//
// The routing key equals that string (see
// TestRoutingKeyMatchesTheOutboxEventType), so it carries the suffix too.
// The exchange and work queue are versioned as well, so a message that
// somehow slips out under the old names has nowhere to land.
func TestJobDispatchTopology_CarriesTheGenerationOnTheRoutingKey(t *testing.T) {
	topo := messaging.JobDispatchTopology()

	const generation = ".v2"
	if !strings.HasSuffix(topo.RoutingKey, generation) {
		t.Errorf("RoutingKey = %q, want the generation suffix %q — it is the outbox event_type, and that is what isolates the generations", topo.RoutingKey, generation)
	}
	if !strings.HasSuffix(topo.Exchange, generation) {
		t.Errorf("Exchange = %q, want the generation suffix %q", topo.Exchange, generation)
	}
	if !strings.HasSuffix(topo.WorkQueue, generation) {
		t.Errorf("WorkQueue = %q, want the generation suffix %q", topo.WorkQueue, generation)
	}
	if strings.HasSuffix(topo.DeadExchange, generation) || strings.HasSuffix(topo.DeadQueue, generation) {
		t.Errorf("the dead-letter sink is versioned (%q, %q); both generations share it deliberately", topo.DeadExchange, topo.DeadQueue)
	}
}
