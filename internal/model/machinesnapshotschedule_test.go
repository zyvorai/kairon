// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
	"time"
)

func TestMachineSnapshotScheduleSpecDue(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		spec    MachineSnapshotScheduleSpec
		lastRun time.Time
		want    bool
	}{
		{
			name:    "never run yet is immediately due",
			spec:    MachineSnapshotScheduleSpec{IntervalSeconds: 3600},
			lastRun: time.Time{},
			want:    true,
		},
		{
			name:    "not yet due",
			spec:    MachineSnapshotScheduleSpec{IntervalSeconds: 3600},
			lastRun: now.Add(-30 * time.Minute),
			want:    false,
		},
		{
			name:    "exactly at the interval boundary is due",
			spec:    MachineSnapshotScheduleSpec{IntervalSeconds: 3600},
			lastRun: now.Add(-1 * time.Hour),
			want:    true,
		},
		{
			name:    "well past the interval is due",
			spec:    MachineSnapshotScheduleSpec{IntervalSeconds: 60},
			lastRun: now.Add(-24 * time.Hour),
			want:    true,
		},
		{
			name:    "suspended never-run-yet is not due",
			spec:    MachineSnapshotScheduleSpec{IntervalSeconds: 60, Suspend: true},
			lastRun: time.Time{},
			want:    false,
		},
		{
			name:    "suspended long-overdue is still not due",
			spec:    MachineSnapshotScheduleSpec{IntervalSeconds: 60, Suspend: true},
			lastRun: now.Add(-24 * time.Hour),
			want:    false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.spec.Due(tc.lastRun, now); got != tc.want {
				t.Errorf("Due(%v, %v) = %v, want %v", tc.lastRun, now, got, tc.want)
			}
		})
	}
}

func TestMachineSnapshotScheduleSpecNextRunAfter(t *testing.T) {
	firedAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		interval int
		want     time.Time
	}{
		{
			name:     "one hour interval projects one hour past the firing time",
			interval: 3600,
			want:     firedAt.Add(time.Hour),
		},
		{
			name:     "one minute interval projects one minute past the firing time",
			interval: 60,
			want:     firedAt.Add(time.Minute),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := MachineSnapshotScheduleSpec{IntervalSeconds: tc.interval}
			if got := spec.NextRunAfter(firedAt); !got.Equal(tc.want) {
				t.Errorf("NextRunAfter(%v) = %v, want %v", firedAt, got, tc.want)
			}
		})
	}
}
