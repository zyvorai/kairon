// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package uiapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zyvorai/kairon/internal/model"
)

func TestHandleListPoolsFullRelay(t *testing.T) {
	nodeHost, nodePort, relayToken, cleanup := nodeRelaySetup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/pools" {
			http.Error(w, "bad route", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"name":"warm-agents","size":3,"members":[]}]}`))
	})
	defer cleanup()

	fk := newFakeKube()
	fk.nodes = []model.Node{{
		Metadata: model.ObjectMeta{Name: "worker-1"},
		Status: struct {
			Conditions []model.NodeCondition `json:"conditions,omitempty"`
			Addresses  []model.NodeAddress   `json:"addresses,omitempty"`
		}{Addresses: []model.NodeAddress{{Type: "InternalIP", Address: nodeHost}}},
	}}
	kubeSrv := httptest.NewServer(fk.handler())
	defer kubeSrv.Close()

	s := &Server{
		Kube:          mustKubeClientAt(t, kubeSrv.URL),
		Users:         []User{{Username: "root", PasswordHash: hashFor(t, "correct-horse"), IsAdmin: true}},
		SessionSecret: []byte("test-session-secret"),
		ConsoleToken:  relayToken, ConsolePort: nodePort,
	}
	h := s.Handler()
	token := loginToken(t, h, "root", "correct-horse")

	rr := doJSON(t, h, http.MethodGet, "/api/v1/nodes/worker-1/pools", token, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleCreatePoolDeniesNonAdmin(t *testing.T) {
	fk := newFakeKube()
	fk.nodes = []model.Node{{Metadata: model.ObjectMeta{Name: "worker-1"}}}
	kubeSrv := httptest.NewServer(fk.handler())
	defer kubeSrv.Close()
	s := &Server{
		Kube:          mustKubeClientAt(t, kubeSrv.URL),
		Users:         []User{{Username: "alice", PasswordHash: hashFor(t, "correct-horse")}}, // not admin
		SessionSecret: []byte("test-session-secret"),
		ConsoleToken:  "x", ConsolePort: "8090",
	}
	h := s.Handler()
	token := loginToken(t, h, "alice", "correct-horse")

	rr := doJSON(t, h, http.MethodPost, "/api/v1/nodes/worker-1/pools", token, createPoolRequest{Name: "warm-agents", Size: 3})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a non-admin account, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleCreatePoolFullRelay(t *testing.T) {
	var gotBody string
	nodeHost, nodePort, relayToken, cleanup := nodeRelaySetup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/pools" {
			http.Error(w, "bad route", http.StatusNotFound)
			return
		}
		buf := make([]byte, 2048)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		_, _ = w.Write([]byte(`{"name":"warm-agents","size":3,"members":[]}`))
	})
	defer cleanup()

	fk := newFakeKube()
	fk.nodes = []model.Node{{
		Metadata: model.ObjectMeta{Name: "worker-1"},
		Status: struct {
			Conditions []model.NodeCondition `json:"conditions,omitempty"`
			Addresses  []model.NodeAddress   `json:"addresses,omitempty"`
		}{Addresses: []model.NodeAddress{{Type: "InternalIP", Address: nodeHost}}},
	}}
	kubeSrv := httptest.NewServer(fk.handler())
	defer kubeSrv.Close()

	s := &Server{
		Kube:          mustKubeClientAt(t, kubeSrv.URL),
		Users:         []User{{Username: "root", PasswordHash: hashFor(t, "correct-horse"), IsAdmin: true}},
		SessionSecret: []byte("test-session-secret"),
		ConsoleToken:  relayToken, ConsolePort: nodePort,
	}
	h := s.Handler()
	token := loginToken(t, h, "root", "correct-horse")

	rr := doJSON(t, h, http.MethodPost, "/api/v1/nodes/worker-1/pools", token, createPoolRequest{Name: "warm-agents", Size: 3, Template: map[string]any{"backend": "flux-vm", "image": "alpine-agent", "vcpus": 1, "memory_mib": 512}})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if gotBody == "" {
		t.Fatal("expected the request to reach FluxVM through both relay hops")
	}
}

func TestHandleClaimPoolFullRelay(t *testing.T) {
	var gotPath string
	nodeHost, nodePort, relayToken, cleanup := nodeRelaySetup(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"uuid":"vm-1","status":"Running"}`))
	})
	defer cleanup()

	fk := newFakeKube()
	fk.nodes = []model.Node{{
		Metadata: model.ObjectMeta{Name: "worker-1"},
		Status: struct {
			Conditions []model.NodeCondition `json:"conditions,omitempty"`
			Addresses  []model.NodeAddress   `json:"addresses,omitempty"`
		}{Addresses: []model.NodeAddress{{Type: "InternalIP", Address: nodeHost}}},
	}}
	kubeSrv := httptest.NewServer(fk.handler())
	defer kubeSrv.Close()

	s := &Server{
		Kube:          mustKubeClientAt(t, kubeSrv.URL),
		Users:         []User{{Username: "root", PasswordHash: hashFor(t, "correct-horse"), IsAdmin: true}},
		SessionSecret: []byte("test-session-secret"),
		ConsoleToken:  relayToken, ConsolePort: nodePort,
	}
	h := s.Handler()
	token := loginToken(t, h, "root", "correct-horse")

	rr := doJSON(t, h, http.MethodPost, "/api/v1/nodes/worker-1/pools/warm-agents/claim", token, claimPoolRequest{Name: "agent-run"})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if gotPath != "/v1/pools/warm-agents/claim" {
		t.Fatalf("unexpected upstream path: %q", gotPath)
	}
}

func TestHandleDeletePoolFullRelay(t *testing.T) {
	var gotPath, gotMethod string
	nodeHost, nodePort, relayToken, cleanup := nodeRelaySetup(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.WriteHeader(http.StatusNoContent)
	})
	defer cleanup()

	fk := newFakeKube()
	fk.nodes = []model.Node{{
		Metadata: model.ObjectMeta{Name: "worker-1"},
		Status: struct {
			Conditions []model.NodeCondition `json:"conditions,omitempty"`
			Addresses  []model.NodeAddress   `json:"addresses,omitempty"`
		}{Addresses: []model.NodeAddress{{Type: "InternalIP", Address: nodeHost}}},
	}}
	kubeSrv := httptest.NewServer(fk.handler())
	defer kubeSrv.Close()

	s := &Server{
		Kube:          mustKubeClientAt(t, kubeSrv.URL),
		Users:         []User{{Username: "root", PasswordHash: hashFor(t, "correct-horse"), IsAdmin: true}},
		SessionSecret: []byte("test-session-secret"),
		ConsoleToken:  relayToken, ConsolePort: nodePort,
	}
	h := s.Handler()
	token := loginToken(t, h, "root", "correct-horse")

	rr := doJSON(t, h, http.MethodDelete, "/api/v1/nodes/worker-1/pools/warm-agents", token, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
	if gotPath != "/v1/pools/warm-agents" || gotMethod != http.MethodDelete {
		t.Fatalf("unexpected upstream request: method=%q path=%q", gotMethod, gotPath)
	}
}
