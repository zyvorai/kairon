// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package uiapi

import "net/http"

// This file wraps five CRDs that kaironctl already has full get/describe/
// delete support for (see internal/kaironctl/kaironctl.go), but that
// kairon-ui's own REST API had no route for at all until now: an operator
// using only the dashboard/API -- not kubectl or kaironctl -- had no way
// to see MachineQuota/MachineDisruptionBudget usage (both first-class
// admission-time concerns, see README.md's "Guarding the fleet" section)
// or MachineSet/MachineInstanceType/MigrationPolicy state. Read-only,
// any-authenticated-operator (matching every other read-only
// cross-checkable resource this project exposes, e.g. runtime
// diagnostics/network observability) -- there is no create/update/delete
// route here, matching this first cut's narrow scope; kaironctl and
// kubectl remain the way to mutate any of these five kinds.

func (s *Server) handleListQuotas(w http.ResponseWriter, r *http.Request) {
	items, err := s.Kube.ListMachineQuotasNamespace(r.Context(), namespaceParam(r))
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleListBudgets(w http.ResponseWriter, r *http.Request) {
	items, err := s.Kube.ListMachineDisruptionBudgetsNamespace(r.Context(), namespaceParam(r))
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleListMachineSets(w http.ResponseWriter, r *http.Request) {
	items, err := s.Kube.ListMachineSetsNamespace(r.Context(), namespaceParam(r))
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleListInstanceTypes(w http.ResponseWriter, r *http.Request) {
	items, err := s.Kube.ListMachineInstanceTypesNamespace(r.Context(), namespaceParam(r))
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleListMigrationPolicies(w http.ResponseWriter, r *http.Request) {
	items, err := s.Kube.ListMigrationPoliciesNamespace(r.Context(), namespaceParam(r))
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}
