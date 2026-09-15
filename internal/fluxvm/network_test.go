// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package fluxvm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zyvorai/kairon/internal/model"
)

func TestNetworkMigrationQuiesce(t *testing.T) {
	hit := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/vms/vm-1/network/migration/quiesce" {
			hit = true
			_ = json.NewEncoder(w).Encode(map[string]any{})
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	if err := c.NetworkMigrationQuiesce(context.Background(), "vm-1"); err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("expected the quiesce endpoint to be called")
	}
}

func TestNetworkMigrationExport(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-1/network/migration/export" {
			_, _ = w.Write([]byte(`{"snapshot":"data","v":1}`))
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	snap, err := c.NetworkMigrationExport(context.Background(), "vm-1")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(snap, &got); err != nil {
		t.Fatal(err)
	}
	if got["snapshot"] != "data" || got["v"] != float64(1) {
		t.Fatalf("snapshot=%v", got)
	}
}

func TestNetworkMigrationRestoreWithSnapshot(t *testing.T) {
	var posted map[string]any
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/vms/vm-1/network/migration/restore" {
			_ = json.NewDecoder(r.Body).Decode(&posted)
			_ = json.NewEncoder(w).Encode(map[string]any{})
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	if err := c.NetworkMigrationRestore(context.Background(), "vm-1", json.RawMessage(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	if posted["v"] != float64(1) {
		t.Fatalf("posted=%v", posted)
	}
}

func TestNetworkMigrationRestoreEmptySnapshotSendsEmptyObject(t *testing.T) {
	var body string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/vms/vm-1/network/migration/restore" {
			b, _ := io.ReadAll(r.Body)
			body = strings.TrimSpace(string(b))
			_ = json.NewEncoder(w).Encode(map[string]any{})
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	if err := c.NetworkMigrationRestore(context.Background(), "vm-1", nil); err != nil {
		t.Fatal(err)
	}
	if body != "{}" {
		t.Fatalf("body=%q, want {}", body)
	}
}

// A snapshot that isn't valid JSON can't be marshaled back onto the wire
// (json.RawMessage's MarshalJSON is compacted/validated by json.Marshal),
// so this must surface as an error rather than silently sending garbage.
func TestNetworkMigrationRestoreInvalidJSONReturnsError(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	if err := c.NetworkMigrationRestore(context.Background(), "vm-1", json.RawMessage("not-json")); err == nil {
		t.Fatal("expected an error for a non-JSON snapshot")
	}
}

func TestNetworkMigrationResume(t *testing.T) {
	hit := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/vms/vm-1/network/migration/resume" {
			hit = true
			_ = json.NewEncoder(w).Encode(map[string]any{})
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	if err := c.NetworkMigrationResume(context.Background(), "vm-1"); err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("expected the resume endpoint to be called")
	}
}

func TestGetVMNetworkPolicyDecodesWireShape(t *testing.T) {
	mbps := uint32(100)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/vms/vm-1/network/policy" {
			http.Error(w, "bad route", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(WireVmNetworkPolicy{DefaultAllow: false, AllowPorts: []string{"tcp/443"}, MaxEgressMbps: &mbps})
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()

	got, err := c.GetVMNetworkPolicy(context.Background(), "vm-1")
	if err != nil {
		t.Fatal(err)
	}
	want := model.VmNetworkPolicy{DefaultAllow: false, AllowPorts: []string{"tcp/443"}, MaxEgressMbps: &mbps}
	if got.DefaultAllow != want.DefaultAllow || len(got.AllowPorts) != 1 || got.AllowPorts[0] != "tcp/443" || got.MaxEgressMbps == nil || *got.MaxEgressMbps != 100 {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
}

func TestGetVMNetworkPolicyPropagatesErrors(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "VM not found", http.StatusNotFound)
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	if _, err := c.GetVMNetworkPolicy(context.Background(), "vm-1"); err == nil {
		t.Fatal("expected an error when the server rejects the request")
	}
}

func TestBestGuestIPPicksFirstIPv4OnFirstNonLoopbackInterface(t *testing.T) {
	ifaces := []QgaNetworkInterface{
		{Name: "lo", IPAddresses: []QgaIPAddress{{IPAddress: "127.0.0.1", IPAddressType: "ipv4"}}},
		{Name: "enp0s7", IPAddresses: []QgaIPAddress{
			{IPAddress: "fe80::1", IPAddressType: "ipv6"},
			{IPAddress: "10.0.2.15", IPAddressType: "ipv4"},
		}},
	}
	if got := BestGuestIP(ifaces); got != "10.0.2.15" {
		t.Fatalf("got %q, want 10.0.2.15", got)
	}
}

func TestBestGuestIPFallsBackToIPv6WhenNoIPv4Anywhere(t *testing.T) {
	ifaces := []QgaNetworkInterface{
		{Name: "lo", IPAddresses: []QgaIPAddress{{IPAddress: "127.0.0.1", IPAddressType: "ipv4"}}},
		{Name: "enp0s7", IPAddresses: []QgaIPAddress{{IPAddress: "fe80::1", IPAddressType: "ipv6"}}},
	}
	if got := BestGuestIP(ifaces); got != "fe80::1" {
		t.Fatalf("got %q, want the IPv6 address as a fallback (no IPv4 anywhere shouldn't mean no address at all)", got)
	}
}

func TestBestGuestIPHandlesNoInterfaces(t *testing.T) {
	if got := BestGuestIP(nil); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestAllGuestIPsOrdersIPv4BeforeIPv6AcrossInterfaces(t *testing.T) {
	ifaces := []QgaNetworkInterface{
		{Name: "lo", IPAddresses: []QgaIPAddress{{IPAddress: "127.0.0.1", IPAddressType: "ipv4"}}},
		{Name: "enp0s7", IPAddresses: []QgaIPAddress{
			{IPAddress: "fe80::1", IPAddressType: "ipv6"},
			{IPAddress: "10.0.2.15", IPAddressType: "ipv4"},
		}},
		{Name: "enp0s8", IPAddresses: []QgaIPAddress{
			{IPAddress: "192.168.1.5", IPAddressType: "ipv4"},
			{IPAddress: "2001:db8::5", IPAddressType: "ipv6"},
		}},
	}
	got := AllGuestIPs(ifaces)
	want := []string{"10.0.2.15", "192.168.1.5", "fe80::1", "2001:db8::5"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if got[0] != BestGuestIP(ifaces) {
		t.Fatalf("AllGuestIPs()[0] = %q should agree with BestGuestIP() = %q", got[0], BestGuestIP(ifaces))
	}
}

func TestAllGuestIPsExcludesLoopbackAndHandlesEmpty(t *testing.T) {
	ifaces := []QgaNetworkInterface{
		{Name: "lo", IPAddresses: []QgaIPAddress{{IPAddress: "127.0.0.1", IPAddressType: "ipv4"}, {IPAddress: "::1", IPAddressType: "ipv6"}}},
	}
	if got := AllGuestIPs(ifaces); len(got) != 0 {
		t.Fatalf("got %v, want empty (only a loopback interface present)", got)
	}
	if got := AllGuestIPs(nil); len(got) != 0 {
		t.Fatalf("got %v, want empty for nil input", got)
	}
}

func TestQGANetworkInterfacesDecodesRealResponseShape(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/vms/vm-1/qga/network-interfaces" {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`[{"name":"enp0s7","hardware-address":"52:54:00:12:34:56","ip-addresses":[{"ip-address":"10.0.2.15","ip-address-type":"ipv4","prefix":24}]}]`))
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	ifaces, err := c.QGANetworkInterfaces(context.Background(), "vm-1")
	if err != nil {
		t.Fatalf("QGANetworkInterfaces: %v", err)
	}
	if len(ifaces) != 1 || ifaces[0].Name != "enp0s7" || BestGuestIP(ifaces) != "10.0.2.15" {
		t.Fatalf("unexpected result: %+v", ifaces)
	}
}

func TestNetworkMigrationQuiesceErrorPropagates(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "quiesce failed", http.StatusInternalServerError)
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	err := c.NetworkMigrationQuiesce(context.Background(), "vm-1")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("err=%v", err)
	}
}
