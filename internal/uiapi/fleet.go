// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package uiapi

import "net/http"

// suspendScheduleRequest is the body handleSuspendMachineSnapshotSchedule
// expects -- a single boolean, mirroring `kaironctl edit snapshotschedule
// --suspend`'s own one-field-at-a-time patch shape rather than accepting an
// arbitrary spec patch from the dashboard.
type suspendScheduleRequest struct {
	Suspend bool `json:"suspend"`
}

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

// handleListMachineSnapshotSchedules and handleSuspendMachineSnapshotSchedule
// give MachineSnapshotSchedule (added after this file's other five routes)
// its own dashboard visibility -- any-authenticated-operator list, matching
// every other read-only resource here, plus one write action: pausing or
// resuming a schedule without deleting it. Suspend/resume was picked as
// this CRD's one dashboard mutation for the same reason MachineSet got
// delete (see the file-level comment above) -- it's the one action an
// operator reaches for as routine fleet management (pause backups during a
// maintenance window), not a full spec edit; changing selector/interval/
// keepLast still needs kaironctl/kubectl.
func (s *Server) handleListMachineSnapshotSchedules(w http.ResponseWriter, r *http.Request) {
	items, err := s.Kube.ListMachineSnapshotSchedulesNamespace(r.Context(), namespaceParam(r))
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleSuspendMachineSnapshotSchedule(w http.ResponseWriter, r *http.Request) {
	var req suspendScheduleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	patch := map[string]any{"spec": map[string]any{"suspend": req.Suspend}}
	if err := s.Kube.PatchMachineSnapshotSchedule(r.Context(), namespace, name, patch); err != nil {
		writeUpstreamError(w, err)
		return
	}
	updated, err := s.Kube.GetMachineSnapshotSchedule(r.Context(), namespace, name)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}
