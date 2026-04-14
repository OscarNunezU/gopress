// Package telemetry configures Prometheus metrics and OpenTelemetry tracing.
package telemetry

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	ConversionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gopress_conversions_total",
			Help: "Total number of HTML to PDF conversions.",
		},
		[]string{"status"},
	)

	ConversionDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "gopress_conversion_duration_seconds",
			Help:    "Duration of HTML to PDF conversions.",
			Buckets: []float64{0.1, 0.25, 0.5, 1.0, 2.0, 5.0, 10.0},
		},
		[]string{"status"},
	)

	PoolQueueSize = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "gopress_pool_queue_size",
		Help: "Number of conversion jobs waiting in the pool queue.",
	})

	PoolFreeInstances = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "gopress_pool_free_instances",
		Help: "Number of idle Chromium instances in the pool.",
	})
)

// Register registers all gopress metrics with the default Prometheus registry.
func Register() {
	prometheus.MustRegister(
		ConversionsTotal,
		ConversionDuration,
		PoolQueueSize,
		PoolFreeInstances,
	)
}

// Handler returns the Prometheus metrics HTTP handler.
func Handler() http.Handler {
	return promhttp.Handler()
}
