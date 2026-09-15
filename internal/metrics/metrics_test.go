// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/zyvorai/kairon/internal/model"
)

func migration(ns, name, phase string) model.MachineMigration {
	return model.MachineMigration{
		Metadata: model.ObjectMeta{Name: name, Namespace: ns},
		Status:   model.MachineMigrationStatus{Phase: phase},
	}
}

func TestObserveMigrationsCountsByPhase(t *testing.T) {
	r := NewRecorder()
	r.ObserveMigrations([]model.MachineMigration{
		migration("prod", "a", "Running"),
		migration("prod", "b", "Running"),
		migration("prod", "c", "NeedsRecovery"),
	})
	if got := testutil.ToFloat64(r.phaseCount.WithLabelValues("Running")); got != 2 {
		t.Errorf("Running count = %v, want 2", got)
	}
	if got := testutil.ToFloat64(r.phaseCount.WithLabelValues("NeedsRecovery")); got != 1 {
		t.Errorf("NeedsRecovery count = %v, want 1", got)
	}
}

func TestObserveMigrationsTracksPhaseAgeAcrossTicks(t *testing.T) {
	r := NewRecorder()
	tick := 0
	r.now = func() time.Time {
		tick++
		return time.Unix(int64(tick*10), 0)
	}
	m := migration("prod", "a", "Running")
	r.ObserveMigrations([]model.MachineMigration{m}) // tick 1: since=10
	r.ObserveMigrations([]model.MachineMigration{m}) // tick 2: same phase, age = 20-10=10
	if got := testutil.ToFloat64(r.phaseAgeSeconds.WithLabelValues("prod", "a", "Running")); got != 10 {
		t.Errorf("phase age = %v, want 10", got)
	}

	// Phase changes -- age resets to 0 at the tick it changes.
	m.Status.Phase = "Cutover"
	r.ObserveMigrations([]model.MachineMigration{m}) // tick 3: phase changed, since=30
	if got := testutil.ToFloat64(r.phaseAgeSeconds.WithLabelValues("prod", "a", "Cutover")); got != 0 {
		t.Errorf("phase age after transition = %v, want 0", got)
	}
}

func TestObserveMigrationsCountsCompletedOnceOnTerminalTransition(t *testing.T) {
	r := NewRecorder()
	m := migration("prod", "a", "Running")
	r.ObserveMigrations([]model.MachineMigration{m})
	m.Status.Phase = "Succeeded"
	m.Status.TotalTimeMs = 5000
	m.Status.DowntimeMs = 200
	r.ObserveMigrations([]model.MachineMigration{m})
	r.ObserveMigrations([]model.MachineMigration{m}) // still Succeeded -- must not double-count
	if got := testutil.ToFloat64(r.completedTotal.WithLabelValues("succeeded")); got != 1 {
		t.Errorf("completed count = %v, want 1 (no double-counting)", got)
	}
}

func TestObserveMigrationsPrunesRemovedMigrations(t *testing.T) {
	r := NewRecorder()
	r.ObserveMigrations([]model.MachineMigration{migration("prod", "a", "Running")})
	if _, ok := r.phaseSince[phaseKey{"prod", "a"}]; !ok {
		t.Fatal("expected phaseSince to track the migration")
	}
	r.ObserveMigrations([]model.MachineMigration{}) // migration gone (e.g. list no longer returns it -- shouldn't normally happen, but prune defensively)
	if _, ok := r.phaseSince[phaseKey{"prod", "a"}]; ok {
		t.Error("expected phaseSince entry to be pruned once the migration disappears")
	}
}

func TestObserveMigrationsSetsDataPlaneEncryptedForActivePhasesOnly(t *testing.T) {
	r := NewRecorder()
	m := migration("prod", "a", "Starting")
	m.Status.DataPlaneEncrypted = true
	r.ObserveMigrations([]model.MachineMigration{m})
	if got := testutil.ToFloat64(r.dataPlaneEncrypted.WithLabelValues("prod", "a")); got != 1 {
		t.Errorf("dataPlaneEncrypted = %v, want 1", got)
	}
}

func TestHandlerServesPrometheusExposition(t *testing.T) {
	r := NewRecorder()
	r.ObserveMigrations([]model.MachineMigration{migration("prod", "a", "Running")})
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "kairon_migration_phase_count") {
		t.Error("expected kairon_migration_phase_count in exposition output")
	}
}

func TestObserveReconcileRecordsDurationAndErrors(t *testing.T) {
	r := NewRecorder()
	r.ObserveReconcile(50*time.Millisecond, nil)
	r.ObserveReconcile(10*time.Millisecond, errors.New("boom"))
	if got := testutil.ToFloat64(r.reconcileErrors); got != 1 {
		t.Errorf("reconcileErrors = %v, want 1", got)
	}
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "kairon_reconcile_duration_seconds_count 2") {
		t.Errorf("expected 2 reconcile duration samples in:\n%s", rec.Body.String())
	}
}

