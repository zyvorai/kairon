// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

// reconcileMachineSnapshotSchedules lists every MachineSnapshotSchedule and,
// for each one Due, creates a MachineSnapshot for every Machine matching its
// Selector -- mirrors reconcileDisruptionBudgetsStatus's own "list all,
// compute, patch status" shape (disruption.go), plus the actual
// object-creation side-effect a pure status reconciler doesn't have. Tolerant
// of the CRD not being installed, like every other optional CRD kind in this
// reconcile loop. A create failure for one Machine is logged and counted
// (kairon_reconcile_item_errors_total{kind="snapshotschedule"}), never fails
// the whole tick or skips the rest of that schedule's matches.
func (c *Controller) reconcileMachineSnapshotSchedules(ctx context.Context, machines []model.Machine) error {
	schedules, err := c.Kube.ListMachineSnapshotSchedules(ctx)
	if err != nil {
		if kube.IsNotFound(err) {
			return nil
		}
		return err
	}
	now := time.Now()
	for _, sched := range schedules {
		if !sched.Spec.Due(sched.Status.LastRunTime, now) {
			continue
		}
		count := 0
		var firstErr error
		for _, m := range machines {
			if m.Namespace() != sched.Namespace() || !model.LabelsMatch(m.Metadata.Labels, sched.Spec.Selector) {
				continue
			}
			snap := model.MachineSnapshot{
				TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachineSnapshot},
				Metadata: model.ObjectMeta{
					Name:      fmt.Sprintf("%s-%d", sched.Metadata.Name, now.Unix()),
					Namespace: sched.Namespace(),
				},
				Spec: model.MachineSnapshotSpec{
					MachineName:             m.Metadata.Name,
					VolumeSnapshotClassName: sched.Spec.VolumeSnapshotClassName,
				},
			}
			if _, err := c.Kube.CreateMachineSnapshot(ctx, sched.Namespace(), snap); err != nil {
				c.Log.Error("scheduled snapshot create failed", "namespace", sched.Namespace(), "schedule", sched.Metadata.Name, "machine", m.Metadata.Name, "error", err)
				if c.Metrics != nil {
					c.Metrics.ObserveReconcileItemError("snapshotschedule")
				}
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			count++
		}
		status := model.MachineSnapshotScheduleStatus{LastRunTime: now, LastRunSnapshotCount: count}
		if firstErr != nil {
			status.LastRunError = firstErr.Error()
		}
		if statusErr := c.Kube.PatchMachineSnapshotScheduleStatus(ctx, sched.Namespace(), sched.Metadata.Name, status); statusErr != nil {
			c.Log.Error("machine snapshot schedule status patch failed", "namespace", sched.Namespace(), "schedule", sched.Metadata.Name, "error", statusErr)
		}
	}
	return nil
}
