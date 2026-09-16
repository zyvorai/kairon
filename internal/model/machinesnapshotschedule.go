// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package model

import "time"

const KindMachineSnapshotSchedule = "MachineSnapshotSchedule"

// MachineSnapshotSchedule periodically creates a MachineSnapshot for every
// Machine matching Selector, on a plain wall-clock interval -- Kairon's
// first-cut, deliberately simpler analog of a Kubernetes CronJob (see
// Spec.IntervalSeconds's own doc comment for why this isn't real cron
// syntax). Enforced entirely by kairon-controller's reconcile loop
// (internal/controller/machinesnapshotschedule.go); no admission webhook
// exists or is needed, the same reasoning MigrationPolicy's own doc comment
// already gives -- this CRD doesn't gate any other object's admission, it
// only creates new objects on a timer.
type MachineSnapshotSchedule struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta                    `json:"metadata"`
	Spec     MachineSnapshotScheduleSpec   `json:"spec"`
	Status   MachineSnapshotScheduleStatus `json:"status,omitempty"`
}

func (s MachineSnapshotSchedule) Namespace() string {
	return s.Metadata.Namespace
}

type MachineSnapshotScheduleList struct {
	TypeMeta `json:",inline"`
	Items    []MachineSnapshotSchedule `json:"items"`
}

type MachineSnapshotScheduleSpec struct {
	// Selector matches Machines this schedule applies to, same shape and
	// semantics as MachineDisruptionBudget/MigrationPolicy's own Selector
	// (LabelsMatch -- empty matches nothing, never "everything").
	Selector map[string]string `json:"selector"`
	// IntervalSeconds is how often (at minimum) a matching Machine gets a
	// new MachineSnapshot, checked fresh every reconcile tick against
	// Status.LastRunTime -- not a real cron expression. This is a
	// deliberate first-cut simplification (matching this project's
	// Go-stdlib-only bias: no new cron-parsing dependency, and its
	// established pattern of shipping a simpler mechanism honestly labeled
	// as such -- see MigrationPolicy's own plain BandwidthMbps/
	// MaxConcurrent scalars for the same precedent). No jitter/stagger: if
	// several schedules share the same interval they can all fire on the
	// same tick.
	IntervalSeconds int `json:"intervalSeconds"`
	// VolumeSnapshotClassName is passed straight through to every
	// MachineSnapshot this schedule creates, mirroring
	// MachineSnapshotSpec.VolumeSnapshotClassName exactly -- empty uses
	// whatever default reconcileSnapshot itself falls back to.
	VolumeSnapshotClassName string `json:"volumeSnapshotClassName,omitempty"`
	// Suspend pauses this schedule without deleting it -- Due always
	// returns false while set, regardless of how long it's been since the
	// last run.
	Suspend bool `json:"suspend,omitempty"`
}

// Due reports whether this schedule should fire another round of
// MachineSnapshots, given the last time it actually ran (the zero Time if
// it has never run) and the current time. A never-yet-run schedule is
// always immediately due -- it shouldn't have to wait a full interval after
// creation before its very first snapshot.
func (s MachineSnapshotScheduleSpec) Due(lastRun, now time.Time) bool {
	if s.Suspend {
		return false
	}
	if lastRun.IsZero() {
		return true
	}
	return now.Sub(lastRun) >= time.Duration(s.IntervalSeconds)*time.Second
}

// MachineSnapshotScheduleStatus is purely observational, written once per
// schedule at the end of every reconcile tick that found it due -- mirrors
// MigrationPolicyStatus/MachineDisruptionBudgetStatus's own
// recomputed-fresh-every-tick shape, except LastRunTime/LastRunSnapshotCount
// persist across ticks between runs (they're the whole reason Due can tell
// "already ran recently" from "never ran"/"ran long enough ago").
type MachineSnapshotScheduleStatus struct {
	LastRunTime          time.Time `json:"lastRunTime,omitempty"`
	LastRunSnapshotCount int       `json:"lastRunSnapshotCount,omitempty"`
	// LastRunError is set to the first per-Machine MachineSnapshot creation
	// error the last due run encountered, if any -- a schedule can still
	// have LastRunSnapshotCount > 0 alongside a non-empty LastRunError (some
	// matches succeeded, one or more didn't). Cleared (empty) on a run with
	// no errors at all.
	LastRunError string `json:"lastRunError,omitempty"`
}