func TestObserveWebhookDecisionCountsByResourceOperationAndDecision(t *testing.T) {
	r := NewRecorder()
	r.ObserveWebhookDecision("machines", "CREATE", false)
	r.ObserveWebhookDecision("machines", "CREATE", false)
	r.ObserveWebhookDecision("machinemigrations", "CREATE", true)
	if got := testutil.ToFloat64(r.webhookDecisions.WithLabelValues("machines", "CREATE", "deny")); got != 2 {
		t.Errorf("deny count = %v, want 2", got)
	}
	if got := testutil.ToFloat64(r.webhookDecisions.WithLabelValues("machinemigrations", "CREATE", "allow")); got != 1 {
		t.Errorf("allow count = %v, want 1", got)
	}
}

func TestObserveAPIRequestCountsOkAndError(t *testing.T) {
	r := NewRecorder()
	r.ObserveAPIRequest("GET", 5*time.Millisecond, nil)
	r.ObserveAPIRequest("GET", 5*time.Millisecond, errors.New("boom"))
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `kairon_apiserver_request_duration_seconds_count{method="GET",outcome="ok"} 1`) {
		t.Errorf("missing ok sample in:\n%s", body)
	}
	if !strings.Contains(body, `kairon_apiserver_request_duration_seconds_count{method="GET",outcome="error"} 1`) {
		t.Errorf("missing error sample in:\n%s", body)
	}
}

func TestObserveHTTPRequestBucketsByStatusClass(t *testing.T) {
	r := NewUIRecorder()
	r.ObserveHTTPRequest("GET", "/api/v1/machines/{namespace}/{name}", 200, time.Millisecond)
	r.ObserveHTTPRequest("POST", "/api/v1/machines", 500, time.Millisecond)
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `kairon_ui_request_duration_seconds_count{method="GET",route="/api/v1/machines/{namespace}/{name}",status_class="2xx"} 1`) {
		t.Errorf("missing 2xx sample in:\n%s", body)
	}
	if !strings.Contains(body, `kairon_ui_request_duration_seconds_count{method="POST",route="/api/v1/machines",status_class="5xx"} 1`) {
		t.Errorf("missing 5xx sample in:\n%s", body)
	}
}

// TestNodeRecorderOmitsControllerOnlyMetrics confirms NewNodeRecorder
// doesn't expose migration-lifecycle or webhook metrics it has no way to
// keep meaningful -- and that its Observe* methods are still safe to call
// (no-op) for the metrics it didn't register.
func TestNodeRecorderOmitsControllerOnlyMetrics(t *testing.T) {
	r := NewNodeRecorder()
	r.ObserveMigrations([]model.MachineMigration{migration("prod", "a", "Running")}) // no-op: phaseCount is nil
	r.ObserveWebhookDecision("machines", "CREATE", false)                            // no-op: webhookDecisions is nil
	r.ObserveReconcile(time.Millisecond, nil)
	r.ObserveAPIRequest("GET", time.Millisecond, nil)

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, "kairon_migration_phase_count") {
		t.Error("NewNodeRecorder should not expose migration-lifecycle metrics")
	}
	if strings.Contains(body, "kairon_webhook_decisions_total") {
		t.Error("NewNodeRecorder should not expose webhook decision metrics")
	}
	if !strings.Contains(body, "kairon_reconcile_duration_seconds") {
		t.Error("NewNodeRecorder should expose reconcile metrics")
	}
	if !strings.Contains(body, "kairon_apiserver_request_duration_seconds") {
		t.Error("NewNodeRecorder should expose apiserver call metrics")
	}
}

// TestUIRecorderOmitsReconcileAndMigrationMetrics mirrors
// TestNodeRecorderOmitsControllerOnlyMetrics for kairon-ui's smaller set.
func TestUIRecorderOmitsReconcileAndMigrationMetrics(t *testing.T) {
	r := NewUIRecorder()
	r.ObserveReconcile(time.Millisecond, nil) // no-op: reconcileDuration is nil
	r.ObserveHTTPRequest("GET", "/api/v1/overview", 200, time.Millisecond)
	r.ObserveAPIRequest("GET", time.Millisecond, nil)

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, "kairon_reconcile_duration_seconds") {
		t.Error("NewUIRecorder should not expose reconcile metrics")
	}
	if strings.Contains(body, "kairon_migration_phase_count") {
		t.Error("NewUIRecorder should not expose migration-lifecycle metrics")
	}
	if !strings.Contains(body, "kairon_ui_request_duration_seconds") {
		t.Error("NewUIRecorder should expose its own HTTP request metrics")
	}
	if !strings.Contains(body, "kairon_apiserver_request_duration_seconds") {
		t.Error("NewUIRecorder should expose apiserver call metrics")
	}
}
