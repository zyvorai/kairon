// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zyvorai/kairon/internal/ebpfedge"
	"github.com/zyvorai/kairon/internal/fluxvm"
	"github.com/zyvorai/kairon/internal/migration"
	"github.com/zyvorai/kairon/internal/model"
)

func TestApplyEdgeProjectsStableIdentity(t *testing.T) {
	var posted bool
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/vms/vm-1/network/edge" {
			posted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer fs.Close()
	mbps := uint32(100)
	m := model.Machine{
		Metadata: model.ObjectMeta{Namespace: "demo", Name: "web"},
		Spec: model.MachineSpec{Network: model.NetworkSpec{
			DataplaneMode: "ebpf",
			AntiSpoof:     true,
			MAC:           "52:54:00:aa:bb:cc",
			QoS:           &model.NetworkQoS{EgressMbps: &mbps},
		}},
	}
	a := &Agent{Flux: fluxvm.New(fs.URL, ""), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	st, err := a.applyEdge(context.Background(), m, "vm-1", "10.0.0.8")
	if err != nil {
		t.Fatal(err)
	}
	if !posted || st == nil || st.Identity != ebpfedge.StableIdentity("demo", "web") || !st.AntiSpoof {
		t.Fatalf("status=%+v posted=%v", st, posted)
	}
}

func TestApplyEdgeFailClosedWhenRequired(t *testing.T) {
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no edge", http.StatusNotImplemented)
	}))
	defer fs.Close()
	m := model.Machine{
		Metadata: model.ObjectMeta{Namespace: "demo", Name: "web"},
		Spec:     model.MachineSpec{Network: model.NetworkSpec{DataplaneMode: "ebpf", DataplaneRequired: true}},
	}
	a := &Agent{Flux: fluxvm.New(fs.URL, ""), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if _, err := a.applyEdge(context.Background(), m, "vm-1", ""); err == nil {
		t.Fatal("expected fail-closed edge apply")
	}
}

func TestExportConntrackStampsIdentity(t *testing.T) {
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entries":[{"proto":"tcp","srcIP":"10.0.0.8","dstIP":"10.1.0.4","srcPort":1,"dstPort":443,"state":"established"}]}`))
	}))
	defer fs.Close()
	m := model.Machine{
		Metadata: model.ObjectMeta{Namespace: "demo", Name: "web"},
		Spec:     model.MachineSpec{Network: model.NetworkSpec{AntiSpoof: true}},
	}
	a := &Agent{Flux: fluxvm.New(fs.URL, ""), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	raw := a.exportConntrackSnapshot(context.Background(), m, "vm-1")
	snap, err := ebpfedge.UnmarshalSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Identity != ebpfedge.StableIdentity("demo", "web") || len(snap.Entries) != 1 || snap.ExportedAt.IsZero() {
		t.Fatalf("snap=%+v", snap)
	}
	if _, err := ebpfedge.RestoreConntrack(snap.Identity, snap, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func TestEdgeNotRequestedSkipsFlux(t *testing.T) {
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("flux should not be called")
	}))
	defer fs.Close()
	m := model.Machine{Metadata: model.ObjectMeta{Namespace: "demo", Name: "web"}}
	a := &Agent{Flux: fluxvm.New(fs.URL, ""), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	st, err := a.applyEdge(context.Background(), m, "vm-1", "")
	if err != nil || st != nil {
		t.Fatalf("st=%+v err=%v", st, err)
	}
	if raw := a.exportConntrackSnapshot(context.Background(), m, "vm-1"); raw != nil {
		t.Fatalf("unexpected snapshot %s", raw)
	}
}

func TestMergeSelectingPolicyCopiesSNIAndName(t *testing.T) {
	spec := buildEdgeSpec(model.Machine{Metadata: model.ObjectMeta{Namespace: "demo", Name: "web"}}, "10.0.0.8")
	mbps := uint32(50)
	merged := mergeSelectingPolicy(spec, model.MachineNetworkPolicy{
		Metadata: model.ObjectMeta{Name: "egress-443"},
		Spec: model.MachineNetworkPolicySpec{Policy: model.VmNetworkPolicy{
			DefaultAllow:  false,
			AllowSNI:      []string{"*.vendor.com"},
			AllowFqdns:    []string{"api.vendor.com"},
			MaxEgressMbps: &mbps,
		}},
	})
	if merged.PolicyName != "egress-443" || merged.DefaultAllow || merged.AllowSNI[0] != "*.vendor.com" || merged.AllowDNS[0] != "api.vendor.com" || merged.QoS.EgressMbps != 50 {
		t.Fatalf("merged %+v", merged)
	}
}

func TestApplyEdgeProjectsConntrackRestore(t *testing.T) {
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer fs.Close()
	store := migration.NewRestoreStore()
	store.Put("demo", "web", ebpfedge.RestoreResult{Restored: 4, BlackholeWindowMs: 25, Identity: 1})
	m := model.Machine{
		Metadata: model.ObjectMeta{Namespace: "demo", Name: "web"},
		Spec:     model.MachineSpec{Network: model.NetworkSpec{DataplaneMode: "ebpf"}},
	}
	a := &Agent{Flux: fluxvm.New(fs.URL, ""), Restores: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	st, err := a.applyEdge(context.Background(), m, "vm-1", "10.0.0.8")
	if err != nil {
		t.Fatal(err)
	}
	if st.ConntrackRestored != 4 || st.BlackholeWindowMs != 25 || st.GuestIPSource != ebpfedge.IPSourceAgent {
		t.Fatalf("status %+v", st)
	}
}

func TestApplyEdgeLearnsIPWhenUnset(t *testing.T) {
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/vms/vm-1/network/learned-ip" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ip":"10.0.0.15","source":"dhcp"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer fs.Close()
	m := model.Machine{
		Metadata: model.ObjectMeta{Namespace: "demo", Name: "web"},
		Spec:     model.MachineSpec{Network: model.NetworkSpec{DataplaneMode: "ebpf", LearnIP: true}},
	}
	a := &Agent{Flux: fluxvm.New(fs.URL, ""), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	st, err := a.applyEdge(context.Background(), m, "vm-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if st.GuestIPSource != "dhcp" {
		t.Fatalf("source %q", st.GuestIPSource)
	}
}
