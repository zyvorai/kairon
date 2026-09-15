// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package uiapi

import (
	"context"
	"net/http"
)

// agentExecRequest/agentExecResponse mirror internal/consoleproxy's own
// wire shape for the same reason execRequest/execResponse
// (internal/uiapi/exec.go) do.
type agentExecRequest struct {
	Command        string  `json:"command"`
	TimeoutSeconds *uint64 `json:"timeoutSeconds,omitempty"`
}

type agentExecResponse struct {
	ExitCode int32  `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// handleAgentExec runs a command in the guest over FluxVM's own bespoke
// vsock guest agent: kairon-ui -> kairon-node (internal/consoleproxy) ->
// FluxVM's POST /v1/vms/{id}/agent. A genuinely different mechanism from
// handleExec's qemu-guest-agent guest-exec (internal/uiapi/exec.go),
// despite both being "guest exec": this one is backend-agnostic (works
// on Cloud Hypervisor/Firecracker/FluxVm-backend sandboxes too, anywhere
// the vsock agent runs, not just QEMU) and requires
// spec.guestAgent.console rather than spec.guestAgent.enabled -- reuses
// requireGuestFileAccess's own gate (internal/uiapi/agentfile.go) exactly,
// since it's the same channel and the same admin-only posture (arbitrary
// guest command execution is at least as sensitive as arbitrary guest
// file read/write).
func (s *Server) handleAgentExec(w http.ResponseWriter, r *http.Request) {
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	m, username, ok := s.requireGuestFileAccess(w, r, namespace, name)
	if !ok {
		return
	}
	var req agentExecRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	nodeAddr, err := s.nodeInternalIP(r.Context(), m.Status.NodeName)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), execRelayClientTimeout)
	defer cancel()
	if s.Log != nil {
		// Deliberately not logging req.Command -- a command's own
		// arguments can carry secrets, the same reasoning handleExec's
		// own audit line already documents.
		s.Log.Info("uiapi agent exec requested", "username", username, "namespace", namespace, "name", name)
	}
	var out agentExecResponse
	nodePath := "agent-exec/" + m.Status.RuntimeID
	if err := s.relayToNode(ctx, nodeAddr, nodePath, req, &out); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
