package main

import (
	"github.com/prometheus/client_golang/prometheus"

	notificationapplication "video-processor/internal/notification/application"
	"video-processor/internal/platform/metrics"
)

// deliveryDuration times one terminal event's whole run through
// DeliverNotification.Execute -- claiming, attempting and resolving every
// deliverable preference in turn -- by the disposition handle() reports to
// the consumer. Per docs/roadmap.md's expose-worker-and-notifier-metrics
// row, this is the delivery-attempt histogram to compare against
// deliveryMaxClaimHoldSeconds (set once below): an event resolving to every
// channel in domain.AllChannels() legitimately costs one MaxClaimHold() each,
// so this is the only way to see that budget being approached before a run
// actually exceeds it and the consumer's own drain has to give up on it.
var deliveryDuration = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "fiapx_notifier_event_delivery_duration_seconds",
		Help:    "Time spent handling one terminal event's deliveries, by disposition.",
		Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30, 45, 60, 90, 120, 180},
	},
	[]string{"disposition"},
)

// deliveryMaxClaimHoldSeconds reports the configured per-channel claim-hold
// budget deliveryDuration is measured against, so the two can sit on one
// dashboard without an operator having to know DeliveryConfig.MaxClaimHold's
// arithmetic by heart. Set once at startup: the budget is fixed for the
// process's life, and setupNotifier's call to Validate has already refused
// it if the reclaim bound could never honour it.
var deliveryMaxClaimHoldSeconds = prometheus.NewGauge(
	prometheus.GaugeOpts{
		Name: "fiapx_notifier_delivery_max_claim_hold_seconds",
		Help: "The configured per-channel claim-hold budget (DeliveryConfig.MaxClaimHold), for comparison against fiapx_notifier_event_delivery_duration_seconds.",
	},
)

func init() {
	metrics.MustRegister(deliveryDuration, deliveryMaxClaimHoldSeconds)
}

// recordDeliveryMaxClaimHold publishes the configured budget once, called
// from setupNotifier after DeliveryConfig has been loaded and validated.
func recordDeliveryMaxClaimHold(config notificationapplication.DeliveryConfig) {
	deliveryMaxClaimHoldSeconds.Set(config.MaxClaimHold().Seconds())
}
