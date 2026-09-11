// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package fluxvm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zyvorai/kairon/internal/model"
)

func TestCreateMapping(t *testing.T) {
	var got CreateRequest
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/vms" {
			http.Error(w, "bad route", 404)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(Record{UUID: "u1", Name: got.Name, Status: "Running"})
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	m := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}, Spec: model.MachineSpec{Image: model.ImageSpec{Path: "/images/db.qcow2"}, Resources: model.ResourceSpec{CPU: "1500m", Memory: "2Gi"}, Runtime: model.RuntimeSpec{Backend: "qemu"}, Network: model.NetworkSpec{Mode: "tap", NetNS: true, TapName: "tap-db", StaticNetwork: true, PodUID: "pod-1"}}}
	r, err := c.Create(context.Background(), m, "qemu")
	if err != nil {
		t.Fatal(err)
	}
	if r.ID() != "u1" || got.Name != "kairon-prod-db" || got.Tenant != "prod" || got.VCPUs != 2 || got.MemoryMiB != 2048 {
		t.Fatalf("record=%+v request=%+v", r, got)
	}
	if got.Network["mode"] != "tap" || got.Network["netns"] != true || got.Network["tap_name"] != "tap-db" {
		t.Fatalf("network=%v", got.Network)
	}
	if got.PodUID != "pod-1" || got.CloudInit == nil || !got.CloudInit.StaticNetwork {
		t.Fatalf("pod/cloud_init=%+v", got)
	}
}

func TestCreateUserForwards(t *testing.T) {
	var got CreateRequest
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(Record{UUID: "u2", Status: "Running"})
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	m := model.Machine{Metadata: model.ObjectMeta{Name: "nat", Namespace: "default"}, Spec: model.MachineSpec{
		Image: model.ImageSpec{Path: "/images/a.qcow2"}, Resources: model.ResourceSpec{CPU: "1", Memory: "1Gi"},
		Network: model.NetworkSpec{Mode: "user", Forwards: []model.PortForward{{HostPort: 8080, GuestPort: 80}}},
	}}
	if _, err := c.Create(context.Background(), m, "qemu"); err != nil {
		t.Fatal(err)
	}
	fw, ok := got.Network["forwards"].([]any)
	if !ok || len(fw) != 1 {
		t.Fatalf("forwards=%v", got.Network["forwards"])
	}
}

func TestSetVMNetworkPolicy(t *testing.T) {
	var path string
	var body WireVmNetworkPolicy
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	mbps := uint32(100)
	if err := c.SetVMNetworkPolicy(context.Background(), "vm-1", model.VmNetworkPolicy{DefaultAllow: false, AllowPorts: []string{"tcp/443"}, MaxEgressMbps: &mbps}); err != nil {
		t.Fatal(err)
	}
	if path != "/v1/vms/vm-1/network/policy" || body.DefaultAllow || body.MaxEgressMbps == nil || *body.MaxEgressMbps != 100 {
		t.Fatalf("path=%s body=%+v", path, body)
	}
}

func TestLookupArray(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]Record{{IDValue: "id1", Name: "vm"}})
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	r, err := c.LookupByName(context.Background(), "vm")
	if err != nil || r == nil || r.ID() != "id1" {
		t.Fatalf("r=%+v err=%v", r, err)
	}
}

func TestDeleteNotFoundIsIdempotent(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "bad", http.StatusBadRequest)
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	if err := c.Delete(context.Background(), "missing"); err != nil {
		t.Fatalf("delete should be idempotent: %v", err)
	}
}
