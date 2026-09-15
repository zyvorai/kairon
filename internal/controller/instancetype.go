// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

// resolveInstanceTypes fills in Resources on every Machine that names an
// InstanceTypeName while Resources is still completely empty, from the
// matching (same-namespace) MachineInstanceType -- exactly once per
// Machine: a Machine whose Resources is already set (whether because an
// operator set it directly, or because a previous tick already resolved
// it here) is left untouched, matching this project's "creation-time-only"
// convention for every other field that only takes effect once (spec.image,
// spec.network.forwards, spec.cloudInit -- see docs/guides/machine-storage.md).
//
// Mutates machines in place (not just the API object) so this same tick's
// later steps -- quota tallying, scheduling -- see the resolved footprint
// immediately, rather than admitting a Machine at a false 0 CPU/memory
// footprint for one tick before its real resources are visible (quota's
// own machineFootprint silently treats an empty/invalid quantity as 0,
// so this isn't just a cosmetic staleness -- it would let a resolving
// Machine slip past a quota it should have been blocked by). The API
// object is also patched, so kairon-node and every other reader sees the
// same resolved spec.resources kairon-controller just computed, with no
// special-casing needed anywhere downstream.
func (c *Controller) resolveInstanceTypes(ctx context.Context, machines []model.Machine) []model.Machine {
	needsResolution := false
	for _, m := range machines {
		if machineNeedsInstanceType(m) {
			needsResolution = true
			break
		}
	}
	if !needsResolution {
		return machines
	}

	instanceTypes, err := c.Kube.ListMachineInstanceTypes(ctx)
	if err != nil && !kube.IsNotFound(err) {
		c.Log.Error("list machine instance types failed", "error", err)
		return machines
	}
	byKey := map[string]model.MachineInstanceType{}
	for _, it := range instanceTypes {
		byKey[it.Namespace()+"/"+it.Metadata.Name] = it
	}

	for i, m := range machines {
		if !machineNeedsInstanceType(m) {
			continue
		}
		it, ok := byKey[m.Namespace()+"/"+m.Spec.InstanceTypeName]
		if !ok {
			// Left unresolved -- surfaces downstream as a clear "cpu/memory
			// quantity is empty" error at FluxVM creation time, the same
			// failure mode a hand-typed empty spec.resources already has.
			// No separate error path needed here for a first cut.
			continue
		}
		machines[i].Spec.Resources = it.Spec.Resources
		if err := c.Kube.PatchMachine(ctx, m.Namespace(), m.Metadata.Name, map[string]any{"spec": map[string]any{"resources": it.Spec.Resources}}); err != nil {
			c.Log.Error("patch resolved instance type resources failed", "namespace", m.Namespace(), "machine", m.Metadata.Name, "instanceType", m.Spec.InstanceTypeName, "error", err)
			// Keep the in-memory resolution for this tick regardless --
			// retried against the API again next tick since Resources on
			// the persisted object is still empty.
			continue
		}
		c.Log.Info("resolved machine instance type", "namespace", m.Namespace(), "machine", m.Metadata.Name, "instanceType", m.Spec.InstanceTypeName, "cpu", it.Spec.Resources.CPU, "memory", it.Spec.Resources.Memory)
	}
	return machines
}

func machineNeedsInstanceType(m model.Machine) bool {
	return m.Spec.InstanceTypeName != "" && m.Spec.Resources.CPU == "" && m.Spec.Resources.Memory == ""
}
