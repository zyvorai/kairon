// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
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
