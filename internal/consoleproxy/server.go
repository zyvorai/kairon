// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package consoleproxy is kairon-node's relay for operations that need a
// network path only this node has: it authenticates a request from
// kairon-ui with a shared bearer token, then either (VNC) pumps raw bytes
// between a WebSocket and the VM's otherwise-unreachable local QEMU VNC
// socket (<workspace>/vnc.sock -- see crates/fluxvm-qemu/src/lib.rs in the
// FluxVM repo), or (exec) forwards a guest-exec request straight to
// FluxVM's own REST API and relays its synchronous JSON result back.
// kairon-node is always co-located with the FluxVM it manages (FLUXVM_URL
// is a loopback address by convention everywhere in this repo), so it's
// the only place that can reach either at all.
package consoleproxy

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/zyvorai/kairon/internal/fluxvm"
)

// Server relays VNC bytes and guest-exec calls for VMs running on this
// node. Token is a shared secret kairon-ui presents -- there is no
// per-operator identity here, that check already happened in kairon-ui
// (internal/uiapi/console.go) before the request ever reaches this node.
type Server struct {
	Flux  *fluxvm.Client
	Token string
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /console/{runtimeID}", s.handleConsole)
	mux.HandleFunc("POST /exec/{runtimeID}", s.handleExec)
	return mux
}

// checkToken reports whether r carries the correct shared bearer token,
// writing a 401 and returning false otherwise.
func (s *Server) checkToken(w http.ResponseWriter, r *http.Request) bool {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(got) != len(s.Token) || subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
		http.Error(w, "invalid or missing bearer token", http.StatusUnauthorized)
		return false
	}
	return true
}

// execRequest/execResponse mirror internal/fluxvm.QGAExecRequest/Result's
// own JSON shape -- kept as separate types rather than reusing those
// directly so this package's wire contract with kairon-ui doesn't change
// just because internal/fluxvm's Go-side struct layout does.
type execRequest struct {
	Path           string   `json:"path,omitempty"`
	Args           []string `json:"args,omitempty"`
	Powershell     string   `json:"powershell,omitempty"`
	TimeoutSeconds *uint64  `json:"timeoutSeconds,omitempty"`
}

type execResponse struct {
	ExitCode int64  `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// handleExec forwards a guest-exec request to FluxVM's own
// POST /v1/vms/{id}/qga/exec and relays its synchronous result back as
// plain JSON -- unlike VNC, there's no long-lived connection to relay
// here, just one request and one response.
func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	if !s.checkToken(w, r) {
		return
	}
	var req execRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode request: %v", err), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), execRelayTimeout)
	defer cancel()
	result, err := s.Flux.QGAExec(ctx, r.PathValue("runtimeID"), fluxvm.QGAExecRequest{
		Path: req.Path, Args: req.Args, Powershell: req.Powershell, TimeoutSeconds: req.TimeoutSeconds,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("guest exec: %v", err), http.StatusBadGateway)
		return
	}
	_ = json.NewEncoder(w).Encode(execResponse{ExitCode: result.ExitCode, Stdout: result.Stdout, Stderr: result.Stderr})
}

// execRelayTimeout bounds how long this node waits on FluxVM's own
// synchronous guest-exec call -- comfortably above FluxVM's own 60s
// default guest-side timeout (internal/fluxvm.QGAExecRequest.TimeoutSeconds),
// so a caller's explicit, longer timeout still has room to actually apply
// rather than being cut short by this relay hop first.
const execRelayTimeout = 5 * time.Minute

func (s *Server) handleConsole(w http.ResponseWriter, r *http.Request) {
	if !s.checkToken(w, r) {
		return
	}
	runtimeID := r.PathValue("runtimeID")
	rec, err := s.Flux.Get(r.Context(), runtimeID)
	if err != nil {
		http.Error(w, fmt.Sprintf("lookup runtime: %v", err), http.StatusBadGateway)
		return
	}
	if rec.Workspace == "" {
		http.Error(w, "runtime has no workspace (not a QEMU VM, or FluxVM predates workspace reporting)", http.StatusBadRequest)
		return
	}
	sockPath := rec.Workspace + "/vnc.sock"
	sock, err := net.DialTimeout("unix", sockPath, 5*time.Second)
	if err != nil {
		http.Error(w, fmt.Sprintf("dial vnc socket: %v", err), http.StatusBadGateway)
		return
	}
	defer func() { _ = sock.Close() }()

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// This endpoint is only ever dialed server-to-server by kairon-ui,
		// never directly by a browser, so there is no browser Origin to
		// check against.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()

	ws := websocket.NetConn(r.Context(), conn, websocket.MessageBinary)
	relay(ws, sock)
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// relay pumps bytes in both directions until either side closes; it
// returns once the first direction stops, at which point the caller's
// deferred closes tear down the other side too.
func relay(a, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
}
