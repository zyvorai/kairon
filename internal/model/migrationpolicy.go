// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package model

const KindMigrationPolicy = "MigrationPolicy"

// MigrationPolicy scopes migration bandwidth/concurrency to Machines
// matching Selector, instead of the global, cluster-wide
// migration.maxConcurrentPerNode/maxConcurrentCluster Helm values (which
// still apply to every migration regardless of any MigrationPolicy) --
// Kairon's namespace-scoped equivalent of KubeVirt's own MigrationPolicy
// CRD. Enforced by kairon-controller's existing migration reconcile loop
// (internal/controller/migrationpolicy.go), the same "block into Pending/
// Blocked, not a webhook" shape MachineQuota's own reconcile-loop
// enforcement already uses -- no admission webhook exists or is needed.
type MigrationPolicy struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta            `json:"metadata"`
	Spec     MigrationPolicySpec   `json:"spec"`
	Status   MigrationPolicyStatus `json:"status,omitempty"`
}

func (p MigrationPolicy) Namespace() string {
	return p.Metadata.Namespace
}

type MigrationPolicyList struct {
	TypeMeta `json:",inline"`
	Items    []MigrationPolicy `json:"items"`
}

type MigrationPolicySpec struct {
	// Selector matches Machines this policy applies to, same shape and
	// semantics as MachineDisruptionBudget.Spec.Selector (LabelsMatch --
	// empty matches nothing, never "everything").
	Selector map[string]string `json:"selector"`
	// BandwidthMbps, when set, becomes a MachineMigration's own
	// spec.bandwidthMbps the first time a matching Machine's migration is
	// admitted, but only if that MachineMigration didn't already set one
	// explicitly (an explicit per-migration value always wins) -- see
	// internal/controller/migrationpolicy.go. When more than one
	// MigrationPolicy in the namespace matches the same Machine, the
	// first one found (Kubernetes list order, not otherwise sorted)
	// supplies the default -- deliberately not merged/most-restrictive,
	// keeping this a simple default, not a second, competing enforcement
	// layer on top of MaxConcurrent below.
	BandwidthMbps uint64 `json:"bandwidthMbps,omitempty"`
	// MaxConcurrent caps how many non-terminal migrations of Machines
	// matching Selector may run at once, cluster-wide -- independent of,
	// and in addition to, migration.maxConcurrentPerNode/maxConcurrentCluster's
	// own global caps. 0 (the default) is unlimited within this policy's
	// own scope (the global caps still apply regardless).
	MaxConcurrent int `json:"maxConcurrent,omitempty"`
}

// MigrationPolicyStatus mirrors MachineDisruptionBudget's own status
// shape -- purely observational, written every reconcile tick as a side
// effect of the same tally AdmitMigrationPolicy already computes.
type MigrationPolicyStatus struct {
	ActiveMigrations int `json:"activeMigrations,omitempty"`
}
