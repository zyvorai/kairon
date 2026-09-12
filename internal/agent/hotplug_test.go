// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zyvorai/kairon/internal/fluxvm"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

func hotplugMachine(cpu, memory string, appliedVCPUs uint32, appliedMemoryMiB uint64) model.Machine {
	return model.Machine{
		Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"},
		Spec:     model.MachineSpec{Resources: model.ResourceSpec{CPU: cpu, Memory: memory}},
		Status:   model.MachineStatus{AppliedVCPUs: appliedVCPUs, AppliedMemoryMiB: appliedMemoryMiB},
	}
}

// Three cases where reconcileHotplug must never call FluxVM at all, each
// expected to just report the target spec as already-applied: a fresh
// creation (nothing to hotplug yet, this *is* the baseline), no recorded
// baseline (an adopted Machine or a pre-hotplug-tracking kairon-node
// build), and a shrink request (FluxVM has no CPU/DIMM unplug).
func TestReconcileHotplugCasesThatNeverCallFluxVM(t *testing.T) {
	cases := []struct {
		name           string
		machine        model.Machine
		freshlyCreated bool
	}{
		{"freshly created seeds the baseline", hotplugMachine("2", "2Gi", 0, 0), true},
		{"no recorded baseline assumes already realized", hotplugMachine("2", "2Gi", 0, 0), false},
		{"a shrink request is ignored, applied stays put", hotplugMachine("1", "1Gi", 4, 4096), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatalf("FluxVM should not be called, got %s %s", r.Method, r.URL.Path)
			}))
			defer fs.Close()
			fc := fluxvm.New(fs.URL, "")
			fc.HTTP = fs.Client()
			a := &Agent{Flux: fc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

			wantVCPUs, err := model.ParseVCPUs(tc.machine.Spec.Resources.CPU)
			if err != nil {
				t.Fatal(err)
			}
			wantMemMiB, err := model.ParseMemoryMiB(tc.machine.Spec.Resources.Memory)
			if err != nil {
				t.Fatal(err)
			}
			if tc.machine.Status.AppliedVCPUs != 0 || tc.machine.Status.AppliedMemoryMiB != 0 {
				// The shrink case: applied stays at its prior value, not the (smaller) target.
				wantVCPUs, wantMemMiB = tc.machine.Status.AppliedVCPUs, tc.machine.Status.AppliedMemoryMiB
			}

			vcpus, memMiB, err := a.reconcileHotplug(context.Background(), tc.machine, &fluxvm.Record{UUID: "vm-1"}, tc.freshlyCreated)
			if err != nil {
				t.Fatalf("reconcileHotplug: %v", err)
			}
			if vcpus != wantVCPUs || memMiB != wantMemMiB {
				t.Fatalf("got vcpus=%d memMiB=%d, want %d/%d", vcpus, memMiB, wantVCPUs, wantMemMiB)
			}
		})
	}
}

func TestReconcileHotplugGrowsCPUAndMemory(t *testing.T) {
	var cpuBody, memBody map[string]any
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/vms/vm-1/hotplug/cpu":
			_ = json.NewDecoder(r.Body).Decode(&cpuBody)
			_ = json.NewEncoder(w).Encode(map[string]any{"vcpus": 4})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/vms/vm-1/hotplug/memory":
			_ = json.NewDecoder(r.Body).Decode(&memBody)
			_ = json.NewEncoder(w).Encode(map[string]any{"memory_mib": 4096})
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer fs.Close()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{Flux: fc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	m := hotplugMachine("4", "4Gi", 2, 2048)
	vcpus, memMiB, err := a.reconcileHotplug(context.Background(), m, &fluxvm.Record{UUID: "vm-1"}, false)
	if err != nil {
		t.Fatalf("reconcileHotplug: %v", err)
	}
	if vcpus != 4 || memMiB != 4096 {
		t.Fatalf("got vcpus=%d memMiB=%d, want 4/4096", vcpus, memMiB)
	}
	if cpuBody["add_vcpus"] != float64(2) {
		t.Fatalf("expected add_vcpus=2 (delta), got %v", cpuBody["add_vcpus"])
	}
	if memBody["add_memory_mib"] != float64(2048) {
		t.Fatalf("expected add_memory_mib=2048 (delta), got %v", memBody["add_memory_mib"])
	}
}

func TestReconcileHotplugDoesNotAdvanceAppliedOnFailure(t *testing.T) {
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"not enough hotplug headroom"}`, http.StatusBadRequest)
	}))
	defer fs.Close()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{Flux: fc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	m := hotplugMachine("8", "2Gi", 2, 2048)
	vcpus, memMiB, err := a.reconcileHotplug(context.Background(), m, &fluxvm.Record{UUID: "vm-1"}, false)
	if err == nil || !strings.Contains(err.Error(), "hotplug cpu") {
		t.Fatalf("expected a hotplug cpu error, got %v", err)
	}
	if vcpus != 2 || memMiB != 2048 {
		t.Fatalf("expected applied totals unchanged on failure so the same delta retries next tick, got vcpus=%d memMiB=%d", vcpus, memMiB)
	}
}

func TestReconcileMachineEndToEndTracksAppliedResourcesAndHotplugs(t *testing.T) {
	machine := model.Machine{
		Metadata: model.ObjectMeta{Name: "db", Namespace: "prod", Finalizers: []string{model.Finalizer}},
		Spec: model.MachineSpec{
			NodeName:   "worker-1",
			Image:      model.ImageSpec{Path: "/images/db.qcow2"},
			Resources:  model.ResourceSpec{CPU: "4", Memory: "4Gi"},
			Runtime:    model.RuntimeSpec{Backend: "qemu"},
			PowerState: "Running",
		},
		Status: model.MachineStatus{RuntimeID: "vm-1", AppliedVCPUs: 2, AppliedMemoryMiB: 2048},
	}
	var patchedStatus model.MachineStatus
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && (strings.Contains(r.URL.Path, "networksecuritygroups") || strings.Contains(r.URL.Path, "machinenetworkpolicies") || strings.Contains(r.URL.Path, "machinemigrations")):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/db/status":
			var p struct {
				Status model.MachineStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			patchedStatus = p.Status
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer ks.Close()
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-1":
			_ = json.NewEncoder(w).Encode(fluxvm.Record{UUID: "vm-1", Status: "Running", GuestIP: "10.0.0.5"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/vms/vm-1/hotplug/cpu":
			_ = json.NewEncoder(w).Encode(map[string]any{"vcpus": 4})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/vms/vm-1/hotplug/memory":
			_ = json.NewEncoder(w).Encode(map[string]any{"memory_mib": 4096})
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer fs.Close()
	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{NodeName: "worker-1", Kube: kc, Flux: fc, DefaultBackend: "qemu", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := a.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if patchedStatus.AppliedVCPUs != 4 || patchedStatus.AppliedMemoryMiB != 4096 {
		t.Fatalf("expected status to record the new hotplugged totals, got %+v", patchedStatus)
	}
}
