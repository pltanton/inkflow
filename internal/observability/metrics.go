// Package observability provides the service's isolated Prometheus registry.
package observability

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"inkflow/internal/ai"
)

// Metrics owns all service telemetry. It deliberately uses a private registry
// so tests and embedders do not mutate Prometheus's process-global registry.
type Metrics struct {
	Registry   *prometheus.Registry
	imports    *prometheus.CounterVec
	dedup      prometheus.Counter
	aiCalls    *prometheus.CounterVec
	aiLatency  *prometheus.HistogramVec
	queueDepth prometheus.Gauge
	records    prometheus.Gauge
}

func New() *Metrics {
	m := &Metrics{Registry: prometheus.NewRegistry()}
	m.imports = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "inkflow_imports_total", Help: "Completed import requests."}, []string{"route", "outcome"})
	m.dedup = prometheus.NewCounter(prometheus.CounterOpts{Name: "inkflow_dedup_skips_total", Help: "Imports skipped because the PDF was already imported."})
	m.aiCalls = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "inkflow_ai_calls_total", Help: "AI provider calls."}, []string{"provider", "outcome", "status_class"})
	m.aiLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "inkflow_ai_call_duration_seconds", Help: "AI provider call duration."}, []string{"provider"})
	m.queueDepth = prometheus.NewGauge(prometheus.GaugeOpts{Name: "inkflow_retry_queue_depth", Help: "Pending and failed AI records awaiting worker evaluation."})
	m.records = prometheus.NewGauge(prometheus.GaugeOpts{Name: "inkflow_state_records", Help: "Records in the state store."})
	m.Registry.MustRegister(m.imports, m.dedup, m.aiCalls, m.aiLatency, m.queueDepth, m.records)
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}
func (m *Metrics) Import(route, outcome string) {
	if m != nil {
		m.imports.WithLabelValues(route, outcome).Inc()
	}
}
func (m *Metrics) DedupSkip() {
	if m != nil {
		m.dedup.Inc()
	}
}
func (m *Metrics) AICall(provider, outcome, statusClass string, duration time.Duration) {
	if m != nil {
		m.aiCalls.WithLabelValues(provider, outcome, statusClass).Inc()
		m.aiLatency.WithLabelValues(provider).Observe(duration.Seconds())
	}
}
func (m *Metrics) QueueDepth(depth int) {
	if m != nil {
		m.queueDepth.Set(float64(depth))
	}
}
func (m *Metrics) RecordCount(count int) {
	if m != nil {
		m.records.Set(float64(count))
	}
}

// WrapProvider records every provider invocation, including calls made by the
// asynchronous retry worker, without coupling provider implementations to
// Prometheus.
func WrapProvider(provider string, next ai.Provider, metrics *Metrics) ai.Provider {
	if next == nil || metrics == nil {
		return next
	}
	return providerObserver{provider: provider, next: next, metrics: metrics}
}

type providerObserver struct {
	provider string
	next     ai.Provider
	metrics  *Metrics
}

func (p providerObserver) Process(ctx context.Context, pdf io.Reader) (ai.Result, error) {
	started := time.Now()
	result, err := p.next.Process(ctx, pdf)
	outcome, statusClass := "success", "2xx"
	if err != nil {
		outcome, statusClass = "failure", "unknown"
		var apiErr *ai.APIError
		if errors.As(err, &apiErr) {
			statusClass = string(rune('0'+apiErr.StatusCode/100)) + "xx"
		}
	}
	p.metrics.AICall(p.provider, outcome, statusClass, time.Since(started))
	return result, err
}
