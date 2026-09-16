package main

import (
	"context"
	"testing"

	notificationapplication "video-processor/internal/notification/application"
	notificationdomain "video-processor/internal/notification/domain"
	notificationmessaging "video-processor/internal/notification/infrastructure/messaging"
	"video-processor/internal/platform/metrics"
)

// familySample reads one label combination's value out of the process
// registry, by family name -- the same pattern
// internal/video/infrastructure/ffmpeg's and cmd/worker's own metrics_test.go
// files use, through the client library's own parsed type, never named
// directly. Like every other test in this package that reads process-global
// registry state, callers are not parallel-safe.
func familySample(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()

	families, err := metrics.Gatherer().Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	var total float64
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, sample := range family.GetMetric() {
			got := make(map[string]string, len(labels))
			for _, pair := range sample.GetLabel() {
				got[pair.GetName()] = pair.GetValue()
			}
			match := true
			for wantName, wantValue := range labels {
				if got[wantName] != wantValue {
					match = false
					break
				}
			}
			if !match {
				continue
			}
			if h := sample.GetHistogram(); h != nil {
				total += float64(h.GetSampleCount())
				continue
			}
			if g := sample.GetGauge(); g != nil {
				total += g.GetValue()
				continue
			}
			total += sample.GetCounter().GetValue()
		}
	}
	return total
}

// TestHandle_RecordsDeliveryDurationByDisposition pins the delivery-attempt
// histogram docs/roadmap.md's expose-worker-and-notifier-metrics row asks
// for: "handled" and "deferred" each land under their own label, and nowhere
// else.
func TestHandle_RecordsDeliveryDurationByDisposition(t *testing.T) {
	before := familySample(t, "fiapx_notifier_event_delivery_duration_seconds", map[string]string{"disposition": "handled"})

	deps := newTestDeps(t,
		&stubPreferences{},
		&stubDeliveries{},
		&stubDeliverer{})

	if got := deps.handle(context.Background(), notificationdomain.EventTypeVideoJobCompleted, completedBody(t, testJobID, testUserID)); got != notificationmessaging.Ack {
		t.Fatalf("disposition = %v, want Ack", got)
	}

	if got := familySample(t, "fiapx_notifier_event_delivery_duration_seconds", map[string]string{"disposition": "handled"}); got != before+1 {
		t.Fatalf(`fiapx_notifier_event_delivery_duration_seconds{disposition="handled"} sample count = %v, want %v`, got, before+1)
	}
}

// TestRecordDeliveryMaxClaimHold pins the companion gauge: it publishes
// exactly DeliveryConfig.MaxClaimHold() in seconds, so a dashboard can
// compare the histogram above against it without an operator reproducing
// the arithmetic by hand.
func TestRecordDeliveryMaxClaimHold(t *testing.T) {
	config := notificationapplication.DefaultDeliveryConfig()

	recordDeliveryMaxClaimHold(config)

	if got := familySample(t, "fiapx_notifier_delivery_max_claim_hold_seconds", nil); got != config.MaxClaimHold().Seconds() {
		t.Fatalf("fiapx_notifier_delivery_max_claim_hold_seconds = %v, want %v", got, config.MaxClaimHold().Seconds())
	}
}
