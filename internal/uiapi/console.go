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

// consoleTicketState binds a ticket to the operator it was issued to, so
// handleConsole's audit log can attribute a console session to a person,
// not just "someone who had a valid ticket."
type consoleTicketState struct {
	username string
	expires  time.Time
}

// issueConsoleTicket mints a single-use ticket for handleConsole, so the
// real session/token credential never has to appear in a URL or access
// log -- only this short-lived, narrow-purpose value does.
func (s *Server) issueConsoleTicket(username string) string {
	buf := make([]byte, 20)
	_, _ = rand.Read(buf)
	ticket := hex.EncodeToString(buf)
	s.consoleTickets.Store(ticket, consoleTicketState{username: username, expires: time.Now().Add(consoleTicketTTL)})
	return ticket
}

// consumeConsoleTicket validates and immediately deletes a ticket --
// presenting the same ticket twice always fails the second time -- and
// returns the username it was issued to.
func (s *Server) consumeConsoleTicket(ticket string) (username string, ok bool) {
	if ticket == "" {
		return "", false
	}
	v, found := s.consoleTickets.LoadAndDelete(ticket)
	if !found {
		return "", false
	}
	st, _ := v.(consoleTicketState)
	if time.Now().After(st.expires) {
		return "", false
	}
	return st.username, true
}

func (s *Server) handleConsoleTicket(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"ticket": s.issueConsoleTicket(usernameFromContext(r.Context()))})
}

// handleConfig reports small, non-sensitive feature toggles the frontend
// needs to decide what to render before the user acts -- e.g. whether to
// show a Machine's "Console" button at all, rather than showing it
// unconditionally and only failing after a click.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"consoleEnabled": s.ConsoleToken != "" && s.ConsolePort != "",
	})
}

// handleConsole relays a browser WebSocket to the target Machine's VNC
// display: kairon-ui -> kairon-node (internal/consoleproxy) -> FluxVM's
// local, unix-socket-only QEMU VNC server. See docs/architecture.md for
// the full chain and its trust boundary.
func (s *Server) handleConsole(w http.ResponseWriter, r *http.Request) {
	username, ok := s.consumeConsoleTicket(r.URL.Query().Get("ticket"))
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid or expired console ticket")
		return
	}
	if s.ConsoleToken == "" || s.ConsolePort == "" {
		writeError(w, http.StatusNotImplemented, "console is not enabled on this deployment")
		return
	}
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	m, err := s.Kube.GetMachine(r.Context(), namespace, name)
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

	scheme := "ws"
	dialOpts := &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + s.ConsoleToken}}}
	if s.ConsoleTLS != nil {
		scheme = "wss"
		dialOpts.HTTPClient = &http.Client{Transport: &http.Transport{TLSClientConfig: s.ConsoleTLS}}
	}
	upstreamURL := fmt.Sprintf("%s://%s:%s/console/%s", scheme, nodeAddr, s.ConsolePort, m.Status.RuntimeID)
	dialCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	upstream, resp, err := websocket.Dial(dialCtx, upstreamURL, dialOpts)
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

	if s.Log != nil {
		s.Log.Info("uiapi console opened", "username", username, "namespace", namespace, "name", name, "remoteAddr", r.RemoteAddr)
	}
	start := time.Now()
	ctx := r.Context()
	relay(
		websocket.NetConn(ctx, downstream, websocket.MessageBinary),
		websocket.NetConn(ctx, upstream, websocket.MessageBinary),
	)
	if s.Log != nil {
		s.Log.Info("uiapi console closed", "username", username, "namespace", namespace, "name", name, "duration", time.Since(start).Round(time.Second).String())
	}
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
