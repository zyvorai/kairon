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
		if m.Metadata.DeletionTimestamp != nil || m.DesiredPowerState() == "Stopped" || m.Spec.NodeName == "" {
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
