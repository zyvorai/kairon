// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package metrics exposes Prometheus metrics for Kairon's components.
// kairon-controller's Recorder (NewRecorder) additionally covers its
// unique cluster-wide view of MachineMigration state, since
// Controller.Reconcile already lists every MachineMigration every tick;
// kairon-node (NewNodeRecorder) and kairon-ui (NewUIRecorder) get a
// smaller, purpose-specific subset instead of the full controller set, so
// neither exposes migration-lifecycle metrics it has no way to keep
// meaningful (a node's /metrics permanently reporting
// kairon_migration_phase_count=0 would be misleading, not just unused).
// Every Recorder shares the same struct and Observe* methods; a method
// whose backing metric wasn't registered by the constructor that built
// this Recorder is simply a no-op (nil-checked), so callers never need to
// know which concrete subset they're holding.
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

// Recorder tracks Prometheus metrics for one Kairon component. Not a
// global var by design -- callers construct one and pass it explicitly
// (e.g. Controller.Metrics), matching this codebase's existing style
// (Agent.MigrationPeer etc.).
type Recorder struct {
	registry *prometheus.Registry

	// Migration-lifecycle metrics -- only registered by NewRecorder
	// (kairon-controller), see ObserveMigrations.
	phaseCount         *prometheus.GaugeVec
	phaseAgeSeconds    *prometheus.GaugeVec
	completedTotal     *prometheus.CounterVec
	transferDuration   *prometheus.HistogramVec
	cutoverDowntime    prometheus.Histogram
	dataPlaneEncrypted *prometheus.GaugeVec

	// Reconcile-loop metrics -- registered by NewRecorder and
	// NewNodeRecorder (kairon-controller/kairon-node both run one), see
	// ObserveReconcile.
	reconcileDuration prometheus.Histogram
	reconcileErrors   prometheus.Counter

	// Admission-webhook decision metrics -- only registered by
	// NewRecorder (kairon-node/kairon-ui don't serve the webhook), see
	// ObserveWebhookDecision.
	webhookDecisions *prometheus.CounterVec

	// Kubernetes apiserver call metrics -- registered by all three
	// constructors, since every component talks to the apiserver via
	// internal/kube.Client. See ObserveAPIRequest.
	apiRequestDuration *prometheus.HistogramVec

	// kairon-ui's own HTTP request metrics -- only registered by
	// NewUIRecorder. See ObserveHTTPRequest.
	uiRequestDuration *prometheus.HistogramVec

	mu         sync.Mutex
	phaseSince map[phaseKey]phaseState
	completed  map[phaseKey]bool
	now        func() time.Time
}

func newReconcileMetrics() (prometheus.Histogram, prometheus.Counter) {
	return prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "kairon_reconcile_duration_seconds",
			Help:    "Duration of one reconcile loop iteration.",
			Buckets: prometheus.DefBuckets,
		}), prometheus.NewCounter(prometheus.CounterOpts{
			Name: "kairon_reconcile_errors_total",
			Help: "Total reconcile loop iterations that returned an error.",
		})
}

func newAPIRequestDurationMetric() *prometheus.HistogramVec {
	return prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "kairon_apiserver_request_duration_seconds",
		Help:    "Kubernetes apiserver request duration, by HTTP method and outcome (ok/error).",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "outcome"})
}

// NewRecorder builds the full metric set kairon-controller uses: migration
// lifecycle, its own reconcile loop, admission-webhook decisions (if
// webhook.enabled), and apiserver call health.
func NewRecorder() *Recorder {
	reg := prometheus.NewRegistry()
	reconcileDuration, reconcileErrors := newReconcileMetrics()
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
		reconcileDuration: reconcileDuration,
		reconcileErrors:   reconcileErrors,
		webhookDecisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kairon_webhook_decisions_total",
			Help: "Admission webhook decisions, by resource, operation, and outcome (allow/deny).",
		}, []string{"resource", "operation", "decision"}),
		apiRequestDuration: newAPIRequestDurationMetric(),
		phaseSince:         map[phaseKey]phaseState{},
		completed:          map[phaseKey]bool{},
		now:                time.Now,
	}
	reg.MustRegister(r.phaseCount, r.phaseAgeSeconds, r.completedTotal, r.transferDuration, r.cutoverDowntime, r.dataPlaneEncrypted,
		r.reconcileDuration, r.reconcileErrors, r.webhookDecisions, r.apiRequestDuration)
	return r
}

