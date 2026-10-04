// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package model

import "time"

const (
	KindMachinePool  = "MachinePool"
	KindMachineClaim = "MachineClaim"
)

// A MachinePool keeps spec.replicas booted, unclaimed Machines; a
// MachineClaim takes one of them over by relabelling it, so a claim binds
// in one reconcile tick instead of waiting for a cold boot. Pool members
// carry LabelMachinePool plus LabelPoolState. Only PoolStateWarm members
// count toward spec.replicas, so the pool refills as soon as one is
// claimed. A claimed Machine keeps LabelMachinePool for provenance and gains
// LabelMachineClaim.
const (
	LabelMachinePool         = "kairon.zyvor.dev/machinepool"
	LabelMachinePoolTemplate = "kairon.zyvor.dev/machinepool-template-hash"
	LabelPoolState           = "kairon.zyvor.dev/pool-state"
	LabelMachineClaim        = "kairon.zyvor.dev/machineclaim"

	PoolStateWarm    = "warm"
	PoolStateClaimed = "claimed"

	// FinalizerMachinePool holds a deleting pool until its warm members are
	// gone. Claimed members belong to their claims and are left alone.
	FinalizerMachinePool = "kairon.zyvor.dev/machinepool-cleanup"
	// FinalizerMachineClaim holds a deleting claim until its Machine has
	// been deleted (reclaimPolicy Delete) or released (Retain).
	FinalizerMachineClaim = "kairon.zyvor.dev/machineclaim-cleanup"

	ReclaimDelete = "Delete"
	ReclaimRetain = "Retain"

	ClaimPending = "Pending"
	ClaimBound   = "Bound"
	ClaimLost    = "Lost"
)

type MachinePool struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta        `json:"metadata"`
	Spec     MachinePoolSpec   `json:"spec"`
	Status   MachinePoolStatus `json:"status,omitempty"`
}

func (p MachinePool) Namespace() string {
	return p.Metadata.Namespace
}

type MachinePoolList struct {
	TypeMeta `json:",inline"`
	Items    []MachinePool `json:"items"`
}

type MachinePoolSpec struct {
	// Replicas is how many warm (unclaimed) Machines to keep. The scale
	// subresource points here, so `kubectl scale machinepool` and an HPA
	// work.
	Replicas int             `json:"replicas"`
	Template MachineTemplate `json:"template"`
}

type MachinePoolStatus struct {
	// Replicas counts warm members (booting or Running); ReadyReplicas the
	// Running ones a claim can bind to right now.
	Replicas      int `json:"replicas"`
	ReadyReplicas int `json:"readyReplicas"`
	Claimed       int `json:"claimed"`
	// Selector is the label selector of warm members, for the scale
	// subresource.
	Selector string `json:"selector,omitempty"`
	Message  string `json:"message"`
}

type MachineClaim struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta         `json:"metadata"`
	Spec     MachineClaimSpec   `json:"spec"`
	Status   MachineClaimStatus `json:"status,omitempty"`
}

func (c MachineClaim) Namespace() string {
	return c.Metadata.Namespace
}

type MachineClaimList struct {
	TypeMeta `json:",inline"`
	Items    []MachineClaim `json:"items"`
}

type MachineClaimSpec struct {
	// PoolName is a MachinePool in the claim's namespace.
	PoolName string `json:"poolName"`
	// Labels are added to the Machine when it is bound.
	Labels map[string]string `json:"labels,omitempty"`
	// ReclaimPolicy is Delete (default: deleting the claim deletes the
	// Machine) or Retain (the Machine is unlabelled and kept).
	ReclaimPolicy string `json:"reclaimPolicy,omitempty"`
	// TTLSeconds, when set, deletes the claim this long after it binds,
	// which releases the Machine per ReclaimPolicy.
	TTLSeconds int64 `json:"ttlSeconds,omitempty"`
}

type MachineClaimStatus struct {
	Phase       string     `json:"phase,omitempty"`
	MachineName string     `json:"machineName,omitempty"`
	BoundAt     *time.Time `json:"boundAt,omitempty"`
	// BindMillis is creationTimestamp to bind, in milliseconds.
	BindMillis int64  `json:"bindMillis,omitempty"`
	Message    string `json:"message"`
}
