// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package consoleproxy

import (
	"fmt"
	"net/http"
	"strconv"
)

// handleNetworkEffective forwards to FluxVM's own
// GET /v1/vms/{id}/network/effective -- raw JSON passthrough, like every
// handler in this file, since FluxVM's own handlers return a dynamic
// serde_json::Value here, not a fixed struct.
func (s *Server) handleNetworkEffective(w http.ResponseWriter, r *http.Request) {
	if !s.checkToken(w, r) {
		return
	}
	data, err := s.Flux.GetVMNetworkEffective(r.Context(), r.PathValue("runtimeID"))
	if err != nil {
		http.Error(w, fmt.Sprintf("network effective: %v", err), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

// handleNetworkStats forwards to FluxVM's own
// GET /v1/vms/{id}/network/stats.
func (s *Server) handleNetworkStats(w http.ResponseWriter, r *http.Request) {
	if !s.checkToken(w, r) {
		return
	}
	data, err := s.Flux.GetVMNetworkStats(r.Context(), r.PathValue("runtimeID"))
	if err != nil {
		http.Error(w, fmt.Sprintf("network stats: %v", err), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func parseLimit(r *http.Request) int {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	return limit
}

// handleNetworkFlows forwards to FluxVM's own
// GET /v1/vms/{id}/network/flows?limit=N.
func (s *Server) handleNetworkFlows(w http.ResponseWriter, r *http.Request) {
	if !s.checkToken(w, r) {
		return
	}
	data, err := s.Flux.GetVMNetworkFlows(r.Context(), r.PathValue("runtimeID"), parseLimit(r))
	if err != nil {
		http.Error(w, fmt.Sprintf("network flows: %v", err), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

// handleNetworkDropReasons forwards to FluxVM's own
// GET /v1/vms/{id}/network/drop-reasons?limit=N.
func (s *Server) handleNetworkDropReasons(w http.ResponseWriter, r *http.Request) {
	if !s.checkToken(w, r) {
		return
	}
	data, err := s.Flux.GetVMNetworkDropReasons(r.Context(), r.PathValue("runtimeID"), parseLimit(r))
	if err != nil {
		http.Error(w, fmt.Sprintf("network drop-reasons: %v", err), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}
