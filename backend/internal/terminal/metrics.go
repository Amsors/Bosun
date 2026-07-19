package terminal

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	registry    *prometheus.Registry
	connections prometheus.Gauge
	disconnects *prometheus.CounterVec
	dropped     *prometheus.CounterVec
}

func newMetrics() *Metrics {
	metrics := &Metrics{
		registry: prometheus.NewRegistry(),
		connections: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "bosun_terminal_connections",
			Help: "Current authenticated terminal WebSocket connections.",
		}),
		disconnects: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bosun_terminal_disconnects_total",
			Help: "Terminal WebSocket disconnects by stable reason.",
		}, []string{"reason"}),
		dropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bosun_terminal_dropped_frames_total",
			Help: "Terminal frames dropped when a bounded queue is full.",
		}, []string{"direction"}),
	}
	metrics.registry.MustRegister(metrics.connections, metrics.disconnects, metrics.dropped)
	return metrics
}

func (m *Metrics) handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
