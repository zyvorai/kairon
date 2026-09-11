// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package metrics exposes Prometheus metrics for kairon-controller's view of
// MachineMigration state -- the single cluster-wide vantage point, since
// Controller.Reconcile already lists every MachineMigration every tick.
package metrics

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/zyvorai/kairon/internal/model"
)

var terminalPhases = map[string]bool{"Succeeded": true, "Failed": true, "Blocked": true}

type phaseKey struct{ namespace, name string }

type phaseState struct {
	phase string
	since time.Time
}

// Recorder tracks MachineMigration metrics. Not a global var by design --
// callers construct one and pass it explicitly (e.g. Controller.Metrics),
// matching this codebase's existing style (Agent.MigrationPeer etc.).
type Recorder struct {
	registry *prometheus.Registry

	phaseCount         *prometheus.GaugeVec
	phaseAgeSeconds    *prometheus.GaugeVec
	completedTotal     *prometheus.CounterVec
	transferDuration   *prometheus.HistogramVec
	cutoverDowntime    prometheus.Histogram
	dataPlaneEncrypted *prometheus.GaugeVec

	mu         sync.Mutex
	phaseSince map[phaseKey]phaseState
	completed  map[phaseKey]bool
	now        func() time.Time
}

func NewRecorder() *Recorder {
	reg := prometheus.NewRegistry()
	r := &Recorder{
		registry: reg,
		phaseCount: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "kairon_migration_phase_count",
			Help: "Number of MachineMigrations currently in each phase.",
		}, []string{"phase"}),
		phaseAgeSeconds: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "kairon_migration_phase_age_seconds",
			Help: "Seconds a MachineMigration has been in its current phase.",
		}, []string{"namespace", "name", "phase"}),
		completedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kairon_migration_completed_total",
			Help: "Total MachineMigrations that reached a terminal phase, by result.",
		}, []string{"result"}),
		transferDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "kairon_migration_transfer_duration_seconds",
			Help:    "Live migration transfer duration (status.totalTimeMs) for migrations that reached Succeeded or Failed.",
			Buckets: prometheus.DefBuckets,
		}, []string{"result"}),
		cutoverDowntime: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "kairon_migration_cutover_downtime_seconds",
			Help:    "Live migration cutover downtime (status.downtimeMs) for migrations that reached Succeeded or Failed.",
			Buckets: prometheus.DefBuckets,
		}),
		dataPlaneEncrypted: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "kairon_migration_dataplane_encrypted",
			Help: "1 if an active live migration's QEMU data-plane transport is TLS-encrypted, 0 otherwise.",
		}, []string{"namespace", "name"}),
		phaseSince: map[phaseKey]phaseState{},
		completed:  map[phaseKey]bool{},
		now:        time.Now,
	}
	reg.MustRegister(r.phaseCount, r.phaseAgeSeconds, r.completedTotal, r.transferDuration, r.cutoverDowntime, r.dataPlaneEncrypted)
	return r
}

// Handler serves the Prometheus exposition format for this Recorder's own
// registry (not the default global one, so tests stay hermetic).
func (r *Recorder) Handler() http.Handler {
	return promhttp.HandlerFor(r.registry, promhttp.HandlerOpts{})
}

// ObserveMigrations is a pure function of the current migration list plus
// wall-clock time -- call it once per reconcile tick (Controller.Reconcile,
// right after ListMachineMigrations) rather than scattering Record*() calls
// across every PatchMachineMigrationStatus call site in controller.go/agent.go.
func (r *Recorder) ObserveMigrations(migrations []model.MachineMigration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	counts := map[string]int{}
	seen := map[phaseKey]bool{}
	r.dataPlaneEncrypted.Reset()

	for _, m := range migrations {
		phase := m.Status.Phase
		if phase == "" {
			phase = "Pending"
		}
		counts[phase]++

		key := phaseKey{m.Namespace(), m.Metadata.Name}
		seen[key] = true

		state, ok := r.phaseSince[key]
		if !ok || state.phase != phase {
			state = phaseState{phase: phase, since: now}
			r.phaseSince[key] = state
		}
		r.phaseAgeSeconds.WithLabelValues(key.namespace, key.name, phase).Set(now.Sub(state.since).Seconds())

		if terminalPhases[phase] && !r.completed[key] {
			r.completed[key] = true
			result := strings.ToLower(phase)
			r.completedTotal.WithLabelValues(result).Inc()
			if phase == "Succeeded" || phase == "Failed" {
				r.transferDuration.WithLabelValues(result).Observe(float64(m.Status.TotalTimeMs) / 1000)
				if m.Status.DowntimeMs > 0 {
					r.cutoverDowntime.Observe(float64(m.Status.DowntimeMs) / 1000)
				}
			}
		}

		if phase == "Starting" || phase == "Running" {
			encrypted := 0.0
			if m.Status.DataPlaneEncrypted {
				encrypted = 1
			}
			r.dataPlaneEncrypted.WithLabelValues(key.namespace, key.name).Set(encrypted)
		}
	}

	for key := range r.phaseSince {
		if !seen[key] {
			delete(r.phaseSince, key)
			delete(r.completed, key)
		}
	}

	r.phaseCount.Reset()
	for phase, n := range counts {
		r.phaseCount.WithLabelValues(phase).Set(float64(n))
	}
}
