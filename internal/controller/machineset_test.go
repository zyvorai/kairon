// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

// machineSetTestServer is a minimal fake apiserver serving exactly what
// reconcileMachineSet calls: creating/deleting Machines and patching a
// MachineSet's status. Mirrors cordonTestServer's own inline-httptest
// convention.
type machineSetTestServer struct {
	mu       sync.Mutex
	created  []model.Machine
	deleted  []string
	patched  model.MachineSetStatus
	hadPatch bool
}

func newMachineSetTestController(t *testing.T) (*Controller, *machineSetTestServer) {
	t.Helper()
	fake := &machineSetTestServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines":
			var m model.Machine
			_ = json.NewDecoder(r.Body).Decode(&m)
			fake.created = append(fake.created, m)
			_ = json.NewEncoder(w).Encode(m)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/"):
			name := strings.TrimPrefix(r.URL.Path, "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/")
			fake.deleted = append(fake.deleted, name)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesets/ms1/status":
			var body struct {
				Status model.MachineSetStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			fake.patched = body.Status
			fake.hadPatch = true
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	kc.HTTP = srv.Client()
	return &Controller{Kube: kc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, fake
}

func testMachineSet(replicas int, strategy, maxUnavailable string) model.MachineSet {
	return model.MachineSet{
		Metadata: model.ObjectMeta{Name: "ms1", Namespace: "prod"},
		Spec: model.MachineSetSpec{
			Replicas:       replicas,
			Strategy:       strategy,
			MaxUnavailable: maxUnavailable,
			Template:       model.MachineTemplate{Spec: model.MachineSpec{Resources: model.ResourceSpec{CPU: "1", Memory: "1Gi"}}},
		},
	}
}

func ownedMachine(name, templateHash, phase string) model.Machine {
	return model.Machine{
		Metadata: model.ObjectMeta{Name: name, Namespace: "prod", Labels: map[string]string{
			model.LabelMachineSet:             "ms1",
			model.LabelMachineSetTemplateHash: templateHash,
		}},
		Status: model.MachineStatus{Phase: phase},
	}
}

func TestMachineSetTemplateHashIsStableAndSensitiveToChange(t *testing.T) {
	t1 := model.MachineTemplate{Spec: model.MachineSpec{Resources: model.ResourceSpec{CPU: "1"}}}
	t2 := model.MachineTemplate{Spec: model.MachineSpec{Resources: model.ResourceSpec{CPU: "2"}}}
	if machineSetTemplateHash(t1) != machineSetTemplateHash(t1) {
		t.Fatal("expected the same template to hash the same way twice")
	}
	if machineSetTemplateHash(t1) == machineSetTemplateHash(t2) {
		t.Fatal("expected different templates to hash differently")
	}
}

func TestReconcileMachineSetCreatesMissingReplicas(t *testing.T) {
	ctl, fake := newMachineSetTestController(t)
	ms := testMachineSet(3, "", "")
	if err := ctl.reconcileMachineSet(context.Background(), ms, nil); err != nil {
		t.Fatalf("reconcileMachineSet: %v", err)
	}
	if len(fake.created) != 3 {
		t.Fatalf("expected 3 created Machines, got %d", len(fake.created))
	}
	for _, m := range fake.created {
		hash := machineSetTemplateHash(ms.Spec.Template)
		if m.Metadata.Labels[model.LabelMachineSet] != "ms1" || m.Metadata.Labels[model.LabelMachineSetTemplateHash] != hash {
			t.Fatalf("expected created Machine to carry machineset labels, got %+v", m.Metadata.Labels)
		}
	}
	if !fake.hadPatch || fake.patched.Replicas != 0 {
		t.Fatalf("expected a status patch reflecting the pre-create owned count, got %+v", fake.patched)
	}
}

func TestReconcileMachineSetScalesDownExcessCurrentReplicas(t *testing.T) {
	ctl, fake := newMachineSetTestController(t)
	ms := testMachineSet(1, "", "")
	hash := machineSetTemplateHash(ms.Spec.Template)
	owned := []model.Machine{ownedMachine("ms1-a", hash, "Running"), ownedMachine("ms1-b", hash, "Running"), ownedMachine("ms1-c", hash, "Running")}
	if err := ctl.reconcileMachineSet(context.Background(), ms, owned); err != nil {
		t.Fatalf("reconcileMachineSet: %v", err)
	}
	if len(fake.deleted) != 2 {
		t.Fatalf("expected 2 deletions to scale 3 -> 1, got %d: %v", len(fake.deleted), fake.deleted)
	}
	if len(fake.created) != 0 {
		t.Fatal("expected no creations on a pure scale-down")
	}
}

func TestReconcileMachineSetIgnoresMachinesFromOtherSets(t *testing.T) {
	ctl, fake := newMachineSetTestController(t)
	ms := testMachineSet(1, "", "")
	unrelated := model.Machine{Metadata: model.ObjectMeta{Name: "other", Namespace: "prod", Labels: map[string]string{model.LabelMachineSet: "ms2"}}}
	if err := ctl.reconcileMachineSet(context.Background(), ms, []model.Machine{unrelated}); err != nil {
		t.Fatalf("reconcileMachineSet: %v", err)
	}
	if len(fake.created) != 1 || len(fake.deleted) != 0 {
		t.Fatalf("expected exactly 1 create and no deletes (unrelated Machine untouched), got created=%d deleted=%d", len(fake.created), len(fake.deleted))
	}
}

func TestStepMachineSetTowardRecreateDeletesOutdatedBeforeCreating(t *testing.T) {
	ctl, fake := newMachineSetTestController(t)
	ms := testMachineSet(2, "Recreate", "")
	current := []model.Machine{ownedMachine("ms1-a", "newhash", "Running")}
	outdated := []model.Machine{ownedMachine("ms1-b", "oldhash", "Running")}
	if err := ctl.stepMachineSetToward(context.Background(), ms, "newhash", "Recreate", 2, current, outdated); err != nil {
		t.Fatalf("stepMachineSetToward: %v", err)
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != "ms1-b" {
		t.Fatalf("expected the outdated replica to be deleted first, got %v", fake.deleted)
	}
	if len(fake.created) != 0 {
		t.Fatal("expected no creation until every outdated replica is gone")
	}
}

func TestStepMachineSetTowardRollingUpdateBoundsByMaxUnavailable(t *testing.T) {
	ctl, fake := newMachineSetTestController(t)
	ms := testMachineSet(5, "RollingUpdate", "2")
	var current, outdated []model.Machine
	for i := 0; i < 3; i++ {
		current = append(current, ownedMachine("cur-"+string(rune('a'+i)), "newhash", "Running"))
	}
	for i := 0; i < 2; i++ {
		outdated = append(outdated, ownedMachine("old-"+string(rune('a'+i)), "oldhash", "Running"))
	}
	// total=5 (matches desired), maxUnavailable=2 -> canDelete = 5-(5-2) = 2
	if err := ctl.stepMachineSetToward(context.Background(), ms, "newhash", "RollingUpdate", 5, current, outdated); err != nil {
		t.Fatalf("stepMachineSetToward: %v", err)
	}
	if len(fake.deleted) != 2 {
		t.Fatalf("expected exactly 2 outdated replicas deleted (maxUnavailable bound), got %d: %v", len(fake.deleted), fake.deleted)
	}
}

func TestStepMachineSetTowardRollingUpdateMakesProgressWhenOutdatedExceedsMaxUnavailable(t *testing.T) {
	// Regression guard: outdated count (5) exceeding maxUnavailable (1)
	// must not deadlock -- exactly maxUnavailable get deleted this step.
	ctl, fake := newMachineSetTestController(t)
	ms := testMachineSet(5, "RollingUpdate", "1")
	var outdated []model.Machine
	for i := 0; i < 5; i++ {
		outdated = append(outdated, ownedMachine("old-"+string(rune('a'+i)), "oldhash", "Running"))
	}
	if err := ctl.stepMachineSetToward(context.Background(), ms, "newhash", "RollingUpdate", 5, nil, outdated); err != nil {
		t.Fatalf("stepMachineSetToward: %v", err)
	}
	if len(fake.deleted) != 1 {
		t.Fatalf("expected exactly 1 deletion (maxUnavailable=1), got %d: %v", len(fake.deleted), fake.deleted)
	}
}

func TestStepMachineSetTowardUnderProvisionedAlwaysCreatesRegardlessOfStrategy(t *testing.T) {
	ctl, fake := newMachineSetTestController(t)
	ms := testMachineSet(4, "RollingUpdate", "1")
	current := []model.Machine{ownedMachine("cur-a", "newhash", "Running")}
	if err := ctl.stepMachineSetToward(context.Background(), ms, "newhash", "RollingUpdate", 4, current, nil); err != nil {
		t.Fatalf("stepMachineSetToward: %v", err)
	}
	if len(fake.created) != 3 {
		t.Fatalf("expected 3 creations to fill the gap (4 desired - 1 current), got %d", len(fake.created))
	}
}

func TestResolveMaxUnavailableDefaultsToOne(t *testing.T) {
	n, err := resolveMaxUnavailable("", 10)
	if err != nil || n != 1 {
		t.Fatalf("expected default 1, got %d, err %v", n, err)
	}
}

func TestResolveMaxUnavailableParsesPercent(t *testing.T) {
	n, err := resolveMaxUnavailable("50%", 10)
	if err != nil || n != 5 {
		t.Fatalf("expected 5 for 50%% of 10, got %d, err %v", n, err)
	}
}

func TestResolveMaxUnavailableRejectsInvalid(t *testing.T) {
	if _, err := resolveMaxUnavailable("not-a-number", 10); err == nil {
		t.Fatal("expected an error for a malformed maxUnavailable")
	}
}
