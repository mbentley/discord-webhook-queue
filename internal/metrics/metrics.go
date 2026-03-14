package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all Prometheus metrics for the daemon.
type Metrics struct {
	QueueDepth     prometheus.GaugeFunc
	MessagesSent   prometheus.Counter
	MessagesFailed prometheus.Counter
	Retries        prometheus.Counter
	Healthy        prometheus.Gauge
}

// New registers and returns all Prometheus metrics.
// depthFn is called on each scrape to report the current queue depth.
func New(depthFn func() float64) *Metrics {
	m := &Metrics{
		QueueDepth: promauto.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "discord_queue_depth",
			Help: "Number of messages currently waiting in the queue.",
		}, depthFn),

		MessagesSent: promauto.NewCounter(prometheus.CounterOpts{
			Name: "discord_queue_messages_sent_total",
			Help: "Total number of messages successfully delivered to Discord.",
		}),

		MessagesFailed: promauto.NewCounter(prometheus.CounterOpts{
			Name: "discord_queue_messages_failed_total",
			Help: "Total number of failed delivery attempts (before retry).",
		}),

		Retries: promauto.NewCounter(prometheus.CounterOpts{
			Name: "discord_queue_retry_total",
			Help: "Total number of message retries (including rate-limit retries).",
		}),

		Healthy: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "discord_queue_healthy",
			Help: "1 if the daemon is in healthy delivery state, 0 if in probing mode.",
		}),
	}

	m.Healthy.Set(1)
	return m
}