// NewNodeRecorder builds the smaller metric set kairon-node uses: its own
// reconcile loop plus apiserver call health -- no migration-lifecycle or
// webhook metrics, since kairon-node reconciles individual Machines on one
// node, not MachineMigrations cluster-wide, and never serves the webhook.
func NewNodeRecorder() *Recorder {
	reg := prometheus.NewRegistry()
	reconcileDuration, reconcileErrors := newReconcileMetrics()
	r := &Recorder{
		registry:           reg,
		reconcileDuration:  reconcileDuration,
		reconcileErrors:    reconcileErrors,
		apiRequestDuration: newAPIRequestDurationMetric(),
	}
	reg.MustRegister(r.reconcileDuration, r.reconcileErrors, r.apiRequestDuration)
	return r
}

// NewUIRecorder builds the metric set kairon-ui uses: its own HTTP request
// health plus apiserver call health. kairon-ui has no reconcile loop or
// webhook of its own.
func NewUIRecorder() *Recorder {
	reg := prometheus.NewRegistry()
	r := &Recorder{
		registry:           reg,
		apiRequestDuration: newAPIRequestDurationMetric(),
		uiRequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "kairon_ui_request_duration_seconds",
			Help:    "kairon-ui HTTP request duration, by method and response status class.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "status_class"}),
	}
	reg.MustRegister(r.apiRequestDuration, r.uiRequestDuration)
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
	if r.phaseCount == nil {
		return
	}
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

// ObserveReconcile records one reconcile loop iteration's duration and,
// if err is non-nil, counts it as a failed iteration. Call once per tick
// from Controller.Run/Agent.Run, regardless of outcome -- a nil Recorder
// method receiver is never valid here (callers nil-check the *Recorder
// itself, e.g. `if c.Metrics != nil`), but a Recorder built by a
// constructor that didn't register these two metrics (there is none
// today -- every constructor does) would silently no-op via the nil
// field check below.
func (r *Recorder) ObserveReconcile(d time.Duration, err error) {
	if r.reconcileDuration == nil {
		return
	}
	r.reconcileDuration.Observe(d.Seconds())
	if err != nil {
		r.reconcileErrors.Inc()
	}
}

// ObserveWebhookDecision records one admission webhook decision. No-op on
// a Recorder that didn't register webhookDecisions (NewNodeRecorder/
// NewUIRecorder) -- neither kairon-node nor kairon-ui serves the webhook.
func (r *Recorder) ObserveWebhookDecision(resource, operation string, allowed bool) {
	if r.webhookDecisions == nil {
		return
	}
	decision := "deny"
	if allowed {
		decision = "allow"
	}
	r.webhookDecisions.WithLabelValues(resource, operation, decision).Inc()
}

// ObserveAPIRequest records one Kubernetes apiserver call. Wire it to
// internal/kube.Client.Observe (see cmd/*/main.go) -- every constructor
// registers apiRequestDuration, so this is safe to call from any
// component's Recorder.
func (r *Recorder) ObserveAPIRequest(method string, d time.Duration, err error) {
	if r.apiRequestDuration == nil {
		return
	}
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	r.apiRequestDuration.WithLabelValues(method, outcome).Observe(d.Seconds())
}

// ObserveHTTPRequest records one kairon-ui HTTP request. No-op on any
// Recorder but NewUIRecorder's.
func (r *Recorder) ObserveHTTPRequest(method string, status int, d time.Duration) {
	if r.uiRequestDuration == nil {
		return
	}
	class := "2xx"
	switch {
	case status >= 500:
		class = "5xx"
	case status >= 400:
		class = "4xx"
	case status >= 300:
		class = "3xx"
	}
	r.uiRequestDuration.WithLabelValues(method, class).Observe(d.Seconds())
}
