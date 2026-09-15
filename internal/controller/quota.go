// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"fmt"

	"github.com/zyvorai/kairon/internal/model"
)

// quotaTracker accumulates one MachineQuota's usage for the duration of a
// single Reconcile pass -- seeded from already-scheduled Machines, then
// spent as newly-admitted Machines are scheduled within the same pass, so a
// burst of Machine creations in one tick can't all slip through before
// status catches up on the next tick.
type quotaTracker struct {
	quota model.MachineQuota
	used  model.MachineQuotaStatus
}

// buildQuotaTrackers seeds one tracker per MachineQuota from every Machine
// already consuming real capacity in that quota's namespace: not deleted,
// not desired-Stopped, and already scheduled (spec.nodeName set). A Machine
// that exists but hasn't been scheduled yet doesn't count until it actually
// gets in -- quota blocks *new* scheduling, it doesn't evict or retroactively
// un-admit anything.
func buildQuotaTrackers(quotas []model.MachineQuota, machines []model.Machine) (map[string][]*quotaTracker, error) {
	trackers := map[string][]*quotaTracker{}
	for _, q := range quotas {
		trackers[q.Namespace()] = append(trackers[q.Namespace()], &quotaTracker{quota: q})
	}
	for _, m := range machines {
		if !machineCountsTowardQuota(m) {
			continue
		}
		cpu, mem := machineFootprint(m)
		for _, t := range trackers[m.Namespace()] {
			t.used.UsedMachines++
			t.used.UsedTotalCPUCores += cpu
			t.used.UsedTotalMemoryMiB += mem
		}
	}
	for _, list := range trackers {
		for _, t := range list {
			if t.quota.Spec.MaxTotalCPU != "" {
				if _, err := model.ParseVCPUs(t.quota.Spec.MaxTotalCPU); err != nil {
					return nil, fmt.Errorf("MachineQuota %s/%s: invalid maxTotalCpu: %w", t.quota.Namespace(), t.quota.Metadata.Name, err)
				}
			}
			if t.quota.Spec.MaxTotalMemory != "" {
				if _, err := model.ParseMemoryMiB(t.quota.Spec.MaxTotalMemory); err != nil {
					return nil, fmt.Errorf("MachineQuota %s/%s: invalid maxTotalMemory: %w", t.quota.Namespace(), t.quota.Metadata.Name, err)
				}
			}
		}
	}
	return trackers, nil
}

func machineFootprint(m model.Machine) (cpu uint32, memMiB uint64) {
	cpu, _ = model.ParseVCPUs(m.Spec.Resources.CPU)
	memMiB, _ = model.ParseMemoryMiB(m.Spec.Resources.Memory)
	return cpu, memMiB
}

// machineCountsTowardQuota is the one predicate for "does this Machine's
// footprint currently count against its namespace's MachineQuota" --
// shared by buildQuotaTrackers' seed pass and the admission webhook's
// UPDATE handling (see admitQuotaResize) so the two can never drift apart
// on what "already counted" means. Matches quota blocking *new*
// scheduling, not evicting or retroactively un-admitting anything: not
// deleted, not desired-Stopped-or-Halted (both genuinely free the
// runtime/host footprint the same way, see countAssigned's own comment
// in internal/controller/controller.go), and already scheduled.
func machineCountsTowardQuota(m model.Machine) bool {
	desired := m.DesiredPowerState()
	return m.Metadata.DeletionTimestamp == nil && desired != "Stopped" && desired != "Halted" && m.Spec.NodeName != ""
}

// admitQuota returns a non-empty blocker reason if scheduling m would push
// any MachineQuota it's subject to over a limit, otherwise it spends m's
// footprint against every quota in its namespace and returns "".
func admitQuota(trackers map[string][]*quotaTracker, m model.Machine) string {
	cpu, mem := machineFootprint(m)
	for _, t := range trackers[m.Namespace()] {
		if t.quota.Spec.MaxMachines != nil && t.used.UsedMachines+1 > *t.quota.Spec.MaxMachines {
			return fmt.Sprintf("MachineQuota %s/%s: maxMachines %d reached", t.quota.Namespace(), t.quota.Metadata.Name, *t.quota.Spec.MaxMachines)
		}
		if t.quota.Spec.MaxTotalCPU != "" {
			max, _ := model.ParseVCPUs(t.quota.Spec.MaxTotalCPU) // already validated in buildQuotaTrackers
			if t.used.UsedTotalCPUCores+cpu > max {
				return fmt.Sprintf("MachineQuota %s/%s: maxTotalCpu %q reached", t.quota.Namespace(), t.quota.Metadata.Name, t.quota.Spec.MaxTotalCPU)
			}
		}
		if t.quota.Spec.MaxTotalMemory != "" {
			max, _ := model.ParseMemoryMiB(t.quota.Spec.MaxTotalMemory) // already validated in buildQuotaTrackers
			if t.used.UsedTotalMemoryMiB+mem > max {
				return fmt.Sprintf("MachineQuota %s/%s: maxTotalMemory %q reached", t.quota.Namespace(), t.quota.Metadata.Name, t.quota.Spec.MaxTotalMemory)
			}
		}
	}
	for _, t := range trackers[m.Namespace()] {
		t.used.UsedMachines++
		t.used.UsedTotalCPUCores += cpu
		t.used.UsedTotalMemoryMiB += mem
	}
	return ""
}

// admitQuotaResize returns a non-empty blocker reason if growing an
// already-scheduled Machine's spec.resources from old to new would push
// any MachineQuota it's subject to over its CPU/memory limit -- the
// UPDATE counterpart to admitQuota's CREATE check (see
// validateMachine in webhook.go). Deliberately never touches
// MaxMachines: a resize doesn't change how many Machines exist. old must
// be old's already-counted footprint (i.e. the caller has confirmed
// machineCountsTowardQuota(oldMachine) so trackers already includes it,
// seeded by buildQuotaTrackers from the pre-update Machine list) --
// subtracting it before adding new is what makes this check the delta,
// not admitQuota's "add a brand-new footprint on top" one. A shrink
// (new <= old on both dimensions) can never be denied here: it only ever
// lowers usage below what buildQuotaTrackers already saw and accepted.
func admitQuotaResize(trackers map[string][]*quotaTracker, namespace string, oldCPU, newCPU uint32, oldMemMiB, newMemMiB uint64) string {
	for _, t := range trackers[namespace] {
		if t.quota.Spec.MaxTotalCPU != "" {
			max, _ := model.ParseVCPUs(t.quota.Spec.MaxTotalCPU) // already validated in buildQuotaTrackers
			if t.used.UsedTotalCPUCores-oldCPU+newCPU > max {
				return fmt.Sprintf("MachineQuota %s/%s: maxTotalCpu %q reached", t.quota.Namespace(), t.quota.Metadata.Name, t.quota.Spec.MaxTotalCPU)
			}
		}
		if t.quota.Spec.MaxTotalMemory != "" {
			max, _ := model.ParseMemoryMiB(t.quota.Spec.MaxTotalMemory) // already validated in buildQuotaTrackers
			if t.used.UsedTotalMemoryMiB-oldMemMiB+newMemMiB > max {
				return fmt.Sprintf("MachineQuota %s/%s: maxTotalMemory %q reached", t.quota.Namespace(), t.quota.Metadata.Name, t.quota.Spec.MaxTotalMemory)
			}
		}
	}
	return ""
}
