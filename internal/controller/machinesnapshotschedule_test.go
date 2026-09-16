// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

func snapshotSchedule(name string, lastRun time.Time, intervalSeconds int) model.MachineSnapshotSchedule {
	return model.MachineSnapshotSchedule{
		Metadata: model.ObjectMeta{Name: name, Namespace: "prod"},
		Spec:     model.MachineSnapshotScheduleSpec{Selector: map[string]string{"tier": "web"}, IntervalSeconds: intervalSeconds},
		Status:   model.MachineSnapshotScheduleStatus{LastRunTime: lastRun},
	}
}

func TestReconcileMachineSnapshotSchedulesNotDueCreatesNothing(t *testing.T) {
	var createCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshotschedules":
			_ = json.NewEncoder(w).Encode(model.MachineSnapshotScheduleList{Items: []model.MachineSnapshotSchedule{
				snapshotSchedule("hourly", time.Now(), 3600),
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshots":
			createCalled = true
			w.WriteHeader(http.StatusCreated)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	machines := []model.Machine{webMachine("web-1", "node-a", "Running")}
	if err := ctl.reconcileMachineSnapshotSchedules(context.Background(), machines); err != nil {
		t.Fatalf("reconcileMachineSnapshotSchedules: %v", err)
	}
	if createCalled {
		t.Fatal("expected no MachineSnapshot to be created for a schedule that isn't due yet")
	}
}

func TestReconcileMachineSnapshotSchedulesDueCreatesOnePerMatch(t *testing.T) {
	var created []string
	var patchedStatus model.MachineSnapshotScheduleStatus
	var patchedName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshotschedules":
			_ = json.NewEncoder(w).Encode(model.MachineSnapshotScheduleList{Items: []model.MachineSnapshotSchedule{
				snapshotSchedule("hourly", time.Time{}, 3600), // never run -- immediately due
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshots":
			var s model.MachineSnapshot
			_ = json.NewDecoder(r.Body).Decode(&s)
			created = append(created, s.Spec.MachineName)
			_ = json.NewEncoder(w).Encode(s)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshotschedules/hourly/status":
			patchedName = "hourly"
			var body map[string]model.MachineSnapshotScheduleStatus
			_ = json.NewDecoder(r.Body).Decode(&body)
			patchedStatus = body["status"]
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	machines := []model.Machine{
		webMachine("web-1", "node-a", "Running"),
		webMachine("web-2", "node-a", "Running"),
		{Metadata: model.ObjectMeta{Name: "db-1", Namespace: "prod", Labels: map[string]string{"tier": "db"}}},
	}
	if err := ctl.reconcileMachineSnapshotSchedules(context.Background(), machines); err != nil {
		t.Fatalf("reconcileMachineSnapshotSchedules: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("created = %v, want 2 snapshots (one per web-* Machine, db-1 excluded)", created)
	}
	if patchedName != "hourly" {
		t.Fatal("expected a status patch for the due schedule")
	}
	if patchedStatus.LastRunSnapshotCount != 2 {
		t.Fatalf("LastRunSnapshotCount = %d, want 2", patchedStatus.LastRunSnapshotCount)
	}
	if patchedStatus.LastRunTime.IsZero() {
		t.Fatal("expected LastRunTime to be set")
	}
	if patchedStatus.LastRunError != "" {
		t.Fatalf("LastRunError = %q, want empty", patchedStatus.LastRunError)
	}
}

func TestReconcileMachineSnapshotSchedulesZeroMatchesStillPatchesLastRunTime(t *testing.T) {
	var patched bool
	var status model.MachineSnapshotScheduleStatus
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshotschedules":
			_ = json.NewEncoder(w).Encode(model.MachineSnapshotScheduleList{Items: []model.MachineSnapshotSchedule{
				snapshotSchedule("hourly", time.Time{}, 3600),
			}})
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshotschedules/hourly/status":
			patched = true
			var body map[string]model.MachineSnapshotScheduleStatus
			_ = json.NewDecoder(r.Body).Decode(&body)
			status = body["status"]
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	// No machine has the tier=web label the schedule selects.
	machines := []model.Machine{{Metadata: model.ObjectMeta{Name: "db-1", Namespace: "prod"}}}
	if err := ctl.reconcileMachineSnapshotSchedules(context.Background(), machines); err != nil {
		t.Fatalf("reconcileMachineSnapshotSchedules: %v", err)
	}
	if !patched {
		t.Fatal("expected LastRunTime to be patched even when the selector matches zero Machines, so this schedule doesn't re-fire every tick forever")
	}
	if status.LastRunSnapshotCount != 0 {
		t.Fatalf("LastRunSnapshotCount = %d, want 0", status.LastRunSnapshotCount)
	}
}

func TestReconcileMachineSnapshotSchedulesTolerantWhenCRDNotInstalled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshotschedules" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer srv.Close()
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	if err := ctl.reconcileMachineSnapshotSchedules(context.Background(), nil); err != nil {
		t.Fatalf("reconcileMachineSnapshotSchedules should tolerate a missing CRD, got: %v", err)
	}
}

func TestReconcileMachineSnapshotSchedulesCreateFailureIsLoggedNotFatal(t *testing.T) {
	var patchedStatus model.MachineSnapshotScheduleStatus
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshotschedules":
			_ = json.NewEncoder(w).Encode(model.MachineSnapshotScheduleList{Items: []model.MachineSnapshotSchedule{
				snapshotSchedule("hourly", time.Time{}, 3600),
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshots":
			http.Error(w, "boom", http.StatusInternalServerError)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshotschedules/hourly/status":
			var body map[string]model.MachineSnapshotScheduleStatus
			_ = json.NewDecoder(r.Body).Decode(&body)
			patchedStatus = body["status"]
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	machines := []model.Machine{webMachine("web-1", "node-a", "Running")}
	if err := ctl.reconcileMachineSnapshotSchedules(context.Background(), machines); err != nil {
		t.Fatalf("a per-Machine create failure should not fail the whole reconcile, got: %v", err)
	}
	if patchedStatus.LastRunSnapshotCount != 0 {
		t.Fatalf("LastRunSnapshotCount = %d, want 0 (the only match failed)", patchedStatus.LastRunSnapshotCount)
	}
	if patchedStatus.LastRunError == "" {
		t.Fatal("expected LastRunError to be set")
	}
}
