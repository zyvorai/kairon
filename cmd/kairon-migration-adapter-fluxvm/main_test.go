// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zyvorai/kairon/internal/migration"
)

func testAdapter(t *testing.T, fluxHandler http.HandlerFunc, migrationNetworks map[string]string) (*adapter, *httptest.Server) {
	t.Helper()
	flux := httptest.NewServer(fluxHandler)
	t.Cleanup(flux.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := newAdapter(log, newFluxClient(flux.URL, ""), "203.0.113.1", "virbr0", 300, migrationNetworks, "", "", "")
	return a, flux
}

func testAdapterWithTLS(t *testing.T, fluxHandler http.HandlerFunc) *adapter {
	t.Helper()
	flux := httptest.NewServer(fluxHandler)
	t.Cleanup(flux.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return newAdapter(log, newFluxClient(flux.URL, ""), "203.0.113.1", "virbr0", 300, nil, "/etc/ca.pem", "/etc/cert.pem", "/etc/key.pem")
}

func prepareSession() migration.Session {
	return migration.Session{
		ID:         "sess-1",
		Namespace:  "default",
		Machine:    "vm-1",
		SourceNode: "node-a",
		TargetNode: "node-b",
		DiskPath:   "/var/lib/fluxvm/shared/vm-1.qcow2",
		MAC:        "52:54:00:aa:bb:01",
		VCPUs:      2,
		MemoryMiB:  2048,
	}
}

func doPrepare(t *testing.T, a *adapter, session migration.Session) migration.PrepareResult {
	t.Helper()
	body, err := json.Marshal(migration.PrepareRequest{Session: session})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/destination/prepare", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	a.prepare(rec, req)
	var result migration.PrepareResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return result
}

func TestPrepareWithoutMigrationNetworkUsesAdvertiseHost(t *testing.T) {
	a, _ := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		var req receiverRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.MigrationBindAddress != "" {
			t.Errorf("expected empty MigrationBindAddress, got %q", req.MigrationBindAddress)
		}
		writeJSON(w, http.StatusOK, receiverInfo{ID: "recv-1", Status: "Receiving", Port: 45001, ExpiresAt: time.Now().Format(time.RFC3339)})
	}, nil)

	session := prepareSession()
	result := doPrepare(t, a, session)
	if !result.TransferSupported {
		t.Fatalf("expected TransferSupported=true, got %+v", result)
	}
	if result.Endpoint != "tcp:203.0.113.1:45001" {
		t.Errorf("expected default advertise host in endpoint, got %q", result.Endpoint)
	}
}

func TestPrepareWithResolvedMigrationNetworkOverridesAddress(t *testing.T) {
	a, _ := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		var req receiverRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.MigrationBindAddress != "10.10.10.5" {
			t.Errorf("expected MigrationBindAddress 10.10.10.5, got %q", req.MigrationBindAddress)
		}
		writeJSON(w, http.StatusOK, receiverInfo{ID: "recv-1", Status: "Receiving", Port: 45001, ExpiresAt: time.Now().Format(time.RFC3339)})
	}, map[string]string{"migration-fast": "10.10.10.5"})

	session := prepareSession()
	session.MigrationNetwork = "migration-fast"
	result := doPrepare(t, a, session)
	if !result.TransferSupported {
		t.Fatalf("expected TransferSupported=true, got %+v", result)
	}
	if result.Endpoint != "tcp:10.10.10.5:45001" {
		t.Errorf("expected migration-network address in endpoint, got %q", result.Endpoint)
	}
}

func TestPrepareWithUnresolvableMigrationNetworkFailsWithoutCallingFluxVM(t *testing.T) {
	called := false
	a, _ := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		writeJSON(w, http.StatusOK, receiverInfo{})
	}, map[string]string{"migration-fast": "10.10.10.5"})

	session := prepareSession()
	session.MigrationNetwork = "does-not-exist"
	result := doPrepare(t, a, session)
	if result.TransferSupported {
		t.Fatalf("expected TransferSupported=false, got %+v", result)
	}
	if result.Reason == "" {
		t.Error("expected a non-empty Reason explaining the unresolvable network")
	}
	if called {
		t.Error("expected FluxVM not to be called for an unresolvable migrationNetwork")
	}
}

func TestPrepareWithoutTLSConfiguredSendsNoTls(t *testing.T) {
	a, _ := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		var req receiverRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Tls != nil {
			t.Errorf("expected nil Tls, got %+v", req.Tls)
		}
		writeJSON(w, http.StatusOK, receiverInfo{ID: "recv-1", Status: "Receiving", Port: 45001, ExpiresAt: time.Now().Format(time.RFC3339)})
	}, nil)
	result := doPrepare(t, a, prepareSession())
	if result.DataPlaneEncrypted {
		t.Error("expected DataPlaneEncrypted=false when no TLS is configured")
	}
}

func TestPrepareWithTLSConfiguredSendsCertPathsAndNoHostname(t *testing.T) {
	a := testAdapterWithTLS(t, func(w http.ResponseWriter, r *http.Request) {
		var req receiverRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Tls == nil {
			t.Fatal("expected non-nil Tls")
		}
		if req.Tls.CaPath != "/etc/ca.pem" || req.Tls.CertPath != "/etc/cert.pem" || req.Tls.KeyPath != "/etc/key.pem" {
			t.Errorf("unexpected cert paths: %+v", req.Tls)
		}
		if req.Tls.TLSHostname != "" {
			t.Errorf("expected empty TLSHostname on the receiver side, got %q", req.Tls.TLSHostname)
		}
		writeJSON(w, http.StatusOK, receiverInfo{ID: "recv-1", Status: "Receiving", Port: 45001, ExpiresAt: time.Now().Format(time.RFC3339)})
	})
	result := doPrepare(t, a, prepareSession())
	if !result.DataPlaneEncrypted {
		t.Error("expected DataPlaneEncrypted=true when TLS is configured")
	}
}

func TestSourceStartWithTLSConfiguredSendsCertPathsAndParsedHostname(t *testing.T) {
	a := testAdapterWithTLS(t, func(w http.ResponseWriter, r *http.Request) {
		var req migrationStartRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Tls == nil {
			t.Fatal("expected non-nil Tls")
		}
		if req.Tls.CaPath != "/etc/ca.pem" || req.Tls.CertPath != "/etc/cert.pem" || req.Tls.KeyPath != "/etc/key.pem" {
			t.Errorf("unexpected cert paths: %+v", req.Tls)
		}
		if req.Tls.TLSHostname != "10.10.10.5" {
			t.Errorf("expected TLSHostname parsed from endpoint, got %q", req.Tls.TLSHostname)
		}
		writeJSON(w, http.StatusOK, migrationStatus{Phase: "Active", Status: "active"})
	})

	body, err := json.Marshal(migration.SourceRequest{
		Session:   prepareSession(),
		RuntimeID: "vm-1",
		Endpoint:  "tcp:10.10.10.5:45001",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/source/start", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	a.sourceStart(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestEndpointHost(t *testing.T) {
	cases := map[string]string{
		"tcp:10.0.0.5:4444":       "10.0.0.5",
		"tcp:[::1]:4444":          "::1",
		"unix:/run/incoming.sock": "",
		"not-a-uri":               "",
	}
	for endpoint, want := range cases {
		if got := endpointHost(endpoint); got != want {
			t.Errorf("endpointHost(%q) = %q, want %q", endpoint, got, want)
		}
	}
}
