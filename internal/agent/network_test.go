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
	"time"

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

// TestProjectNetworkStatusThrottlesRecheckOnceResolved replaces the old
// "never calls QGA again once resolved" guarantee: that guarantee is gone
// on purpose (see guestAgentRecheckInterval) so a Machine's address change
// is eventually noticed. What must still hold is that two ticks close
// together (well within guestAgentRecheckInterval) on the *same* live
// Agent don't call QGA twice -- confirmed by feeding the resolved status
// from the first tick back in as the second tick's m.Status, the same way
// a real reconcile loop would.
func TestProjectNetworkStatusThrottlesRecheckOnceResolved(t *testing.T) {
	calls := 0
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-1/qga/network-interfaces" {
			calls++
			_, _ = w.Write([]byte(`[{"name":"enp0s7","ip-addresses":[{"ip-address":"10.0.2.15","ip-address-type":"ipv4"}]}]`))
			return
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
	}
	rec := &fluxvm.Record{UUID: "vm-1"}

	var status model.MachineStatus
	if err := a.projectNetworkStatus(context.Background(), m, rec, &status); err != nil {
		t.Fatalf("projectNetworkStatus (first tick): %v", err)
	}
	if status.GuestIP != "10.0.2.15" || calls != 1 {
		t.Fatalf("first tick: GuestIP=%q calls=%d, want 10.0.2.15 via exactly one qga call", status.GuestIP, calls)
	}

	m.Status = status
	var status2 model.MachineStatus
	if err := a.projectNetworkStatus(context.Background(), m, rec, &status2); err != nil {
		t.Fatalf("projectNetworkStatus (second tick): %v", err)
	}
	if status2.GuestIP != "10.0.2.15" || calls != 1 {
		t.Fatalf("second tick: GuestIP=%q calls=%d, want the preserved IP and no additional qga call", status2.GuestIP, calls)
	}
}

func TestProjectNetworkStatusRechecksAfterIntervalElapses(t *testing.T) {
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-1/qga/network-interfaces" {
			_, _ = w.Write([]byte(`[{"name":"enp0s7","ip-addresses":[{"ip-address":"10.0.2.99","ip-address-type":"ipv4"}]}]`))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer fs.Close()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{
		Flux: fc,
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		guestIPCheckedAt: map[string]time.Time{
			"prod/db": time.Now().Add(-2 * guestAgentRecheckInterval),
		},
	}

	m := model.Machine{
		Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"},
		Spec:     model.MachineSpec{GuestAgent: model.GuestAgentSpec{Enabled: true}},
		Status:   model.MachineStatus{GuestIP: "10.0.2.15"}, // stale -- address changed since the last check
	}
	rec := &fluxvm.Record{UUID: "vm-1"}
	var status model.MachineStatus
	if err := a.projectNetworkStatus(context.Background(), m, rec, &status); err != nil {
		t.Fatalf("projectNetworkStatus: %v", err)
	}
	if status.GuestIP != "10.0.2.99" {
		t.Fatalf("got GuestIP %q, want the freshly re-resolved 10.0.2.99 once the recheck interval elapsed", status.GuestIP)
	}
}

func TestProjectNetworkStatusPopulatesGuestIPsFromMultipleInterfaces(t *testing.T) {
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-1/qga/network-interfaces" {
			_, _ = w.Write([]byte(`[
				{"name":"lo","ip-addresses":[{"ip-address":"127.0.0.1","ip-address-type":"ipv4"}]},
				{"name":"enp0s7","ip-addresses":[
					{"ip-address":"fe80::1","ip-address-type":"ipv6"},
					{"ip-address":"10.0.2.15","ip-address-type":"ipv4"}
				]}
			]`))
			return
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
	}
	rec := &fluxvm.Record{UUID: "vm-1"}
	var status model.MachineStatus
	if err := a.projectNetworkStatus(context.Background(), m, rec, &status); err != nil {
		t.Fatalf("projectNetworkStatus: %v", err)
	}
	if status.GuestIP != "10.0.2.15" {
		t.Fatalf("got GuestIP %q, want the IPv4 address to remain the primary pick", status.GuestIP)
	}
	want := []string{"10.0.2.15", "fe80::1"}
	if len(status.GuestIPs) != len(want) || status.GuestIPs[0] != want[0] || status.GuestIPs[1] != want[1] {
		t.Fatalf("got GuestIPs %v, want %v", status.GuestIPs, want)
	}
	if status.Network == nil || len(status.Network.GuestIPs) != len(want) {
		t.Fatalf("status.Network.GuestIPs = %v, want it mirrored there too", status.Network)
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
