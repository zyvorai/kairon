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
	var effectiveSynced bool
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
			effectiveSynced = p.Status.EffectiveSynced
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
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-9/network/policy":
			_ = json.NewEncoder(w).Encode(fluxvm.ToWirePolicy(policy.Spec.Policy))
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
	if !effectiveSynced {
		t.Fatal("expected EffectiveSynced=true once the read-back policy matches what was posted")
	}
}

// TestReconcileMachineNetworkPolicyDoesNotConfirmOnMismatch proves
// EffectiveSynced now reflects a real read-back comparison, not just "the
// POST succeeded" -- a FluxVM that accepts the write but reports a
// different policy on GET must not be reported as synced.
func TestReconcileMachineNetworkPolicyDoesNotConfirmOnMismatch(t *testing.T) {
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
	var effectiveSynced bool
	var effectiveSyncedSet bool
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
			effectiveSynced = p.Status.EffectiveSynced
			effectiveSyncedSet = true
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
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-9/network/policy":
			// Deliberately reports something different from what was
			// posted (DefaultAllow flipped) -- simulates FluxVM silently
			// normalizing or only partially applying the request.
			_ = json.NewEncoder(w).Encode(fluxvm.ToWirePolicy(model.VmNetworkPolicy{DefaultAllow: true}))
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
	if !effectiveSyncedSet {
		t.Fatal("expected a status patch to have been observed")
	}
	if effectiveSynced {
		t.Fatal("expected EffectiveSynced=false when the read-back policy doesn't match what was posted")
	}
}

