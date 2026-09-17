// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package model

import "time"

const KindMachineSnapshotSchedule = "MachineSnapshotSchedule"

// SnapshotScheduleLabel is stamped onto every MachineSnapshot a
// MachineSnapshotSchedule creates (value: the schedule's own Metadata.Name),
// the same "operator/controller-asserted fact" label shape
// PinnableCPUsLabel already uses -- reconcileMachineSnapshotSchedules'
// retention pruning (Spec.KeepLast) relies on it to reliably tell its own
// schedule-created snapshots apart from ones a person or script created by
// hand, or ones a *different* schedule created; pruning only ever touches
// a MachineSnapshot carrying this exact label with this exact schedule's
// name as its value.
const SnapshotScheduleLabel = "kairon.zyvor.dev/snapshot-schedule"

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
	// KeepLast, when set (> 0), bounds how many of THIS schedule's own
	// MachineSnapshots are retained per Machine: once a Machine has more
	// than KeepLast snapshots carrying this schedule's SnapshotScheduleLabel
	// AND status.readyToUse -- only ready-to-use ones count toward or
	// against the limit, so a snapshot still in progress is never counted
	// (avoiding a moment with zero completed backups while a new one is
	// still being taken) and never itself a deletion candidate -- the
	// oldest (by metadata.creationTimestamp) beyond the limit are deleted
	// right after this tick's new snapshot is created. Zero (the default)
	// never prunes anything -- snapshots accumulate forever, exactly
	// kairon's behavior before this field existed. A manually-created
	// MachineSnapshot, or one created by a *different* schedule, is never a
	// candidate: only this schedule's own labeled snapshots for that exact
	// Machine are ever counted or deleted.
	KeepLast int `json:"keepLast,omitempty"`
	// StartingDeadlineSeconds, when set (> 0), bounds how late a due run is
	// still allowed to actually fire -- Kubernetes CronJob's own
	// spec.startingDeadlineSeconds, for exactly the same reason: if
	// kairon-controller was down, or this CRD was reinstalled with a stale
	// status.lastRunTime, a schedule's next-due window can end up far in
	// the past by the time reconciliation resumes. Zero (the default)
	// preserves this project's original, simpler behavior -- an overdue
	// schedule always fires immediately, no matter how overdue -- exactly
	// how MachineSnapshotSchedule behaved before this field existed, so
	// enabling it is purely opt-in and never a silent behavior change to an
	// existing schedule. See DeadlineExceeded's own doc comment for exactly
	// what "too late" means and what happens instead of firing.
	StartingDeadlineSeconds int `json:"startingDeadlineSeconds,omitempty"`
}

// Due reports whether this schedule should fire another round of
// MachineSnapshots, given the last time it actually ran (the zero Time if
// it has never run) and the current time. A never-yet-run schedule is
// always immediately due -- it shouldn't have to wait a full interval after
// creation before its very first snapshot.
//
// Due deliberately does NOT itself account for StartingDeadlineSeconds --
// it only answers "has at least one interval elapsed," the same question it
// always has. A caller that also cares whether firing now would be too
// late (a missed-deadline skip rather than a normal run) calls
// DeadlineExceeded separately once Due is true, exactly the same two-step
// shape reconcileMachineSnapshotSchedules and describeSnapshotSchedule's
// preview both use -- keeping "is it due" and "is it too late to fire"
// independently testable, rather than folding a second opt-in concept into
// Due's own long-stable boolean contract.
func (s MachineSnapshotScheduleSpec) Due(lastRun, now time.Time) bool {
	if s.Suspend {
		return false
	}
	if lastRun.IsZero() {
		return true
	}
	return now.Sub(lastRun) >= time.Duration(s.IntervalSeconds)*time.Second
}

// DeadlineExceeded reports whether a due run has been overdue for longer
// than StartingDeadlineSeconds allows -- Kubernetes CronJob's own
// "missed schedule" concept. It's meaningful only once Due(lastRun, now)
// is already true; calling it when the schedule isn't due at all is
// harmless (it still reports honestly whether the -- nonexistent -- due
// window would count as missed) but never something either caller of this
// method actually needs, since both only ever check it after Due.
//
// StartingDeadlineSeconds <= 0 (unset, the default) always returns false --
// no deadline ever applies, preserving the original always-fire-once-due
// behavior. A schedule that has never yet run (the zero lastRun) also
// always returns false: there's no scheduled window to have missed yet, a
// brand-new schedule's very first run can't be "late."
//
// Otherwise, the due window opened at lastRun + IntervalSeconds (the
// instant Due first became true); DeadlineExceeded is true once now has
// moved more than StartingDeadlineSeconds past that instant. Exactly at the
// boundary is still on time (matches Due's own ">=" convention of treating
// the boundary instant as the earliest due moment, not the latest
// allowed one).
func (s MachineSnapshotScheduleSpec) DeadlineExceeded(lastRun, now time.Time) bool {
	if s.StartingDeadlineSeconds <= 0 || lastRun.IsZero() {
		return false
	}
	dueAt := lastRun.Add(time.Duration(s.IntervalSeconds) * time.Second)
	return now.Sub(dueAt) > time.Duration(s.StartingDeadlineSeconds)*time.Second
}

