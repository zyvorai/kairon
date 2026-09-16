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
// diagnostics/network observability) for four of the five.
//
// MachineSet is the one exception: it now also has a DELETE route
// (handleDeleteMachineSet, below), matching handleDeleteMachine's own
// any-authenticated-operator gate exactly (no separate admin check --
// deleting a MachineSet is no more privileged than deleting a Machine
// directly, which any operator can already do). MachineSet was picked
// over the other four because it's the one an operator manages as a
// day-to-day fleet-sizing operation (create/scale/edit all landed via
// kaironctl this same session) rather than a GitOps-managed policy
// object (MachineQuota/MachineDisruptionBudget/MigrationPolicy) or a
// mostly-static reference value (MachineInstanceType) -- deleting a
// MachineSet only ever stops it managing replicas going forward; any
// Machines it already created are ordinary Machines afterward, not
// cascade-deleted (MachineSet carries no finalizer). The other four
// stay list-only, matching this first cut's narrow scope; kaironctl and
// kubectl remain the way to mutate them.

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

// handleDeleteMachineSet mirrors handleDeleteMachine's shape exactly
// (internal/uiapi/machines.go) -- a plain apiserver delete, no separate
// admin gate, no cascade cleanup of Machines the MachineSet already
// created (see the file-level comment above for why that's correct).
func (s *Server) handleDeleteMachineSet(w http.ResponseWriter, r *http.Request) {
	if err := s.Kube.DeleteMachineSet(r.Context(), r.PathValue("namespace"), r.PathValue("name")); err != nil {
		writeUpstreamError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
