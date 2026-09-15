// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package uiapi

import (
	"context"
	"net/http"
)

// requireCatalogAdmin (internal/uiapi/catalog.go) is reused as-is for
// pools too -- the same "mutating node-scoped FluxVM state is admin-only,
// the deployment needs the console relay configured" posture applies
// verbatim; a pool's own template embeds a full CreateRequest, at least
// as sensitive to let an arbitrary operator mutate as a catalog entry.

// poolTimeout is generous, matching catalogTimeout: creating a pool
// triggers FluxVM's own background backfill (booting spec.size VMs), and
// while the create call itself returns immediately (the backfill is
// async), a claim can involve a real VM resume.
const poolTimeout = execRelayClientTimeout

// handleListPools lists every warm-VM pool on a node: kairon-ui ->
// kairon-node (internal/consoleproxy) -> FluxVM's own GET /v1/pools. Any
// authenticated operator -- read-only visibility, same posture as
// sandboxes/templates/catalog listing.
func (s *Server) handleListPools(w http.ResponseWriter, r *http.Request) {
	nodeName := r.PathValue("node")
	if s.ConsoleToken == "" || s.ConsolePort == "" {
		writeError(w, http.StatusNotImplemented, "pools are not enabled on this deployment")
		return
	}
	nodeAddr, err := s.nodeInternalIP(r.Context(), nodeName)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), execRelayClientTimeout)
	defer cancel()
	var out map[string]any
	if err := s.relayToNodeGet(ctx, nodeAddr, "pools", &out); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetPool returns one pool's current state: kairon-ui -> kairon-node
// -> FluxVM's own GET /v1/pools/{name}. Same any-operator posture as
// handleListPools.
func (s *Server) handleGetPool(w http.ResponseWriter, r *http.Request) {
	nodeName, name := r.PathValue("node"), r.PathValue("name")
	if s.ConsoleToken == "" || s.ConsolePort == "" {
		writeError(w, http.StatusNotImplemented, "pools are not enabled on this deployment")
		return
	}
	nodeAddr, err := s.nodeInternalIP(r.Context(), nodeName)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), execRelayClientTimeout)
	defer cancel()
	var out map[string]any
	if err := s.relayToNodeGet(ctx, nodeAddr, "pools/"+name, &out); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// createPoolRequest mirrors fluxvm.PoolSpec's own JSON shape -- a
// separate wire type from Kairon's own Machine spec, since a pool's
// template is FluxVM's raw CreateVmRequest shape, not a Kubernetes
// Machine spec.
type createPoolRequest struct {
	Name     string         `json:"name"`
	Size     int            `json:"size"`
	Template map[string]any `json:"template"`
}

// handleCreatePool creates a new warm-VM pool on a node: kairon-ui ->
// kairon-node -> FluxVM's own POST /v1/pools. Admin-only, the same
// posture handleAddCatalogEntry/handleBuildTemplate already have --
// booting spec.size VMs ahead of time is a real, ongoing resource
// commitment on the node.
func (s *Server) handleCreatePool(w http.ResponseWriter, r *http.Request) {
	nodeName := r.PathValue("node")
	nodeAddr, ok := s.requireCatalogAdmin(w, r, nodeName)
	if !ok {
		return
	}
	var req createPoolRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), poolTimeout)
	defer cancel()
	if s.Log != nil {
		s.Log.Info("uiapi pool create requested", "username", usernameFromContext(r.Context()), "node", nodeName, "name", req.Name, "size", req.Size)
	}
	var out map[string]any
	if err := s.relayToNode(ctx, nodeAddr, "pools", req, &out); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeletePool deletes a pool and every one of its member VMs:
// kairon-ui -> kairon-node -> FluxVM's own DELETE /v1/pools/{name}.
// Admin-only, and genuinely destructive -- every member VM the pool
// currently holds is deleted too, claimed or not.
func (s *Server) handleDeletePool(w http.ResponseWriter, r *http.Request) {
	nodeName, name := r.PathValue("node"), r.PathValue("name")
	nodeAddr, ok := s.requireCatalogAdmin(w, r, nodeName)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), execRelayClientTimeout)
	defer cancel()
	if s.Log != nil {
		s.Log.Info("uiapi pool delete requested", "username", usernameFromContext(r.Context()), "node", nodeName, "name", name)
	}
	if err := s.relayToNodeDelete(ctx, nodeAddr, "pools/"+name); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type claimPoolRequest struct {
	Name       string `json:"name,omitempty"`
	TTLSeconds *int64 `json:"ttlSeconds,omitempty"`
}

// handleClaimPool pops one ready pool member, resumes it, and returns the
// now-Running VM: kairon-ui -> kairon-node -> FluxVM's own
// POST /v1/pools/{name}/claim. Admin-only, same posture as creating a
// pool. The claimed VM is real FluxVM state, not automatically wired into
// a Kairon Machine object -- see docs/guides/machine-sandboxes.md's
// "Warm pools" section for what that means in practice.
func (s *Server) handleClaimPool(w http.ResponseWriter, r *http.Request) {
	nodeName, name := r.PathValue("node"), r.PathValue("name")
	nodeAddr, ok := s.requireCatalogAdmin(w, r, nodeName)
	if !ok {
		return
	}
	var req claimPoolRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	body := struct {
		Name       string `json:"name,omitempty"`
		TTLSeconds *int64 `json:"ttl_seconds,omitempty"`
	}{Name: req.Name, TTLSeconds: req.TTLSeconds}
	ctx, cancel := context.WithTimeout(r.Context(), execRelayClientTimeout)
	defer cancel()
	if s.Log != nil {
		s.Log.Info("uiapi pool claim requested", "username", usernameFromContext(r.Context()), "node", nodeName, "pool", name)
	}
	var out map[string]any
	if err := s.relayToNode(ctx, nodeAddr, "pools/"+name+"/claim", body, &out); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
