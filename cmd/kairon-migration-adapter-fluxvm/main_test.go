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
	a := newAdapter(log, newFluxClient(flux.URL, ""), "203.0.113.1", "virbr0", 300, migrationNetworks)
	return a, flux
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
