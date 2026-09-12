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
	"testing"

	"github.com/zyvorai/kairon/internal/fluxvm"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

func TestProjectNetworkStatusFallsBackToQGAWhenNoLeaseIP(t *testing.T) {
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-1/network/status":
			http.Error(w, "not found", http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-1/qga/network-interfaces":
			_, _ = w.Write([]byte(`[{"name":"enp0s7","ip-addresses":[{"ip-address":"10.0.2.15","ip-address-type":"ipv4"}]}]`))
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer fs.Close()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{Flux: fc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	m := model.Machine{
		Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"},
		Spec:     model.MachineSpec{GuestAgent: model.GuestAgentSpec{Enabled: true}},
	}
	rec := &fluxvm.Record{UUID: "vm-1"} // GuestIP empty -- no DHCP lease (user-mode networking)
	var status model.MachineStatus
	if err := a.projectNetworkStatus(context.Background(), m, rec, &status); err != nil {
		t.Fatalf("projectNetworkStatus: %v", err)
	}
	if status.GuestIP != "10.0.2.15" {
		t.Fatalf("got GuestIP %q, want 10.0.2.15 (resolved via qga)", status.GuestIP)
	}
}

func TestProjectNetworkStatusPreservesLastKnownGoodIPWithoutCallingQGA(t *testing.T) {
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-1/qga/network-interfaces" {
			t.Fatal("qga should not be called when a guest IP is already known")
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer fs.Close()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{Flux: fc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	m := model.Machine{
		Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"},
		Spec:     model.MachineSpec{GuestAgent: model.GuestAgentSpec{Enabled: true}},
		Status:   model.MachineStatus{GuestIP: "10.0.0.9"},
	}
	rec := &fluxvm.Record{UUID: "vm-1"}
	var status model.MachineStatus
	if err := a.projectNetworkStatus(context.Background(), m, rec, &status); err != nil {
		t.Fatalf("projectNetworkStatus: %v", err)
	}
	if status.GuestIP != "10.0.0.9" {
		t.Fatalf("got GuestIP %q, want the preserved 10.0.0.9", status.GuestIP)
	}
}

func TestProjectNetworkStatusSkipsQGAWhenNotEnabled(t *testing.T) {
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-1/qga/network-interfaces" {
			t.Fatal("qga should not be called when spec.guestAgent.enabled is false")
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer fs.Close()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{Flux: fc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	m := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}}
	rec := &fluxvm.Record{UUID: "vm-1"}
	var status model.MachineStatus
	if err := a.projectNetworkStatus(context.Background(), m, rec, &status); err != nil {
		t.Fatalf("projectNetworkStatus: %v", err)
	}
	if status.GuestIP != "" {
		t.Fatalf("got GuestIP %q, want empty", status.GuestIP)
	}
}

func TestReconcileMachineNetworkPolicy(t *testing.T) {
	machine := model.Machine{
		Metadata: model.ObjectMeta{Name: "web", Namespace: "default", Labels: map[string]string{"app": "web"}, Finalizers: []string{model.Finalizer}},
		Spec:     model.MachineSpec{NodeName: "worker-1", Image: model.ImageSpec{Path: "/images/web.qcow2"}, Resources: model.ResourceSpec{CPU: "1", Memory: "1Gi"}, PowerState: "Running"},
		Status:   model.MachineStatus{Phase: "Running", RuntimeID: "vm-9", NodeName: "worker-1"},
	}
	policy := model.MachineNetworkPolicy{
		Metadata: model.ObjectMeta{Name: "web-edge", Namespace: "default", Finalizers: []string{model.FinalizerNetworkPolicy}},
		Spec: model.MachineNetworkPolicySpec{
			Selector: map[string]string{"app": "web"},
			Policy:   model.VmNetworkPolicy{DefaultAllow: false, AllowPorts: []string{"tcp/443"}},
		},
	}
	var policyPosted bool
	var statusPhase string
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/networksecuritygroups":
			_ = json.NewEncoder(w).Encode(model.NetworkSecurityGroupList{})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinenetworkpolicies":
			_ = json.NewEncoder(w).Encode(model.MachineNetworkPolicyList{Items: []model.MachineNetworkPolicy{policy}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{})
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinenetworkpolicies/web-edge/status":
			var p struct {
				Status model.MachineNetworkPolicyStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			statusPhase = p.Status.Phase
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines/web/status":
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer ks.Close()

	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-9":
			_ = json.NewEncoder(w).Encode(fluxvm.Record{UUID: "vm-9", Status: "Running", GuestIP: "10.44.0.9"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-9/network/status":
			_ = json.NewEncoder(w).Encode(fluxvm.DataplaneStatus{Mode: "ebpf", Attached: true, Identity: 42, PolicySynced: true})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/vms/vm-9/network/policy":
			policyPosted = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
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
	if !policyPosted || statusPhase != "Applied" {
		t.Fatalf("policyPosted=%v statusPhase=%q", policyPosted, statusPhase)
	}
}
