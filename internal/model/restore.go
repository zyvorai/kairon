// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package model

const KindMachineSnapshotRestore = "MachineSnapshotRestore"

// MachineSnapshotRestore restores one volume of a Succeeded MachineSnapshot
// into a brand-new PersistentVolumeClaim via the standard CSI
// spec.dataSource restore flow. There is no such thing as an in-place PVC
// restore in Kubernetes -- a bound PVC's dataSource can't be swapped after
// the fact -- so "restore" and "clone-from-snapshot" are the same operation
// here: point a new Machine's spec.volumes[0].claimName at
// status.restoredClaimName once this reaches Succeeded (see
// docs/guides/machine-snapshot-restore.md). Deliberately doesn't also create the
// Machine itself -- that's already a solved, separate problem
// (docs/guides/machine-storage.md), and duplicating Machine-spec templating
// here would be real, avoidable scope creep.
type MachineSnapshotRestore struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta                   `json:"metadata"`
	Spec     MachineSnapshotRestoreSpec   `json:"spec"`
	Status   MachineSnapshotRestoreStatus `json:"status,omitempty"`
}

func (r MachineSnapshotRestore) Namespace() string {
	return r.Metadata.Namespace
}

type MachineSnapshotRestoreList struct {
	TypeMeta `json:",inline"`
	Items    []MachineSnapshotRestore `json:"items"`
}

type MachineSnapshotRestoreSpec struct {
	SnapshotName string `json:"snapshotName"`
	// VolumeName selects which of the MachineSnapshot's volumes to restore
	// (matches Machine.spec.volumes[].name at snapshot time) -- optional
	// when the snapshot covers exactly one volume.
	VolumeName string `json:"volumeName,omitempty"`
	// TargetClaimName is the name for the new PersistentVolumeClaim.
	TargetClaimName string `json:"targetClaimName"`
	// StorageClassName and StorageSize are optional overrides. StorageSize
	// defaults to the VolumeSnapshot's own reported restoreSize when unset.
	StorageClassName string `json:"storageClassName,omitempty"`
	StorageSize      string `json:"storageSize,omitempty"`
}

type MachineSnapshotRestoreStatus struct {
	Phase string `json:"phase,omitempty"`
	// No omitempty -- see model.MachineStatus.Message's comment: this
	// status is patched as one whole object, so an omitted key never
	// clears a previously-set value under JSON merge-patch semantics.
	Message           string `json:"message"`
	RestoredClaimName string `json:"restoredClaimName,omitempty"`
}
