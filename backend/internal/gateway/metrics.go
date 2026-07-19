package gateway

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	registry *prometheus.Registry
	requests *prometheus.CounterVec
	errors   *prometheus.CounterVec
	duration *prometheus.HistogramVec
	usage    *prometheus.CounterVec
}

func NewMetrics() *Metrics {
	metrics := &Metrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bosun_gateway_requests_total",
			Help: "Total number of LLM gateway requests.",
		}, []string{"provider", "status"}),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bosun_gateway_errors_total",
			Help: "Total number of LLM gateway errors by stable reason.",
		}, []string{"provider", "reason"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "bosun_gateway_request_duration_seconds",
			Help:    "LLM gateway request latency in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"provider"}),
		usage: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bosun_gateway_usage_tokens_total",
			Help: "LLM token usage reported by the upstream provider.",
		}, []string{"provider", "type"}),
	}
	metrics.registry.MustRegister(metrics.requests, metrics.errors, metrics.duration, metrics.usage)
	return metrics
}

func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}

func (m *Metrics) Observe(provider string, status int, reason string, elapsed time.Duration, usage tokenUsage) {
	m.requests.WithLabelValues(provider, strconv.Itoa(status)).Inc()
	m.duration.WithLabelValues(provider).Observe(elapsed.Seconds())
	if reason != "" {
		m.errors.WithLabelValues(provider, reason).Inc()
	}
	if usage.Input > 0 {
		m.usage.WithLabelValues(provider, "input").Add(float64(usage.Input))
	}
	if usage.Output > 0 {
		m.usage.WithLabelValues(provider, "output").Add(float64(usage.Output))
	}
}
