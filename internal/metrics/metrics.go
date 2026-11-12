package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sentoz/vk-scheduler/internal/executor"
)

// Metrics owns an isolated registry and low-cardinality operational counters.
type Metrics struct {
	Registry    *prometheus.Registry
	operations  *prometheus.CounterVec
	corrections *prometheus.CounterVec
	skips       *prometheus.CounterVec
	Pending     prometheus.Gauge
	Oldest      prometheus.Gauge
	Unconfirmed prometheus.Gauge
}

// New creates a private registry, avoiding global collector collisions.
func New() (*Metrics, error) {
	m := &Metrics{Registry: prometheus.NewRegistry(), operations: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "vk_scheduler_operations_total", Help: "Terminal outcomes of admitted resource intents."}, []string{"resource_type", "action", "status"}), corrections: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "vk_scheduler_validator_corrections_total", Help: "New correction intents accepted by the executor."}, []string{"resource_type", "action"}), skips: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "vk_scheduler_scheduler_skips_total", Help: "Skipped intents by bounded reason."}, []string{"resource_type", "action", "reason"}), Pending: prometheus.NewGauge(prometheus.GaugeOpts{Name: "vk_scheduler_pending_resources", Help: "Resources waiting for delivery."}), Oldest: prometheus.NewGauge(prometheus.GaugeOpts{Name: "vk_scheduler_oldest_pending_seconds", Help: "Oldest pending intent age."}), Unconfirmed: prometheus.NewGauge(prometheus.GaugeOpts{Name: "vk_scheduler_unconfirmed_operations", Help: "Process-local operations with unknown outcomes."})}
	for _, c := range []prometheus.Collector{m.operations, m.corrections, m.skips, m.Pending, m.Oldest, m.Unconfirmed} {
		if err := m.Registry.Register(c); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// Outcome records one terminal intent result.
func (m *Metrics) Outcome(kind string, out executor.Outcome) {
	if out.Decision.Action == "" {
		out.Decision.Action = "unknown"
	}
	m.operations.WithLabelValues(kind, out.Decision.Action, out.Status).Inc()
	if out.Status == "skipped" {
		m.skips.WithLabelValues(kind, out.Decision.Action, out.Reason).Inc()
	}
}

// Correction records a newly accepted correction.
func (m *Metrics) Correction(kind, action string) { m.corrections.WithLabelValues(kind, action).Inc() }
