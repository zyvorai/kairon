// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kaironctl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

func TestMigrationStillPending(t *testing.T) {
	for phase, want := range map[string]bool{
		"":          true, // not yet reconciled -- must NOT be treated as safe to recreate
		"Starting":  true,
		"Running":   true,
		"Cutover":   true,
		"Succeeded": false,
		"Failed":    false,
		"Blocked":   false,
	} {
		if got := migrationStillPending(phase); got != want {
			t.Errorf("migrationStillPending(%q) = %v, want %v", phase, got, want)
		}
	}
}

// evacuateTestServer is a minimal in-memory fake of the endpoints
// evacuatePass calls, mirroring the inline-httptest-server convention
// already used throughout internal/controller's own tests.
type evacuateTestServer struct {
	machines   []model.Machine
	migrations []model.MachineMigration
	budgets    []model.MachineDisruptionBudget
	created    []model.MachineMigration
}

func (s *evacuateTestServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: s.machines})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: s.migrations})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinedisruptionbudgets":
			_ = json.NewEncoder(w).Encode(model.MachineDisruptionBudgetList{Items: s.budgets})
		case r.Method == http.MethodPost && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinemigrations":
			var m model.MachineMigration
			_ = json.NewDecoder(r.Body).Decode(&m)
			s.created = append(s.created, m)
			_ = json.NewEncoder(w).Encode(m)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
}

func evacuateMachine(name string) model.Machine {
	return model.Machine{Metadata: model.ObjectMeta{Name: name, Namespace: "default"}, Spec: model.MachineSpec{NodeName: "worker-1"}, Status: model.MachineStatus{Phase: "Running"}}
}

func TestEvacuatePassCreatesMigrationsForEligibleMachines(t *testing.T) {
	s := &evacuateTestServer{machines: []model.Machine{evacuateMachine("vm-1"), evacuateMachine("vm-2")}}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	kc.HTTP = srv.Client()

	created, skipped, remaining, err := evacuatePass(context.Background(), kc, "worker-1", "cold")
	if err != nil {
		t.Fatalf("evacuatePass: %v", err)
	}
	if created != 2 || skipped != 0 || remaining != 2 {
		t.Fatalf("created=%d skipped=%d remaining=%d, want 2/0/2", created, skipped, remaining)
	}
	if len(s.created) != 2 {
		t.Fatalf("expected 2 MachineMigrations created, got %d", len(s.created))
	}
}

func TestEvacuatePassSkipsMachineAlreadyMidMigrationInsteadOfDuplicating(t *testing.T) {
	s := &evacuateTestServer{
		machines: []model.Machine{evacuateMachine("vm-1")},
		migrations: []model.MachineMigration{
			{Metadata: model.ObjectMeta{Namespace: "default", Name: "existing"}, Spec: model.MachineMigrationSpec{MachineName: "vm-1"}, Status: model.MachineMigrationStatus{Phase: "Running"}},
		},
	}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	kc.HTTP = srv.Client()

	created, skipped, remaining, err := evacuatePass(context.Background(), kc, "worker-1", "cold")
	if err != nil {
		t.Fatalf("evacuatePass: %v", err)
	}
	// Already mid-migration: not created again, not counted as
	// budget-skipped either, but still remaining (hasn't left the node yet).
	if created != 0 || skipped != 0 || remaining != 1 {
		t.Fatalf("created=%d skipped=%d remaining=%d, want 0/0/1", created, skipped, remaining)
	}
	if len(s.created) != 0 {
		t.Fatalf("expected no new MachineMigration, got %d", len(s.created))
	}
}

