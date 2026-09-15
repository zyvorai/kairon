// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"

	"github.com/zyvorai/kairon/internal/controller"
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

	growingCPU := targetVCPUs > appliedVCPUs
	growingMemory := targetMemoryMiB > appliedMemoryMiB
	if growingCPU || growingMemory {
		// Self-check against MachineQuota before actually growing --
		// closes a real gap: the admission webhook already enforces this
		// on the same spec.resources UPDATE, but only when webhook.enabled
		// is set (off by default), and kairon-node's own hotplug
		// reconciliation had no cluster-wide quota visibility of its own
		// before this existed. Independent of webhook.enabled -- this
		// runs regardless, so a resize past quota is caught even on a
		// deployment that has never turned the webhook on. Fails open
		// (logs a warning, proceeds with the resize) on any lookup error
		// -- most commonly a kairon-node ServiceAccount that hasn't yet
		// been granted the new read-only machinequotas RBAC this needs
		// (a binary upgraded ahead of its chart) -- rather than newly
		// blocking a resize that always worked before on account of
		// infrastructure this check itself depends on.
		if blocked, reason := a.quotaBlocksResize(ctx, m, targetVCPUs, targetMemoryMiB); blocked {
			a.Log.Warn("hotplug resize blocked by MachineQuota", "machine", m.Metadata.Name, "namespace", m.Namespace(), "reason", reason)
			if growingCPU {
				targetVCPUs = appliedVCPUs
				growingCPU = false
			}
			if growingMemory {
				targetMemoryMiB = appliedMemoryMiB
				growingMemory = false
			}
		}
	}

	if growingCPU {
		newTotal, err := a.Flux.HotplugCPU(ctx, rec.ID(), targetVCPUs-appliedVCPUs)
		if err != nil {
			return appliedVCPUs, appliedMemoryMiB, fmt.Errorf("hotplug cpu: %w", err)
		}
		appliedVCPUs = newTotal
	} else if targetVCPUs < appliedVCPUs {
		a.Log.Warn("spec.resources.cpu requests fewer vCPUs than already hotplugged; shrinking a running Machine is not supported, ignoring",
			"machine", m.Metadata.Name, "applied", appliedVCPUs, "requested", targetVCPUs)
	}

	if growingMemory {
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

// quotaBlocksResize reports whether m's namespace's MachineQuota (if any)
// is already exceeded once m's own footprint is counted at
// (targetVCPUs, targetMemoryMiB) -- i.e. whether kairon-node should refuse
// to actually realize a resize m's own spec.resources is already asking
// for.
//
// Reuses internal/controller's own admission logic
// (QuotaTrackersForNamespace/AdmitQuotaResize) rather than reimplementing
// it, so this can never drift from what the admission webhook itself
// would decide for the identical UPDATE -- but with one real difference
// in how it's called, worth spelling out precisely rather than getting
// subtly wrong: the webhook runs *before* a resize is persisted, so its
// own QuotaTrackersForNamespace call sees every Machine (including the
// one being resized) at its OLD, still-in-effect footprint, and passes
// that as AdmitQuotaResize's (old, new) pair. Here, by contrast, m has
// already been persisted with spec.resources at the NEW (target) value
// by the time kairon-node ever sees it -- so QuotaTrackersForNamespace's
// own fresh Machines list already counts m at target, not at what's
// actually been hotplugged so far (status.appliedVCPUs/appliedMemoryMiB,
// which FluxVM itself has no record of at all -- see reconcileHotplug's
// own comment). Passing (target, target) as AdmitQuotaResize's (old, new)
// arguments exploits that already-seeded value exactly: the function's
// own `used - old + new` arithmetic collapses to `used - target + target`
// = `used`, i.e. "is the total, which already includes m at its target
// footprint, over the limit" -- precisely the question that needs
// answering here, with no double-counting. Passing
// (appliedVCPUs, targetVCPUs) instead -- the more obviously-named pair --
// would be wrong: it would subtract only the already-hotplugged amount
// while the tracker already added the full target, inflating usage by
// (target - applied) and blocking resizes that shouldn't be blocked.
//
// Fails open (blocked=false) on any lookup error -- see
// reconcileHotplug's own comment for why.
func (a *Agent) quotaBlocksResize(ctx context.Context, m model.Machine, targetVCPUs uint32, targetMemoryMiB uint64) (blocked bool, reason string) {
	if !controller.MachineCountsTowardQuota(m) {
		return false, ""
	}
	trackers, ok, err := controller.QuotaTrackersForNamespace(ctx, a.Kube, m.Namespace())
	if err != nil {
		a.Log.Warn("listing MachineQuotas for hotplug resize check failed; proceeding without this check", "machine", m.Metadata.Name, "namespace", m.Namespace(), "error", err)
		return false, ""
	}
	if !ok {
		return false, ""
	}
	if reason := controller.AdmitQuotaResize(trackers, m.Namespace(), targetVCPUs, targetVCPUs, targetMemoryMiB, targetMemoryMiB); reason != "" {
		return true, reason
	}
	return false, ""
}
