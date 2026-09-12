// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package uiapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/zyvorai/kairon/internal/consoleproxy"
	"github.com/zyvorai/kairon/internal/fluxvm"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

func mustKubeClientAt(t *testing.T, u string) *kube.Client {
	t.Helper()
	kc, err := kube.New(u, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	return kc
}

// dialWS wraps websocket.Dial and always closes the handshake response
// body (the *websocket.Conn returned on success owns the underlying
// connection itself and needs no separate close here).
func dialWS(ctx context.Context, u string, opts *websocket.DialOptions) (*websocket.Conn, error) {
	conn, resp, err := websocket.Dial(ctx, u, opts)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	return conn, err
}

func TestConsoleTicketIsSingleUse(t *testing.T) {
	s := &Server{}
	ticket := s.issueConsoleTicket()
	if !s.consumeConsoleTicket(ticket) {
		t.Fatal("expected a freshly issued ticket to be valid")
	}
	if s.consumeConsoleTicket(ticket) {
		t.Fatal("expected a ticket to be rejected the second time it's presented")
	}
}

func TestConsoleTicketRejectsExpired(t *testing.T) {
	s := &Server{}
	ticket := s.issueConsoleTicket()
	// Overwrite with an already-expired timestamp rather than sleeping
	// past the real (30s) TTL.
	s.consoleTickets.Store(ticket, time.Now().Add(-time.Second))
	if s.consumeConsoleTicket(ticket) {
		t.Fatal("expected an expired ticket to be rejected")
	}
}

func TestConsoleTicketRejectsUnknownOrEmpty(t *testing.T) {
	s := &Server{}
	if s.consumeConsoleTicket("") {
		t.Fatal("expected an empty ticket to be rejected")
	}
	if s.consumeConsoleTicket("never-issued") {
		t.Fatal("expected an unissued ticket to be rejected")
	}
}

// shortTempDir mirrors internal/consoleproxy's helper: AF_UNIX socket
// paths are capped at ~104 bytes on macOS, well under what t.TempDir()'s
// test-name-based nesting produces.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cu")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func listenEchoVNCSocket(t *testing.T, dir string) {
	t.Helper()
	l, err := net.Listen("unix", filepath.Join(dir, "vnc.sock"))
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				if _, werr := conn.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
}