// TestDeregisterServiceFabricRemovesOnlyTheMatchingBackend proves
// deregisterServiceFabric's own backend-list surgery: it removes exactly
// the (address, port) pair belonging to this Machine and leaves every
// other backend on the service (including a different port on the same
// address) untouched.
func TestDeregisterServiceFabricRemovesOnlyTheMatchingBackend(t *testing.T) {
	var posted fluxvm.ServiceSpec
	var postSeen bool
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/network/services/web-vip":
			_ = json.NewEncoder(w).Encode(fluxvm.ServiceSpec{
				Name: "web-vip",
				VIP:  "10.0.0.1",
				Port: 8080,
				Backends: []fluxvm.ServiceBackend{
					{Address: "10.44.0.9", Port: 8080, Weight: 1},
					{Address: "10.44.0.9", Port: 9090, Weight: 1}, // different port, same address -- must survive
					{Address: "10.44.0.5", Port: 8080, Weight: 1}, // unrelated backend -- must survive
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/network/services":
			postSeen = true
			_ = json.NewDecoder(r.Body).Decode(&posted)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer fs.Close()

	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{Flux: fc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	m := model.Machine{
		Metadata: model.ObjectMeta{Name: "web", Namespace: "default"},
		Spec:     model.MachineSpec{ServiceFabric: model.ServiceFabricSpec{Services: []model.ServiceFabricMembership{{Name: "web-vip", Port: 8080}}}},
	}
	if err := a.deregisterServiceFabric(context.Background(), m, "10.44.0.9"); err != nil {
		t.Fatalf("deregisterServiceFabric: %v", err)
	}
	if !postSeen {
		t.Fatal("expected the pruned service to be posted back")
	}
	if len(posted.Backends) != 2 {
		t.Fatalf("got %d backends, want the two unrelated ones kept: %+v", len(posted.Backends), posted.Backends)
	}
	for _, b := range posted.Backends {
		if b.Address == "10.44.0.9" && b.Port == 8080 {
			t.Fatalf("the deregistered backend is still present: %+v", posted.Backends)
		}
	}
}

// TestDeregisterServiceFabricToleratesMissingService proves a service
// that's already gone (404) is nothing left to deregister from -- not an
// error, and no POST is issued to recreate it.
func TestDeregisterServiceFabricToleratesMissingService(t *testing.T) {
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/network/services/web-vip" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer fs.Close()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{Flux: fc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	m := model.Machine{
		Metadata: model.ObjectMeta{Name: "web", Namespace: "default"},
		Spec:     model.MachineSpec{ServiceFabric: model.ServiceFabricSpec{Services: []model.ServiceFabricMembership{{Name: "web-vip", Port: 8080}}}},
	}
	if err := a.deregisterServiceFabric(context.Background(), m, "10.44.0.9"); err != nil {
		t.Fatalf("deregisterServiceFabric: %v (want a missing service tolerated as nothing-to-do)", err)
	}
}

// TestDeregisterServiceFabricNoopWithoutGuestIP proves an empty guestIP
// (never resolved, or already cleared) short-circuits before any FluxVM
// call -- there is no address to look up or remove.
func TestDeregisterServiceFabricNoopWithoutGuestIP(t *testing.T) {
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected FluxVM call with no guestIP: %s %s", r.Method, r.URL.Path)
	}))
	defer fs.Close()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{Flux: fc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	m := model.Machine{
		Metadata: model.ObjectMeta{Name: "web", Namespace: "default"},
		Spec:     model.MachineSpec{ServiceFabric: model.ServiceFabricSpec{Services: []model.ServiceFabricMembership{{Name: "web-vip", Port: 8080}}}},
	}
	if err := a.deregisterServiceFabric(context.Background(), m, ""); err != nil {
		t.Fatalf("deregisterServiceFabric: %v", err)
	}
}

// TestCleanupDeregistersServiceFabricBeforeRemovingFinalizer proves
// cleanup (Machine deletion) now removes this Machine's Service Fabric
// backend membership as part of tearing the runtime down, so a deleted
// Machine's now-dead guest IP stops being routed live traffic by FluxVM.
func TestCleanupDeregistersServiceFabricBeforeRemovingFinalizer(t *testing.T) {
	m := model.Machine{
		Metadata: model.ObjectMeta{Name: "web", Namespace: "default", Finalizers: []string{model.Finalizer}},
		Spec:     model.MachineSpec{ServiceFabric: model.ServiceFabricSpec{Services: []model.ServiceFabricMembership{{Name: "web-vip", Port: 8080}}}},
		Status:   model.MachineStatus{RuntimeID: "vm-9", GuestIP: "10.44.0.9"},
	}
	var postSeen bool
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/vms/vm-9":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/network/services/web-vip":
			_ = json.NewEncoder(w).Encode(fluxvm.ServiceSpec{Name: "web-vip", Backends: []fluxvm.ServiceBackend{{Address: "10.44.0.9", Port: 8080}}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/network/services":
			postSeen = true
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer fs.Close()

	var finalizerCleared bool
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines/web" {
			var body struct {
				Metadata model.ObjectMeta `json:"metadata"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			finalizerCleared = len(body.Metadata.Finalizers) == 0
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer ks.Close()

	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{Kube: kc, Flux: fc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	if err := a.cleanup(context.Background(), m); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if !postSeen {
		t.Fatal("expected the Service Fabric backend to be deregistered during cleanup")
	}
	if !finalizerCleared {
		t.Fatal("expected the finalizer to be removed after a successful cleanup")
	}
}

// TestCleanupFailsClosedWhenServiceFabricDeregistrationFails proves
// cleanup fails closed exactly like the FluxVM runtime delete and CSI
// teardown steps it already fails closed on: a genuine deregistration
// error must leave the finalizer in place (no finalizer-removal Patch
// observed) rather than letting the Machine vanish from Kubernetes while
// its guest IP stays a live, now-orphaned Service Fabric backend.
func TestCleanupFailsClosedWhenServiceFabricDeregistrationFails(t *testing.T) {
	m := model.Machine{
		Metadata: model.ObjectMeta{Name: "web", Namespace: "default", Finalizers: []string{model.Finalizer}},
		Spec:     model.MachineSpec{ServiceFabric: model.ServiceFabricSpec{Services: []model.ServiceFabricMembership{{Name: "web-vip", Port: 8080}}}},
		Status:   model.MachineStatus{RuntimeID: "vm-9", GuestIP: "10.44.0.9"},
	}
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/vms/vm-9":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/network/services/web-vip":
			http.Error(w, "fluxvm node unreachable", http.StatusInternalServerError)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer fs.Close()

	var finalizerPatchSeen bool
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines/web" {
			finalizerPatchSeen = true
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer ks.Close()

	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{Kube: kc, Flux: fc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	err := a.cleanup(context.Background(), m)
	if err == nil {
		t.Fatal("expected an error from a failed Service Fabric deregistration")
	}
	if finalizerPatchSeen {
		t.Fatal("finalizer must not be removed when Service Fabric deregistration failed")
	}
}

// TestEnsureStoppedDeregistersServiceFabricBackend proves
// spec.powerState: Stopped deregisters this Machine's Service Fabric
// membership too, not just Machine deletion -- a Stopped guest is just
// as unreachable as a deleted one, and status.guestIP below is about to
// be cleared, leaving no later reconcile to notice the stale backend.
func TestEnsureStoppedDeregistersServiceFabricBackend(t *testing.T) {
	m := model.Machine{
		Metadata: model.ObjectMeta{Name: "web", Namespace: "default"},
		Spec:     model.MachineSpec{ServiceFabric: model.ServiceFabricSpec{Services: []model.ServiceFabricMembership{{Name: "web-vip", Port: 8080}}}},
		Status:   model.MachineStatus{RuntimeID: "vm-9", GuestIP: "10.44.0.9"},
	}
	var postSeen bool
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-9":
			_ = json.NewEncoder(w).Encode(fluxvm.Record{UUID: "vm-9", Status: "Running"})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/vms/vm-9":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/network/services/web-vip":
			_ = json.NewEncoder(w).Encode(fluxvm.ServiceSpec{Name: "web-vip", Backends: []fluxvm.ServiceBackend{{Address: "10.44.0.9", Port: 8080}}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/network/services":
			postSeen = true
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer fs.Close()

	var statusPhase string
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines/web/status" {
			var p struct {
				Status model.MachineStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			statusPhase = p.Status.Phase
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer ks.Close()

	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{Kube: kc, Flux: fc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	if err := a.ensureStopped(context.Background(), m); err != nil {
		t.Fatalf("ensureStopped: %v", err)
	}
	if !postSeen {
		t.Fatal("expected the Service Fabric backend to be deregistered on stop")
	}
	if statusPhase != "Stopped" {
		t.Fatalf("got status.Phase %q, want Stopped", statusPhase)
	}
}

// TestEnsureStoppedFailsClosedWhenServiceFabricDeregistrationFails proves
// ensureStopped, like cleanup, never reports Stopped until Service
// Fabric deregistration actually succeeds -- a Machine that fails this
// step keeps reporting its prior (Running) status honestly and is
// retried next tick, rather than lying about being Stopped while a live
// VIP backend entry for it is still left behind.
func TestEnsureStoppedFailsClosedWhenServiceFabricDeregistrationFails(t *testing.T) {
	m := model.Machine{
		Metadata: model.ObjectMeta{Name: "web", Namespace: "default"},
		Spec:     model.MachineSpec{ServiceFabric: model.ServiceFabricSpec{Services: []model.ServiceFabricMembership{{Name: "web-vip", Port: 8080}}}},
		Status:   model.MachineStatus{RuntimeID: "vm-9", GuestIP: "10.44.0.9", Phase: "Running"},
	}
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-9":
			_ = json.NewEncoder(w).Encode(fluxvm.Record{UUID: "vm-9", Status: "Running"})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/vms/vm-9":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/network/services/web-vip":
			http.Error(w, "fluxvm node unreachable", http.StatusInternalServerError)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer fs.Close()

	var statusPatchSeen bool
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines/web/status" {
			statusPatchSeen = true
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer ks.Close()

	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{Kube: kc, Flux: fc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	err := a.ensureStopped(context.Background(), m)
	if err == nil {
		t.Fatal("expected an error from a failed Service Fabric deregistration")
	}
	if statusPatchSeen {
		t.Fatal("status must not be patched to Stopped when Service Fabric deregistration failed")
	}
}

// TestEnsureHaltedDeregistersServiceFabricBackend proves
// spec.powerState: Halted deregisters Service Fabric membership too --
// Halted keeps the FluxVM runtime record around (unlike Stopped), but
// the guest itself is just as powered-off and unreachable.
func TestEnsureHaltedDeregistersServiceFabricBackend(t *testing.T) {
	m := model.Machine{
		Metadata: model.ObjectMeta{Name: "web", Namespace: "default"},
		Spec:     model.MachineSpec{ServiceFabric: model.ServiceFabricSpec{Services: []model.ServiceFabricMembership{{Name: "web-vip", Port: 8080}}}},
		Status:   model.MachineStatus{RuntimeID: "vm-9", GuestIP: "10.44.0.9"},
	}
	var postSeen bool
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-9":
			_ = json.NewEncoder(w).Encode(fluxvm.Record{UUID: "vm-9", Status: "Running"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/vms/vm-9/stop":
			_ = json.NewEncoder(w).Encode(fluxvm.Record{UUID: "vm-9", Status: "Stopped"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/network/services/web-vip":
			_ = json.NewEncoder(w).Encode(fluxvm.ServiceSpec{Name: "web-vip", Backends: []fluxvm.ServiceBackend{{Address: "10.44.0.9", Port: 8080}}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/network/services":
			postSeen = true
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer fs.Close()

	var statusPhase string
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines/web/status" {
			var p struct {
				Status model.MachineStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			statusPhase = p.Status.Phase
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer ks.Close()

	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{Kube: kc, Flux: fc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	if err := a.ensureHalted(context.Background(), m); err != nil {
		t.Fatalf("ensureHalted: %v", err)
	}
	if !postSeen {
		t.Fatal("expected the Service Fabric backend to be deregistered on halt")
	}
	if statusPhase != "Halted" {
		t.Fatalf("got status.Phase %q, want Halted", statusPhase)
	}
}

func deletingSecurityGroup() model.NetworkSecurityGroup {
	now := time.Now().UTC()
	return model.NetworkSecurityGroup{
		Metadata: model.ObjectMeta{
			Name:              "web-edge",
			Namespace:         "default",
			Finalizers:        []string{model.FinalizerNetworkGroup},
			DeletionTimestamp: &now,
		},
	}
}

// TestReconcileSecurityGroupDeletionKeepsFinalizerOnDeleteError proves
// reconcileSecurityGroup now fails closed: a genuine FluxVM-side delete
// error must leave the finalizer in place (no finalizer-removal Patch
// observed) rather than silently dropping the Kubernetes object while
// its FluxVM security group state leaks behind untracked.
func TestReconcileSecurityGroupDeletionKeepsFinalizerOnDeleteError(t *testing.T) {
	var finalizerPatchSeen bool
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/networksecuritygroups/web-edge" {
			finalizerPatchSeen = true
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer ks.Close()
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fluxvm node unreachable", http.StatusInternalServerError)
	}))
	defer fs.Close()

	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{NodeName: "worker-1", Kube: kc, Flux: fc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	err := a.reconcileSecurityGroup(context.Background(), deletingSecurityGroup())
	if err == nil {
		t.Fatal("expected an error from a failed FluxVM delete")
	}
	if finalizerPatchSeen {
		t.Fatal("finalizer must not be removed when the FluxVM-side delete failed")
	}
}

// TestReconcileSecurityGroupDeletionRemovesFinalizerOnSuccess is the
// positive case: a successful FluxVM delete does remove the finalizer,
// so a healthy deletion still completes exactly as before this fix.
func TestReconcileSecurityGroupDeletionRemovesFinalizerOnSuccess(t *testing.T) {
	var patchedFinalizers []string
	var sawFinalizerPatch bool
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/networksecuritygroups/web-edge" {
			sawFinalizerPatch = true
			var body struct {
				Metadata struct {
					Finalizers []string `json:"finalizers"`
				} `json:"metadata"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			patchedFinalizers = body.Metadata.Finalizers
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer ks.Close()
	var deleteHit bool
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/network/groups/web-edge" {
			deleteHit = true
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer fs.Close()

	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{NodeName: "worker-1", Kube: kc, Flux: fc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	if err := a.reconcileSecurityGroup(context.Background(), deletingSecurityGroup()); err != nil {
		t.Fatalf("reconcileSecurityGroup: %v", err)
	}
	if !deleteHit {
		t.Fatal("expected FluxVM's DeleteNetworkGroup to have been called")
	}
	if !sawFinalizerPatch || len(patchedFinalizers) != 0 {
		t.Fatalf("expected the finalizer removed, got sawFinalizerPatch=%v patchedFinalizers=%v", sawFinalizerPatch, patchedFinalizers)
	}
}

// TestReconcileSecurityGroupDeletionToleratesAlreadyDeleted proves a
// FluxVM-side 404 (the group was already deleted, e.g. by a prior tick
// whose own finalizer-removal Patch then failed) still lets the
// finalizer clear -- without this, DeleteNetworkGroup's own 404
// tolerance combined with a naive non-idempotent caller could still
// deadlock a retry.
func TestReconcileSecurityGroupDeletionToleratesAlreadyDeleted(t *testing.T) {
	var sawFinalizerPatch bool
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/networksecuritygroups/web-edge" {
			sawFinalizerPatch = true
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer ks.Close()
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer fs.Close()

	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{NodeName: "worker-1", Kube: kc, Flux: fc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	if err := a.reconcileSecurityGroup(context.Background(), deletingSecurityGroup()); err != nil {
		t.Fatalf("reconcileSecurityGroup: %v", err)
	}
	if !sawFinalizerPatch {
		t.Fatal("expected the finalizer to be removed once the FluxVM-side group is confirmed already gone")
	}
}

func deletingMachineNetworkPolicyAndMachine() (model.MachineNetworkPolicy, model.Machine) {
	now := time.Now().UTC()
	m := model.Machine{
		Metadata: model.ObjectMeta{Name: "web", Namespace: "default", Labels: map[string]string{"app": "web"}},
		Spec:     model.MachineSpec{NodeName: "worker-1"},
		Status:   model.MachineStatus{RuntimeID: "vm-9"},
	}
	p := model.MachineNetworkPolicy{
		Metadata: model.ObjectMeta{
			Name:              "web-edge",
			Namespace:         "default",
			Finalizers:        []string{model.FinalizerNetworkPolicy},
			DeletionTimestamp: &now,
		},
		Spec: model.MachineNetworkPolicySpec{Selector: map[string]string{"app": "web"}},
	}
	return p, m
}

// TestReconcileMachineNetworkPolicyDeletionKeepsFinalizerOnResetError
// proves reconcileMachineNetworkPolicy now fails closed on the deletion
// path: a genuine FluxVM-side reset error must leave the finalizer in
// place (no finalizer-removal Patch observed) rather than letting the
// MachineNetworkPolicy vanish from Kubernetes while a selected Machine's
// VM keeps running under its now-stale restriction.
func TestReconcileMachineNetworkPolicyDeletionKeepsFinalizerOnResetError(t *testing.T) {
	var finalizerPatchSeen bool
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinenetworkpolicies/web-edge" {
			finalizerPatchSeen = true
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer ks.Close()
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fluxvm node unreachable", http.StatusInternalServerError)
	}))
	defer fs.Close()

	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{NodeName: "worker-1", Kube: kc, Flux: fc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	p, m := deletingMachineNetworkPolicyAndMachine()
	err := a.reconcileMachineNetworkPolicy(context.Background(), p, []model.Machine{m})
	if err == nil {
		t.Fatal("expected an error from a failed FluxVM policy reset")
	}
	if finalizerPatchSeen {
		t.Fatal("finalizer must not be removed when the FluxVM-side reset failed")
	}
}

// TestReconcileMachineNetworkPolicyDeletionRemovesFinalizerOnSuccess is
// the positive case: a successful FluxVM reset still removes the
// finalizer, so a healthy deletion completes exactly as before this fix.
func TestReconcileMachineNetworkPolicyDeletionRemovesFinalizerOnSuccess(t *testing.T) {
	var patchedFinalizers []string
	var sawFinalizerPatch bool
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinenetworkpolicies/web-edge" {
			sawFinalizerPatch = true
			var body struct {
				Metadata struct {
					Finalizers []string `json:"finalizers"`
				} `json:"metadata"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			patchedFinalizers = body.Metadata.Finalizers
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer ks.Close()
	var resetHit bool
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/vms/vm-9/network/policy" {
			resetHit = true
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer fs.Close()

	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{NodeName: "worker-1", Kube: kc, Flux: fc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	p, m := deletingMachineNetworkPolicyAndMachine()
	if err := a.reconcileMachineNetworkPolicy(context.Background(), p, []model.Machine{m}); err != nil {
		t.Fatalf("reconcileMachineNetworkPolicy: %v", err)
	}
	if !resetHit {
		t.Fatal("expected FluxVM's SetVMNetworkPolicy reset to have been called")
	}
	if !sawFinalizerPatch || len(patchedFinalizers) != 0 {
		t.Fatalf("expected the finalizer removed, got sawFinalizerPatch=%v patchedFinalizers=%v", sawFinalizerPatch, patchedFinalizers)
	}
}

// TestReconcileMachineNetworkPolicyDeletionToleratesAlreadyGoneVM proves
// a FluxVM-side 404 (the VM is already gone) still lets the finalizer
// clear -- without this, SetVMNetworkPolicy's own 404 tolerance combined
// with a naive non-idempotent caller could still deadlock a retry.
func TestReconcileMachineNetworkPolicyDeletionToleratesAlreadyGoneVM(t *testing.T) {
	var sawFinalizerPatch bool
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinenetworkpolicies/web-edge" {
			sawFinalizerPatch = true
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer ks.Close()
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer fs.Close()

	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{NodeName: "worker-1", Kube: kc, Flux: fc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	p, m := deletingMachineNetworkPolicyAndMachine()
	if err := a.reconcileMachineNetworkPolicy(context.Background(), p, []model.Machine{m}); err != nil {
		t.Fatalf("reconcileMachineNetworkPolicy: %v", err)
	}
	if !sawFinalizerPatch {
		t.Fatal("expected the finalizer to be removed once the FluxVM-side VM is confirmed already gone")
	}
}
