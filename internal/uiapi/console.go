// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package uiapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// consoleTicketTTL is deliberately short: a ticket only needs to survive
// the few hundred milliseconds between the browser's ticket-issuing
// fetch() (which can carry the real Authorization header) and its
// follow-up WebSocket handshake (which can't -- browsers don't allow
// custom headers on a native WebSocket upgrade).
const consoleTicketTTL = 30 * time.Second

// issueConsoleTicket mints a single-use ticket for handleConsole, so the
// real session/token credential never has to appear in a URL or access
// log -- only this short-lived, narrow-purpose value does.
func (s *Server) issueConsoleTicket() string {
	buf := make([]byte, 20)
	_, _ = rand.Read(buf)
	ticket := hex.EncodeToString(buf)
	s.consoleTickets.Store(ticket, time.Now().Add(consoleTicketTTL))
	return ticket
}

// consumeConsoleTicket validates and immediately deletes a ticket --
// presenting the same ticket twice always fails the second time.
func (s *Server) consumeConsoleTicket(ticket string) bool {
	if ticket == "" {
		return false
	}
	v, ok := s.consoleTickets.LoadAndDelete(ticket)
	if !ok {
		return false
	}
	expires, _ := v.(time.Time)
	return time.Now().Before(expires)
}

func (s *Server) handleConsoleTicket(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"ticket": s.issueConsoleTicket()})
}

// handleConsole relays a browser WebSocket to the target Machine's VNC
// display: kairon-ui -> kairon-node (internal/consoleproxy) -> FluxVM's
// local, unix-socket-only QEMU VNC server. See docs/architecture.md for
// the full chain and its trust boundary.
func (s *Server) handleConsole(w http.ResponseWriter, r *http.Request) {
	if !s.consumeConsoleTicket(r.URL.Query().Get("ticket")) {
		writeError(w, http.StatusUnauthorized, "invalid or expired console ticket")
		return
	}
	if s.ConsoleToken == "" || s.ConsolePort == "" {
		writeError(w, http.StatusNotImplemented, "console is not enabled on this deployment")
		return
	}
	m, err := s.Kube.GetMachine(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	if m.Status.Phase != "Running" {
		writeError(w, http.StatusConflict, "machine is not Running")
		return
	}
	if backend := m.Spec.Runtime.Backend; backend != "" && backend != "auto" && backend != "qemu" {
		writeError(w, http.StatusBadRequest, "VNC console is only available for the qemu backend")
		return
	}
	if m.Status.RuntimeID == "" || m.Status.NodeName == "" {
		writeError(w, http.StatusConflict, "machine has no runtime yet")
		return
	}
	nodeAddr, err := s.nodeInternalIP(r.Context(), m.Status.NodeName)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	upstreamURL := fmt.Sprintf("ws://%s:%s/console/%s", nodeAddr, s.ConsolePort, m.Status.RuntimeID)
	dialCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	upstream, resp, err := websocket.Dial(dialCtx, upstreamURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + s.ConsoleToken}},
	})
	cancel()
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "connect to node console relay: "+err.Error())
		return
	}
	defer func() { _ = upstream.CloseNow() }()

	downstream, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = downstream.CloseNow() }()

	ctx := r.Context()
	relay(
		websocket.NetConn(ctx, downstream, websocket.MessageBinary),
		websocket.NetConn(ctx, upstream, websocket.MessageBinary),
	)
	_ = downstream.Close(websocket.StatusNormalClosure, "")
}

// nodeInternalIP mirrors internal/agent's targetControlURL node-address
// resolution (used there for the migration peer control plane) -- same
// "look up the Node object, take its InternalIP" logic, duplicated rather
// than shared since internal/agent and internal/uiapi are different
// binaries' packages with no existing common dependency between them.
func (s *Server) nodeInternalIP(ctx context.Context, nodeName string) (string, error) {
	nodes, err := s.Kube.ListNodes(ctx)
	if err != nil {
		return "", err
	}
	for _, node := range nodes {
		if node.Metadata.Name != nodeName {
			continue
		}
		for _, addr := range node.Status.Addresses {
			if addr.Type == "InternalIP" && strings.TrimSpace(addr.Address) != "" {
				return strings.TrimSpace(addr.Address), nil
			}
		}
	}
	return "", fmt.Errorf("node %q has no InternalIP", nodeName)
}

// relay pumps bytes in both directions until either side closes; it
// returns once the first direction stops, at which point the caller's
// deferred closes tear down the other side too. Same small helper as
// internal/consoleproxy's (kairon-node's half of this same relay) --
// duplicated rather than shared across the two binaries' packages.
func relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
}
