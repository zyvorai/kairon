// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package model

// PersistentVolumeClaim mirrors just the fields Kairon reads from a
// core/v1 PersistentVolumeClaim to resolve a Machine's spec.volumes[]
// entry to a real host path -- not a general-purpose PVC client.
type PersistentVolumeClaim struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta                  `json:"metadata"`
	Spec     PersistentVolumeClaimSpec   `json:"spec"`
	Status   PersistentVolumeClaimStatus `json:"status,omitempty"`
}

type PersistentVolumeClaimSpec struct {
	// VolumeName is set by the Kubernetes binder once the claim is Bound --
	// the name of the PersistentVolume backing it.
	VolumeName string `json:"volumeName,omitempty"`
	// The remaining fields are only meaningful when Kairon is the one
	// creating the PVC (restoring a MachineSnapshot into a new PVC) rather
	// than just reading one to resolve a Machine's boot disk.
	AccessModes      []string                        `json:"accessModes,omitempty"`
	Resources        *PersistentVolumeClaimResources `json:"resources,omitempty"`
	DataSource       *TypedLocalObjectReference      `json:"dataSource,omitempty"`
	StorageClassName *string                         `json:"storageClassName,omitempty"`
}

type PersistentVolumeClaimResources struct {
	Requests map[string]string `json:"requests,omitempty"`
}

// TypedLocalObjectReference points a new PVC's spec.dataSource at the
// VolumeSnapshot to restore from -- same shape Kubernetes itself uses.
type TypedLocalObjectReference struct {
	APIGroup string `json:"apiGroup,omitempty"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
}

type PersistentVolumeClaimStatus struct {
	Phase string `json:"phase,omitempty"`
}

// PersistentVolume mirrors just the fields Kairon reads to turn a bound PV
// into a real host directory a Machine's disk image can live in. Only
// hostPath- and local-backed volumes are resolvable today -- see
// docs/guides/machine-storage.md for why (the PV must already be
// attach-ready on the node Kairon schedules onto; Kairon doesn't run a CSI
// node plugin to attach/mount network-block volumes itself).
type PersistentVolume struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta           `json:"metadata"`
	Spec     PersistentVolumeSpec `json:"spec"`
}

type PersistentVolumeSpec struct {
	// VolumeMode is "Filesystem" (default, empty means Filesystem per the
	// Kubernetes API) or "Block". Only Filesystem-mode volumes are
	// supported: the Machine's disk image is a file inside the volume's
	// directory, not the raw block device itself.
	VolumeMode string                `json:"volumeMode,omitempty"`
	HostPath   *HostPathVolumeSource `json:"hostPath,omitempty"`
	Local      *LocalVolumeSource    `json:"local,omitempty"`
}

type HostPathVolumeSource struct {
	Path string `json:"path,omitempty"`
}

type LocalVolumeSource struct {
	Path string `json:"path,omitempty"`
}
