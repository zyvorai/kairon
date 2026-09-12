// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package consoleproxy is kairon-node's half of the VNC relay: it
// authenticates a request from kairon-ui with a shared bearer token, asks
// FluxVM for the target VM's workspace directory, and pumps raw bytes
// between a WebSocket and the VM's otherwise-unreachable local QEMU VNC
// socket (<workspace>/vnc.sock -- see crates/fluxvm-qemu/src/lib.rs in the
// FluxVM repo). kairon-node is always co-located with the FluxVM it
// manages (FLUXVM_URL is a loopback address by convention everywhere in
// this repo), so it's the only place that can reach that socket at all.
package consoleproxy

import (
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/zyvorai/kairon/internal/fluxvm"
)

// Server relays VNC bytes for VMs running on this node. Token is a shared
// secret kairon-ui presents -- there is no per-operator identity here,
// that check already happened in kairon-ui (internal/uiapi/console.go)
// before the request ever reaches this node.
type Server struct {
	Flux  *fluxvm.Client
	Token string
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /console/{runtimeID}", s.handleConsole)
	return mux
}

func (s *Server) handleConsole(w http.ResponseWriter, r *http.Request) {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(got) != len(s.Token) || subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
		http.Error(w, "invalid or missing bearer token", http.StatusUnauthorized)
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
