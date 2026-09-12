// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"

	"github.com/zyvorai/kairon/internal/fluxvm"
	"github.com/zyvorai/kairon/internal/model"
)

// reconcileHotplug grows a running Machine's live vCPU/memory to match
// spec.resources, without a reboot, via FluxVM's real QMP hotplug (not the
// cgroup-only /resources endpoint). FluxVM has no query endpoint for
// "current live vcpus/memory" -- hotplugged CPUs/DIMMs are pure QMP-time
// state it never persists back into its own VM record -- so
// status.appliedVCPUs/appliedMemoryMiB is the only record of how much of
// spec.resources has actually been realized, and this function is the only
// thing that advances it. Only grows: FluxVM has no CPU/DIMM unplug, so a
// spec.resources decrease is logged and ignored rather than attempted.
func (a *Agent) reconcileHotplug(ctx context.Context, m model.Machine, rec *fluxvm.Record, freshlyCreated bool) (appliedVCPUs uint32, appliedMemoryMiB uint64, err error) {
	targetVCPUs, err := model.ParseVCPUs(m.Spec.Resources.CPU)
	if err != nil {
		return m.Status.AppliedVCPUs, m.Status.AppliedMemoryMiB, fmt.Errorf("parse spec.resources.cpu: %w", err)
	}
	targetMemoryMiB, err := model.ParseMemoryMiB(m.Spec.Resources.Memory)
	if err != nil {
		return m.Status.AppliedVCPUs, m.Status.AppliedMemoryMiB, fmt.Errorf("parse spec.resources.memory: %w", err)
	}
	if freshlyCreated {
		// Just realized against FluxVM with exactly these values -- this is
		// the fresh baseline, not a hotplug event. A VM stop/start cycle
		// (which also lands here, rec having been nil) loses every
		// previously hotplugged resource, since none of it is part of the
		// boot-time -smp/-m args -- so resetting to the plain spec here is
		// correct, not just convenient.
		return targetVCPUs, targetMemoryMiB, nil
	}
	appliedVCPUs, appliedMemoryMiB = m.Status.AppliedVCPUs, m.Status.AppliedMemoryMiB
	if appliedVCPUs == 0 && appliedMemoryMiB == 0 {
		// No recorded baseline -- a Machine adopted from an existing
		// runtime, or reconciled for the first time by a kairon-node build
		// that predates this tracking. Assume spec.resources is already
		// fully realized rather than attempting a hotplug FluxVM would see
		// as a bogus zero-or-negative delta.
		return targetVCPUs, targetMemoryMiB, nil
	}

	if targetVCPUs > appliedVCPUs {
		newTotal, err := a.Flux.HotplugCPU(ctx, rec.ID(), targetVCPUs-appliedVCPUs)
		if err != nil {
			return appliedVCPUs, appliedMemoryMiB, fmt.Errorf("hotplug cpu: %w", err)
		}
		appliedVCPUs = newTotal
	} else if targetVCPUs < appliedVCPUs {
		a.Log.Warn("spec.resources.cpu requests fewer vCPUs than already hotplugged; shrinking a running Machine is not supported, ignoring",
			"machine", m.Metadata.Name, "applied", appliedVCPUs, "requested", targetVCPUs)
	}

	if targetMemoryMiB > appliedMemoryMiB {
		newTotal, err := a.Flux.HotplugMemory(ctx, rec.ID(), targetMemoryMiB-appliedMemoryMiB)
		if err != nil {
			return appliedVCPUs, appliedMemoryMiB, fmt.Errorf("hotplug memory: %w", err)
		}
		appliedMemoryMiB = newTotal
	} else if targetMemoryMiB < appliedMemoryMiB {
		a.Log.Warn("spec.resources.memory requests less memory than already hotplugged; shrinking a running Machine is not supported, ignoring",
			"machine", m.Metadata.Name, "applied", appliedMemoryMiB, "requested", targetMemoryMiB)
	}
	return appliedVCPUs, appliedMemoryMiB, nil
}
