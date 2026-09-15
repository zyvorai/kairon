// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package model

const KindMachineInstanceType = "MachineInstanceType"

// MachineInstanceType is a reusable, named CPU/memory shape a Machine
// references via spec.instanceTypeName instead of inlining spec.resources
// itself -- Kairon's equivalent of an EC2 instance type or KubeVirt's
// VirtualMachineInstancetype, kept deliberately to just the resource
// shape (no OS-preference bundling, no CPU model/topology hints) for this
// first cut. See docs/guides/machine-instance-types.md.
type MachineInstanceType struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta              `json:"metadata"`
	Spec     MachineInstanceTypeSpec `json:"spec"`
}

func (t MachineInstanceType) Namespace() string {
	return t.Metadata.Namespace
}

type MachineInstanceTypeList struct {
	TypeMeta `json:",inline"`
	Items    []MachineInstanceType `json:"items"`
}

// MachineInstanceTypeSpec reuses ResourceSpec directly rather than
// duplicating a parallel Cpu/Memory pair -- the exact shape
// kairon-controller copies into a referencing Machine's own
// spec.resources.
type MachineInstanceTypeSpec struct {
	Resources ResourceSpec `json:"resources"`
}