// TestEvacuatePassSkipsMachineWithFreshlyCreatedMigrationTooEmptyPhase is a
// regression test for a real bug found running evacuate twice in quick
// succession against a real cluster: a MachineMigration's status.phase is
// "" until kairon-controller's own reconcile loop picks it up (up to one
// reconcile interval later), and an earlier version of migrationStillPending
// (copy-pasted from a different helper with different semantics) treated
// "" as terminal, so a fast second evacuate call created a real duplicate
// migration for the same Machine.
func TestEvacuatePassSkipsMachineWithFreshlyCreatedMigrationTooEmptyPhase(t *testing.T) {
	s := &evacuateTestServer{
		machines: []model.Machine{evacuateMachine("vm-1")},
		migrations: []model.MachineMigration{
			{Metadata: model.ObjectMeta{Namespace: "default", Name: "existing"}, Spec: model.MachineMigrationSpec{MachineName: "vm-1"}, Status: model.MachineMigrationStatus{Phase: ""}},
		},
	}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	kc.HTTP = srv.Client()

	created, _, remaining, err := evacuatePass(context.Background(), kc, "worker-1", "cold")
	if err != nil {
		t.Fatalf("evacuatePass: %v", err)
	}
	if created != 0 || remaining != 1 {
		t.Fatalf("created=%d remaining=%d, want 0/1", created, remaining)
	}
	if len(s.created) != 0 {
		t.Fatalf("expected no new MachineMigration for a Machine with a pending (phase=\"\") migration already, got %d", len(s.created))
	}
}

func TestEvacuatePassSkipsMachineBlockedByDisruptionBudget(t *testing.T) {
	blocked := evacuateMachine("vm-1")
	blocked.Metadata.Labels = map[string]string{"tier": "web"}
	s := &evacuateTestServer{
		machines: []model.Machine{blocked},
		budgets: []model.MachineDisruptionBudget{
			{Metadata: model.ObjectMeta{Name: "pdb", Namespace: "default"}, Spec: model.MachineDisruptionBudgetSpec{Selector: map[string]string{"tier": "web"}, MinAvailable: "1"}},
		},
	}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	kc.HTTP = srv.Client()

	created, skipped, remaining, err := evacuatePass(context.Background(), kc, "worker-1", "cold")
	if err != nil {
		t.Fatalf("evacuatePass: %v", err)
	}
	if created != 0 || skipped != 1 || remaining != 1 {
		t.Fatalf("created=%d skipped=%d remaining=%d, want 0/1/1", created, skipped, remaining)
	}
}

func TestEvacuatePassIgnoresMachinesOnOtherNodes(t *testing.T) {
	other := evacuateMachine("vm-elsewhere")
	other.Spec.NodeName = "worker-2"
	s := &evacuateTestServer{machines: []model.Machine{other}}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	kc.HTTP = srv.Client()

	created, skipped, remaining, err := evacuatePass(context.Background(), kc, "worker-1", "cold")
	if err != nil {
		t.Fatalf("evacuatePass: %v", err)
	}
	if created != 0 || skipped != 0 || remaining != 0 {
		t.Fatalf("created=%d skipped=%d remaining=%d, want 0/0/0", created, skipped, remaining)
	}
}

func TestResourceKindAndName(t *testing.T) {
	// A single positional arg is NAME alone, kind defaults to "machine" --
	// preserves the exact prior `describe NAME`/`delete NAME` calling
	// convention untouched by this change.
	kind, name := resourceKindAndName("delete", []string{"my-vm"})
	if kind != "machine" || name != "my-vm" {
		t.Fatalf("kind=%q name=%q, want machine/my-vm", kind, name)
	}
	// Two positional args are KIND NAME -- disambiguated purely by count,
	// never by matching NAME's own text against the alias list, so a
	// Machine literally named e.g. "snapshot" is never misrouted.
	kind, name = resourceKindAndName("delete", []string{"snapshot", "my-snap"})
	if kind != "snapshot" || name != "my-snap" {
		t.Fatalf("kind=%q name=%q, want snapshot/my-snap", kind, name)
	}
	kind, name = resourceKindAndName("describe", []string{"snapshot", "snapshot"})
	if kind != "snapshot" || name != "snapshot" {
		t.Fatalf("kind=%q name=%q, want snapshot/snapshot (a resource literally named after its own kind)", kind, name)
	}
}
