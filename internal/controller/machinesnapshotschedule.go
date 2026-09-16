// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"sort"
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
//
// A Due schedule whose window is already past spec.startingDeadlineSeconds
// (Spec.DeadlineExceeded) is a third, distinct outcome from "fired" or "not
// due": no MachineSnapshot is created at all this tick, but status is still
// patched -- lastRunTime advances to now (so the next tick starts counting
// a fresh interval rather than re-detecting the same missed window forever),
// lastRunSnapshotCount is 0, and lastRunError names the skip. This is the
// opt-in behavior; startingDeadlineSeconds unset (0, the default) never
// takes this branch, preserving the original always-fire-once-due behavior
// for every existing schedule.
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
		if sched.Spec.DeadlineExceeded(sched.Status.LastRunTime, now) {
			c.Log.Warn("scheduled snapshot run skipped: starting deadline exceeded", "namespace", sched.Namespace(), "schedule", sched.Metadata.Name, "startingDeadlineSeconds", sched.Spec.StartingDeadlineSeconds)
			status := model.MachineSnapshotScheduleStatus{
				LastRunTime:          now,
				LastRunSnapshotCount: 0,
				LastRunError:         fmt.Sprintf("skipped: this run was more than startingDeadlineSeconds (%ds) late", sched.Spec.StartingDeadlineSeconds),
				NextRunTime:          sched.Spec.NextRunAfter(now),
			}
			if statusErr := c.Kube.PatchMachineSnapshotScheduleStatus(ctx, sched.Namespace(), sched.Metadata.Name, status); statusErr != nil {
				c.Log.Error("machine snapshot schedule status patch failed", "namespace", sched.Namespace(), "schedule", sched.Metadata.Name, "error", statusErr)
			}
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
					Labels:    map[string]string{model.SnapshotScheduleLabel: sched.Metadata.Name},
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
			if sched.Spec.KeepLast > 0 {
				c.pruneScheduledSnapshots(ctx, sched.Namespace(), sched.Metadata.Name, m.Metadata.Name, sched.Spec.KeepLast)
			}
		}
		status := model.MachineSnapshotScheduleStatus{LastRunTime: now, LastRunSnapshotCount: count, NextRunTime: sched.Spec.NextRunAfter(now)}
		if firstErr != nil {
			status.LastRunError = firstErr.Error()
		}
		if statusErr := c.Kube.PatchMachineSnapshotScheduleStatus(ctx, sched.Namespace(), sched.Metadata.Name, status); statusErr != nil {
			c.Log.Error("machine snapshot schedule status patch failed", "namespace", sched.Namespace(), "schedule", sched.Metadata.Name, "error", statusErr)
		}
	}
	return nil
}

// pruneScheduledSnapshots deletes the oldest ready-to-use MachineSnapshots
// this exact schedule created for this exact Machine (identified by
// model.SnapshotScheduleLabel, never a manually-created or
// different-schedule-created one) once there are more than keepLast of
// them. Only status.readyToUse snapshots are counted or deleted -- a
// snapshot still Freezing/Thawing/Pending never counts toward the limit and
// is never itself a deletion candidate, so a still-in-progress snapshot can
// never be the thing that gets pruned, and an old-but-still-only-ready
// snapshot is never deleted out from under a not-yet-ready replacement.
// Best-effort: a list or delete failure is logged, counted against the
// existing snapshotschedule reconcile-item-error metric, and left for the
// next due tick to retry -- never fails the schedule's own status patch.
func (c *Controller) pruneScheduledSnapshots(ctx context.Context, ns, scheduleName, machineName string, keepLast int) {
	all, err := c.Kube.ListMachineSnapshotsNamespace(ctx, ns)
	if err != nil {
		c.Log.Error("listing snapshots for schedule pruning failed", "namespace", ns, "schedule", scheduleName, "machine", machineName, "error", err)
		if c.Metrics != nil {
			c.Metrics.ObserveReconcileItemError("snapshotschedule")
		}
		return
	}
	var mine []model.MachineSnapshot
	for _, s := range all {
		if s.Metadata.Labels[model.SnapshotScheduleLabel] == scheduleName && s.Spec.MachineName == machineName && s.Status.ReadyToUse {
			mine = append(mine, s)
		}
	}
	if len(mine) <= keepLast {
		return
	}
	sort.Slice(mine, func(i, j int) bool {
		return mine[i].Metadata.CreationTimestamp.After(mine[j].Metadata.CreationTimestamp)
	})
	for _, old := range mine[keepLast:] {
		if err := c.Kube.DeleteMachineSnapshot(ctx, ns, old.Metadata.Name); err != nil {
			c.Log.Error("scheduled snapshot prune delete failed", "namespace", ns, "schedule", scheduleName, "machine", machineName, "snapshot", old.Metadata.Name, "error", err)
			if c.Metrics != nil {
				c.Metrics.ObserveReconcileItemError("snapshotschedule")
			}
		}
	}
}