// NextRunAfter projects when this schedule's *next* round is expected,
// given the moment (firedAt) a round has just actually fired -- simply
// firedAt + IntervalSeconds. Called only from the reconciler's Due branch
// (a schedule that didn't fire has nothing new to project; its previously
// stored Status.NextRunTime, if any, is left untouched -- see
// MachineSnapshotScheduleStatus.NextRunTime's own doc comment for why that
// asymmetry is deliberate, not an oversight). Pure and independent of
// Suspend: a caller that just confirmed Due()==true already knows Suspend
// was false at that moment.
func (s MachineSnapshotScheduleSpec) NextRunAfter(firedAt time.Time) time.Time {
	return firedAt.Add(time.Duration(s.IntervalSeconds) * time.Second)
}

// TriggerNowRequested reports whether annotationValue (the live
// AnnotationSnapshotScheduleTriggerNow value on a MachineSnapshotSchedule,
// or "" if unset) names a manual run kairon-controller hasn't handled yet
// (lastHandled, its own status.lastHandledTriggerTime). An empty
// annotationValue is never a request, regardless of lastHandled --
// removing the annotation entirely, or never having set it, must never be
// mistaken for "handle this again." A non-empty annotationValue that
// exactly equals lastHandled is a request that's already been handled --
// the common steady-state case: a schedule sits with a matching pair
// between manual triggers -- so only a genuinely new, different value is
// an outstanding request. See AnnotationSnapshotScheduleTriggerNow's own
// doc comment for the full protocol this implements, and Due/
// DeadlineExceeded above for the two other pure yes/no questions
// reconcileMachineSnapshotSchedules asks about a schedule every tick.
func TriggerNowRequested(annotationValue, lastHandled string) bool {
	return annotationValue != "" && annotationValue != lastHandled
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
	// NextRunTime is set alongside LastRunTime, every time this schedule
	// actually fires, to LastRunTime + IntervalSeconds -- a simple
	// as-of-last-fire projection, not a live countdown recomputed on every
	// reconcile tick (this schedule's own status is only ever touched when
	// Due, exactly like every other field here; adding a tick that patches
	// NextRunTime alone for every not-yet-due schedule would multiply this
	// CRD's write volume for no real benefit, since the projection is a
	// pure function of fields already in Status/Spec). It intentionally
	// does NOT get cleared or recomputed if the schedule is suspended after
	// this projection was made -- kaironctl's own display logic
	// (formatNextRun) checks the live Spec.Suspend flag itself before
	// trusting this field for exactly that reason, so a stale
	// already-passed timestamp is never shown as if it were still
	// meaningful. Zero (the default) means "never yet fired" -- see
	// MachineSnapshotScheduleSpec.NextRunAfter's own doc comment.
	NextRunTime time.Time `json:"nextRunTime,omitempty"`
	// LastHandledTriggerTime records the exact
	// AnnotationSnapshotScheduleTriggerNow value kairon-controller has
	// already acted on -- see TriggerNowRequested and
	// AnnotationSnapshotScheduleTriggerNow's own doc comment for the full
	// request/handled protocol. Empty (the default) means no manual
	// trigger has ever been handled for this schedule. Only ever written
	// alongside LastRunTime/LastRunSnapshotCount/NextRunTime, on the exact
	// tick that actually handles a pending request -- a tick that fires
	// for the ordinary due-interval reason, or skips one for
	// StartingDeadlineSeconds, leaves this field completely untouched
	// (both by this project's json:",omitempty" + merge-patch convention,
	// and by reconcileMachineSnapshotSchedules never setting it on those
	// two paths), so a stale, already-superseded annotation value from a
	// much earlier manual request is never rewritten by an unrelated
	// automatic run.
	LastHandledTriggerTime string `json:"lastHandledTriggerTime,omitempty"`
}