// TestHandleConsoleFullRelay exercises the entire chain this feature
// depends on: browser -> kairon-ui's handleConsole -> a real
// internal/consoleproxy.Server (standing in for kairon-node) -> a real
// Unix socket standing in for QEMU's VNC server. Only the noVNC-in-browser
// leg is out of reach of a Go test.
func TestHandleConsoleTicketIssuesAUsableTicket(t *testing.T) {
	s := &Server{Kube: mustKubeClientAt(t, "http://127.0.0.1:0")}
	h := s.Handler()
	rr := doJSON(t, h, http.MethodPost, "/api/v1/machines/default/vm1/console/ticket", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Ticket == "" {
		t.Fatal("expected a non-empty ticket")
	}
	if !s.consumeConsoleTicket(out.Ticket) {
		t.Fatal("expected the issued ticket to be consumable")
	}
}

func TestHandleConsoleFullRelay(t *testing.T) {
	workspace := shortTempDir(t)
	listenEchoVNCSocket(t, workspace)

	fluxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "runtime-1", "status": "Running", "workspace": workspace})
	}))
	defer fluxSrv.Close()

	const consoleToken = "shared-node-console-token"
	nodeConsole := &consoleproxy.Server{Flux: fluxvm.New(fluxSrv.URL, ""), Token: consoleToken}
	nodeSrv := httptest.NewServer(nodeConsole.Handler())
	defer nodeSrv.Close()
	nodeURL, err := url.Parse(nodeSrv.URL)
	if err != nil {
		t.Fatalf("parse node server URL: %v", err)
	}
	nodeHost, nodePort, err := net.SplitHostPort(nodeURL.Host)
	if err != nil {
		t.Fatalf("split node server host/port: %v", err)
	}

	fk := newFakeKube()
	fk.machines["vm1"] = model.Machine{
		Metadata: model.ObjectMeta{Name: "vm1", Namespace: "default"},
		Spec:     model.MachineSpec{Runtime: model.RuntimeSpec{Backend: "qemu"}},
		Status:   model.MachineStatus{Phase: "Running", NodeName: "worker-1", RuntimeID: "runtime-1"},
	}
	fk.nodes = []model.Node{{
		Metadata: model.ObjectMeta{Name: "worker-1"},
		Status: struct {
			Conditions []model.NodeCondition `json:"conditions,omitempty"`
			Addresses  []model.NodeAddress   `json:"addresses,omitempty"`
		}{Addresses: []model.NodeAddress{{Type: "InternalIP", Address: nodeHost}}},
	}}

	kubeSrv := httptest.NewServer(fk.handler())
	defer kubeSrv.Close()
	kc := mustKubeClientAt(t, kubeSrv.URL)

	s := &Server{Kube: kc, ConsoleToken: consoleToken, ConsolePort: nodePort}
	uiSrv := httptest.NewServer(s.Handler())
	defer uiSrv.Close()

	ticket := s.issueConsoleTicket()
	wsURL := "ws" + strings.TrimPrefix(uiSrv.URL, "http") + "/api/v1/machines/default/vm1/console?ticket=" + ticket
	conn, err := dialWS(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatalf("dial console: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("RFB 003.008\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "RFB 003.008\n" {
		t.Fatalf("expected the byte round trip through both relay hops, got %q", data)
	}
}

func TestHandleConsoleRejectsWhenNotRunning(t *testing.T) {
	fk := newFakeKube()
	fk.machines["vm1"] = model.Machine{
		Metadata: model.ObjectMeta{Name: "vm1", Namespace: "default"},
		Status:   model.MachineStatus{Phase: "Stopped"},
	}
	kubeSrv := httptest.NewServer(fk.handler())
	defer kubeSrv.Close()
	kc := mustKubeClientAt(t, kubeSrv.URL)

	s := &Server{Kube: kc, ConsoleToken: "x", ConsolePort: "8090"}
	uiSrv := httptest.NewServer(s.Handler())
	defer uiSrv.Close()

	ticket := s.issueConsoleTicket()
	wsURL := "ws" + strings.TrimPrefix(uiSrv.URL, "http") + "/api/v1/machines/default/vm1/console?ticket=" + ticket
	if _, err := dialWS(context.Background(), wsURL, nil); err == nil {
		t.Fatal("expected console on a non-Running machine to fail")
	}
}

func TestHandleConsoleDisabledWhenNotConfigured(t *testing.T) {
	fk := newFakeKube()
	fk.machines["vm1"] = model.Machine{
		Metadata: model.ObjectMeta{Name: "vm1", Namespace: "default"},
		Status:   model.MachineStatus{Phase: "Running", NodeName: "worker-1", RuntimeID: "runtime-1"},
	}
	kubeSrv := httptest.NewServer(fk.handler())
	defer kubeSrv.Close()
	kc := mustKubeClientAt(t, kubeSrv.URL)

	s := &Server{Kube: kc} // ConsoleToken/ConsolePort left empty
	uiSrv := httptest.NewServer(s.Handler())
	defer uiSrv.Close()

	ticket := s.issueConsoleTicket()
	wsURL := "ws" + strings.TrimPrefix(uiSrv.URL, "http") + "/api/v1/machines/default/vm1/console?ticket=" + ticket
	if _, err := dialWS(context.Background(), wsURL, nil); err == nil {
		t.Fatal("expected console to be refused when ConsoleToken/ConsolePort aren't configured")
	}
}

func TestHandleConsoleRejectsNonQemuBackend(t *testing.T) {
	fk := newFakeKube()
	fk.machines["vm1"] = model.Machine{
		Metadata: model.ObjectMeta{Name: "vm1", Namespace: "default"},
		Spec:     model.MachineSpec{Runtime: model.RuntimeSpec{Backend: "firecracker"}},
		Status:   model.MachineStatus{Phase: "Running", NodeName: "worker-1", RuntimeID: "runtime-1"},
	}
	kubeSrv := httptest.NewServer(fk.handler())
	defer kubeSrv.Close()
	kc := mustKubeClientAt(t, kubeSrv.URL)

	s := &Server{Kube: kc, ConsoleToken: "x", ConsolePort: "8090"}
	uiSrv := httptest.NewServer(s.Handler())
	defer uiSrv.Close()

	ticket := s.issueConsoleTicket()
	wsURL := "ws" + strings.TrimPrefix(uiSrv.URL, "http") + "/api/v1/machines/default/vm1/console?ticket=" + ticket
	if _, err := dialWS(context.Background(), wsURL, nil); err == nil {
		t.Fatal("expected console on a non-qemu backend to fail")
	}
}
