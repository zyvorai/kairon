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
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

func readyString(s string) *string { return &s }

func TestSelectSnapshotVolumeAutoPicksTheOnlyOne(t *testing.T) {
	refs := []model.VolumeSnapshotReference{{VolumeName: "root", VolumeSnapshotName: "snap-root", ReadyToUse: true}}
	got, err := selectSnapshotVolume(refs, "")
	if err != nil || got.VolumeName != "root" {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestSelectSnapshotVolumeRequiresNameWhenAmbiguous(t *testing.T) {
	refs := []model.VolumeSnapshotReference{{VolumeName: "root"}, {VolumeName: "data"}}
	if _, err := selectSnapshotVolume(refs, ""); err == nil {
		t.Fatal("expected an error when more than one volume exists and none is named")
	}
	got, err := selectSnapshotVolume(refs, "data")
	if err != nil || got.VolumeName != "data" {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestSelectSnapshotVolumeRejectsUnknownName(t *testing.T) {
	refs := []model.VolumeSnapshotReference{{VolumeName: "root"}}
	if _, err := selectSnapshotVolume(refs, "nope"); err == nil {
		t.Fatal("expected an error for an unknown volume name")
	}
}

func TestReconcileSnapshotRestoreParksPendingOnANotYetReadySnapshot(t *testing.T) {
	snapshot := model.MachineSnapshot{
		Metadata: model.ObjectMeta{Name: "snap", Namespace: "prod"},
		Status:   model.MachineSnapshotStatus{Phase: "Pending"},
	}
	var restoreStatus model.MachineSnapshotRestoreStatus
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshots/snap":
			_ = json.NewEncoder(w).Encode(snapshot)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshotrestores/r1/status":
			var p struct {
				Status model.MachineSnapshotRestoreStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			restoreStatus = p.Status
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	restore := model.MachineSnapshotRestore{
		Metadata: model.ObjectMeta{Name: "r1", Namespace: "prod"},
		Spec:     model.MachineSnapshotRestoreSpec{SnapshotName: "snap", TargetClaimName: "restored-pvc"},
	}
	// A snapshot that isn't ready *yet* -- e.g. this restore was created
	// slightly before its MachineSnapshot finished -- must not become a
	// permanent Failed with no further retries: reconcileSnapshotRestore
	// itself patches Pending and returns nil, so Controller.Reconcile's own
	// per-item error handling never marks it Failed.
	if err := ctl.reconcileSnapshotRestore(context.Background(), restore); err != nil {
		t.Fatalf("expected a nil error (parked Pending, not Failed), got %v", err)
	}
	if restoreStatus.Phase != "Pending" || !strings.Contains(restoreStatus.Message, "waiting for MachineSnapshot") {
		t.Fatalf("expected a Pending status naming the wait, got %+v", restoreStatus)
	}
}

func TestReconcileSnapshotRestoreParksPendingWhenSnapshotDoesNotExistYet(t *testing.T) {
	var restoreStatus model.MachineSnapshotRestoreStatus
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshots/snap":
			http.Error(w, "not found", http.StatusNotFound)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshotrestores/r1/status":
			var p struct {
				Status model.MachineSnapshotRestoreStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			restoreStatus = p.Status
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	restore := model.MachineSnapshotRestore{
		Metadata: model.ObjectMeta{Name: "r1", Namespace: "prod", CreationTimestamp: time.Now().UTC()},
		Spec:     model.MachineSnapshotRestoreSpec{SnapshotName: "snap", TargetClaimName: "restored-pvc"},
	}
	// A brand-new restore whose MachineSnapshot hasn't hit the API yet --
	// e.g. both were submitted together in one GitOps apply and this
	// restore's own reconcile tick landed first -- must not become a
	// permanent Failed either, the same ordinary-race reasoning as the
	// not-yet-ready case above, just one step earlier.
	if err := ctl.reconcileSnapshotRestore(context.Background(), restore); err != nil {
		t.Fatalf("expected a nil error (parked Pending, not Failed), got %v", err)
	}
	if restoreStatus.Phase != "Pending" || !strings.Contains(restoreStatus.Message, "waiting for MachineSnapshot") {
		t.Fatalf("expected a Pending status naming the wait, got %+v", restoreStatus)
	}
}

func TestReconcileSnapshotRestoreFailsWhenSnapshotNeverAppears(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshots/snap":
			http.Error(w, "not found", http.StatusNotFound)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	restore := model.MachineSnapshotRestore{
		// Old enough (well past missingSnapshotGracePeriod) that a still-
		// missing MachineSnapshot is a genuine, permanent misconfiguration
		// (e.g. a misspelled spec.snapshotName), not an ordinary race --
		// this must fail outright, exactly like before this change.
		Metadata: model.ObjectMeta{Name: "r1", Namespace: "prod", CreationTimestamp: time.Now().UTC().Add(-time.Hour)},
		Spec:     model.MachineSnapshotRestoreSpec{SnapshotName: "snap", TargetClaimName: "restored-pvc"},
	}
	if err := ctl.reconcileSnapshotRestore(context.Background(), restore); err == nil {
		t.Fatal("expected an error for a snapshot that never appeared within the grace period")
	}
}

func TestReconcileSnapshotRestoreParksPendingOnANotYetReadyVolumeSnapshot(t *testing.T) {
	snapshot := model.MachineSnapshot{
		Metadata: model.ObjectMeta{Name: "snap", Namespace: "prod"},
		Status: model.MachineSnapshotStatus{
			Phase: "Succeeded", ReadyToUse: true,
			VolumeSnapshots: []model.VolumeSnapshotReference{{VolumeName: "root", VolumeSnapshotName: "snap-root", ReadyToUse: false}},
		},
	}
	var restoreStatus model.MachineSnapshotRestoreStatus
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshots/snap":
			_ = json.NewEncoder(w).Encode(snapshot)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshotrestores/r1/status":
			var p struct {
				Status model.MachineSnapshotRestoreStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			restoreStatus = p.Status
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	restore := model.MachineSnapshotRestore{
		Metadata: model.ObjectMeta{Name: "r1", Namespace: "prod"},
		Spec:     model.MachineSnapshotRestoreSpec{SnapshotName: "snap", TargetClaimName: "restored-pvc"},
	}
	if err := ctl.reconcileSnapshotRestore(context.Background(), restore); err != nil {
		t.Fatalf("expected a nil error (parked Pending, not Failed), got %v", err)
	}
	if restoreStatus.Phase != "Pending" || !strings.Contains(restoreStatus.Message, "waiting for VolumeSnapshot") {
		t.Fatalf("expected a Pending status naming the wait, got %+v", restoreStatus)
	}
}

func TestReconcileSnapshotRestoreCreatesPVCFromRestoreSize(t *testing.T) {
	snapshot := model.MachineSnapshot{
		Metadata: model.ObjectMeta{Name: "snap", Namespace: "prod"},
		Status: model.MachineSnapshotStatus{
			Phase:      "Succeeded",
			ReadyToUse: true,
			VolumeSnapshots: []model.VolumeSnapshotReference{
				{VolumeName: "root", VolumeSnapshotName: "snap-root", ReadyToUse: true},
			},
		},
	}
	vs := model.VolumeSnapshot{Status: model.VolumeSnapshotStatus{RestoreSize: readyString("20Gi")}}
	var createdPVC model.PersistentVolumeClaim
	var restoreStatus model.MachineSnapshotRestoreStatus
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshots/snap":
			_ = json.NewEncoder(w).Encode(snapshot)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces/prod/persistentvolumeclaims/restored-pvc":
			http.Error(w, "not found", http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Path == "/apis/snapshot.storage.k8s.io/v1/namespaces/prod/volumesnapshots/snap-root":
			_ = json.NewEncoder(w).Encode(vs)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/namespaces/prod/persistentvolumeclaims":
			_ = json.NewDecoder(r.Body).Decode(&createdPVC)
			createdPVC.Status.Phase = "Pending" // WaitForFirstConsumer, not yet bound
			_ = json.NewEncoder(w).Encode(createdPVC)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshotrestores/r1/status":
			var p struct {
				Status model.MachineSnapshotRestoreStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			restoreStatus = p.Status
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	restore := model.MachineSnapshotRestore{
		Metadata: model.ObjectMeta{Name: "r1", Namespace: "prod"},
		Spec:     model.MachineSnapshotRestoreSpec{SnapshotName: "snap", TargetClaimName: "restored-pvc"},
	}
	if err := ctl.reconcileSnapshotRestore(context.Background(), restore); err != nil {
		t.Fatalf("reconcileSnapshotRestore: %v", err)
	}
	if createdPVC.Spec.DataSource == nil || createdPVC.Spec.DataSource.Name != "snap-root" || createdPVC.Spec.DataSource.Kind != "VolumeSnapshot" {
		t.Fatalf("unexpected dataSource: %+v", createdPVC.Spec.DataSource)
	}
	if createdPVC.Spec.Resources == nil || createdPVC.Spec.Resources.Requests["storage"] != "20Gi" {
		t.Fatalf("expected size defaulted from VolumeSnapshot.status.restoreSize, got %+v", createdPVC.Spec.Resources)
	}
	if restoreStatus.Phase != "Pending" || restoreStatus.RestoredClaimName != "restored-pvc" {
		t.Fatalf("expected Pending (WaitForFirstConsumer) with the claim name recorded, got %+v", restoreStatus)
	}
}

func TestReconcileSnapshotRestoreSucceedsOncePVCIsBound(t *testing.T) {
	snapshot := model.MachineSnapshot{
		Metadata: model.ObjectMeta{Name: "snap", Namespace: "prod"},
		Status: model.MachineSnapshotStatus{
			Phase: "Succeeded", ReadyToUse: true,
			VolumeSnapshots: []model.VolumeSnapshotReference{{VolumeName: "root", VolumeSnapshotName: "snap-root", ReadyToUse: true}},
		},
	}
	existingPVC := model.PersistentVolumeClaim{
		Metadata: model.ObjectMeta{Name: "restored-pvc", Namespace: "prod"},
		Status:   model.PersistentVolumeClaimStatus{Phase: "Bound"},
	}
	var restoreStatus model.MachineSnapshotRestoreStatus
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshots/snap":
			_ = json.NewEncoder(w).Encode(snapshot)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces/prod/persistentvolumeclaims/restored-pvc":
			_ = json.NewEncoder(w).Encode(existingPVC)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshotrestores/r1/status":
			var p struct {
				Status model.MachineSnapshotRestoreStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			restoreStatus = p.Status
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	restore := model.MachineSnapshotRestore{
		Metadata: model.ObjectMeta{Name: "r1", Namespace: "prod"},
		Spec:     model.MachineSnapshotRestoreSpec{SnapshotName: "snap", TargetClaimName: "restored-pvc"},
	}
	if err := ctl.reconcileSnapshotRestore(context.Background(), restore); err != nil {
		t.Fatalf("reconcileSnapshotRestore: %v", err)
	}
	if restoreStatus.Phase != "Succeeded" {
		t.Fatalf("expected Succeeded once the PVC is Bound, got %+v", restoreStatus)
	}
}

func TestReconcileSnapshotRestoreIsANoOpOnceTerminal(t *testing.T) {
	ctl := &Controller{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, phase := range []string{"Succeeded", "Failed"} {
		restore := model.MachineSnapshotRestore{Status: model.MachineSnapshotRestoreStatus{Phase: phase}}
		if err := ctl.reconcileSnapshotRestore(context.Background(), restore); err != nil {
			t.Fatalf("phase=%q: expected a no-op, got %v", phase, err)
		}
	}
}
