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

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
	"github.com/zyvorai/kairon/internal/scheduler"
)

func TestReconcileSchedulesMachine(t *testing.T) {
	var patchedNode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{{
				Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"},
				Spec:     model.MachineSpec{PowerState: "Running"},
			}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			var n model.Node
			n.Metadata.Name = "worker-1"
			n.Metadata.Labels = map[string]string{model.CapableLabel: "true"}
			n.Status.Conditions = []model.NodeCondition{{Type: "Ready", Status: "True"}}
			_ = json.NewEncoder(w).Encode(model.NodeList{Items: []model.Node{n}})
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/db":
			var p map[string]map[string]string
			_ = json.NewDecoder(r.Body).Decode(&p)
			patchedNode = p["spec"]["nodeName"]
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if patchedNode != "worker-1" {
		t.Fatalf("scheduled node = %q", patchedNode)
	}
}

func TestLiveCutoverSetsAdoptOnlyGuard(t *testing.T) {
	machine := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}, Spec: model.MachineSpec{NodeName: "worker-1", PowerState: "Running"}, Status: model.MachineStatus{NodeName: "worker-1", Phase: "Running", RuntimeID: "vm-1"}}
	migration := model.MachineMigration{Metadata: model.ObjectMeta{Name: "move-db", Namespace: "prod"}, Spec: model.MachineMigrationSpec{MachineName: "db", Strategy: "live"}, Status: model.MachineMigrationStatus{Phase: "Cutover", SourceNode: "worker-1", TargetNode: "worker-2", EffectiveStrategy: "live", RuntimeID: "vm-1"}}
	guarded := false
	phase := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(model.NodeList{})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: []model.MachineMigration{migration}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshots":
			http.NotFound(w, r)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/db":
			var p map[string]any
			_ = json.NewDecoder(r.Body).Decode(&p)
			spec, _ := p["spec"].(map[string]any)
			meta, _ := p["metadata"].(map[string]any)
			annotations, _ := meta["annotations"].(map[string]any)
			guarded = spec["nodeName"] == "worker-2" && annotations[model.AnnotationAdoptOnly] == "true"
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinemigrations/move-db/status":
			var p struct {
				Status model.MachineMigrationStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			phase = p.Status.Phase
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !guarded || phase != "Adopting" {
		t.Fatalf("guarded=%v phase=%q", guarded, phase)
	}
}

func TestSnapshotCreatesStandardCSIVolumeSnapshot(t *testing.T) {
	machine := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}, Spec: model.MachineSpec{NodeName: "worker-1", PowerState: "Running", Volumes: []model.MachineVolume{{Name: "data", ClaimName: "db-data"}}}}
	snapshot := model.MachineSnapshot{Metadata: model.ObjectMeta{Name: "before-upgrade", Namespace: "prod"}, Spec: model.MachineSnapshotSpec{MachineName: "db", VolumeSnapshotClassName: "csi-snap"}}
	createdPVC := ""
	statusReady := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(model.NodeList{})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			http.NotFound(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshots":
			_ = json.NewEncoder(w).Encode(model.MachineSnapshotList{Items: []model.MachineSnapshot{snapshot}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/snapshot.storage.k8s.io/v1/namespaces/prod/volumesnapshots/before-upgrade-data":
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/apis/snapshot.storage.k8s.io/v1/namespaces/prod/volumesnapshots":
			var vs model.VolumeSnapshot
			_ = json.NewDecoder(r.Body).Decode(&vs)
			if vs.Spec.Source.PersistentVolumeClaimName != nil {
				createdPVC = *vs.Spec.Source.PersistentVolumeClaimName
			}
			ready := true
			vs.Status.ReadyToUse = &ready
			_ = json.NewEncoder(w).Encode(vs)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshots/before-upgrade/status":
			var p struct {
				Status model.MachineSnapshotStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			statusReady = p.Status.Phase == "Succeeded" && p.Status.ReadyToUse
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if createdPVC != "db-data" || !statusReady {
		t.Fatalf("createdPVC=%q ready=%v", createdPVC, statusReady)
	}
}

func TestColdMigrationStopsSource(t *testing.T) {
	machine := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}, Spec: model.MachineSpec{NodeName: "worker-1", PowerState: "Running"}, Status: model.MachineStatus{NodeName: "worker-1", Phase: "Running"}}
	migration := model.MachineMigration{Metadata: model.ObjectMeta{Name: "cold-db", Namespace: "prod"}, Spec: model.MachineMigrationSpec{MachineName: "db", Strategy: "cold", TargetNode: "worker-2"}}
	stopped := false
	phase := ""
	readyNode := func(name string) model.Node {
		var n model.Node
		n.Metadata.Name = name
		n.Metadata.Labels = map[string]string{model.CapableLabel: "true"}
		n.Status.Conditions = []model.NodeCondition{{Type: "Ready", Status: "True"}}
		return n
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(model.NodeList{Items: []model.Node{readyNode("worker-1"), readyNode("worker-2")}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: []model.MachineMigration{migration}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshots":
			http.NotFound(w, r)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/db":
			var p map[string]map[string]string
			_ = json.NewDecoder(r.Body).Decode(&p)
			stopped = p["spec"]["powerState"] == "Stopped"
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinemigrations/cold-db/status":
			var p struct {
				Status model.MachineMigrationStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			phase = p.Status.Phase
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !stopped || phase != "Stopping" {
		t.Fatalf("stopped=%v phase=%q", stopped, phase)
	}
}

func TestColdMigrationReassignsAfterSourceStopped(t *testing.T) {
	machine := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}, Spec: model.MachineSpec{NodeName: "worker-1", PowerState: "Stopped"}, Status: model.MachineStatus{NodeName: "worker-1", Phase: "Stopped"}}
	migration := model.MachineMigration{Metadata: model.ObjectMeta{Name: "cold-db", Namespace: "prod"}, Spec: model.MachineMigrationSpec{MachineName: "db", Strategy: "cold", TargetNode: "worker-2"}, Status: model.MachineMigrationStatus{Phase: "Stopping", SourceNode: "worker-1", TargetNode: "worker-2", EffectiveStrategy: "cold"}}
	reassigned := false
	phase := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(model.NodeList{})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: []model.MachineMigration{migration}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshots":
			http.NotFound(w, r)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/db":
			var p map[string]map[string]string
			_ = json.NewDecoder(r.Body).Decode(&p)
			reassigned = p["spec"]["nodeName"] == "worker-2" && p["spec"]["powerState"] == "Running"
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinemigrations/cold-db/status":
			var p struct {
				Status model.MachineMigrationStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			phase = p.Status.Phase
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reassigned || phase != "Restarting" {
		t.Fatalf("reassigned=%v phase=%q", reassigned, phase)
	}
}

func readyCapableNode(name string) model.Node {
	var n model.Node
	n.Metadata.Name = name
	n.Metadata.Labels = map[string]string{model.CapableLabel: "true"}
	n.Status.Conditions = []model.NodeCondition{{Type: "Ready", Status: "True"}}
	return n
}

func TestLiveMigrationEntersStarting(t *testing.T) {
	machine := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}, Spec: model.MachineSpec{NodeName: "worker-1", PowerState: "Running", Runtime: model.RuntimeSpec{Backend: "qemu"}}, Status: model.MachineStatus{NodeName: "worker-1", Phase: "Running"}}
	migration := model.MachineMigration{Metadata: model.ObjectMeta{Name: "move-db", Namespace: "prod"}, Spec: model.MachineMigrationSpec{MachineName: "db", Strategy: "live"}}
	phase, strategy := "", ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(model.NodeList{Items: []model.Node{readyCapableNode("worker-1"), readyCapableNode("worker-2")}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: []model.MachineMigration{migration}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshots":
			http.NotFound(w, r)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinemigrations/move-db/status":
			var p struct {
				Status model.MachineMigrationStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			phase = p.Status.Phase
			strategy = p.Status.EffectiveStrategy
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if phase != "Starting" || strategy != "live" {
		t.Fatalf("phase=%q strategy=%q", phase, strategy)
	}
}

func TestLiveMigrationAdoptingCompletesToSucceeded(t *testing.T) {
	machine := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}, Spec: model.MachineSpec{NodeName: "worker-2", PowerState: "Running"}, Status: model.MachineStatus{NodeName: "worker-2", Phase: "Running"}}
	migration := model.MachineMigration{Metadata: model.ObjectMeta{Name: "move-db", Namespace: "prod"}, Spec: model.MachineMigrationSpec{MachineName: "db", Strategy: "live"}, Status: model.MachineMigrationStatus{Phase: "Adopting", SourceNode: "worker-1", TargetNode: "worker-2", EffectiveStrategy: "live"}}
	phase := ""
	clearedAnnotations := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(model.NodeList{})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: []model.MachineMigration{migration}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshots":
			http.NotFound(w, r)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/db":
			var p map[string]any
			_ = json.NewDecoder(r.Body).Decode(&p)
			meta, _ := p["metadata"].(map[string]any)
			annotations, _ := meta["annotations"].(map[string]any)
			_, hasAdoptOnly := annotations[model.AnnotationAdoptOnly]
			_, hasRef := annotations[model.AnnotationMigrationRef]
			clearedAnnotations = hasAdoptOnly && annotations[model.AnnotationAdoptOnly] == nil && hasRef && annotations[model.AnnotationMigrationRef] == nil
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinemigrations/move-db/status":
			var p struct {
				Status model.MachineMigrationStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			phase = p.Status.Phase
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if phase != "Succeeded" || !clearedAnnotations {
		t.Fatalf("phase=%q clearedAnnotations=%v", phase, clearedAnnotations)
	}
}

func TestColdMigrationRestartingCompletesToSucceeded(t *testing.T) {
	machine := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}, Spec: model.MachineSpec{NodeName: "worker-2", PowerState: "Running"}, Status: model.MachineStatus{NodeName: "worker-2", Phase: "Running"}}
	migration := model.MachineMigration{Metadata: model.ObjectMeta{Name: "cold-db", Namespace: "prod"}, Spec: model.MachineMigrationSpec{MachineName: "db", Strategy: "cold", TargetNode: "worker-2"}, Status: model.MachineMigrationStatus{Phase: "Restarting", SourceNode: "worker-1", TargetNode: "worker-2", EffectiveStrategy: "cold"}}
	phase := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(model.NodeList{})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: []model.MachineMigration{migration}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshots":
			http.NotFound(w, r)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinemigrations/cold-db/status":
			var p struct {
				Status model.MachineMigrationStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			phase = p.Status.Phase
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if phase != "Succeeded" {
		t.Fatalf("phase=%q", phase)
	}
}

func TestReconcileMigrationSkipsTerminalPhases(t *testing.T) {
	for _, terminal := range []string{"Succeeded", "Failed", "Blocked", "NeedsRecovery"} {
		t.Run(terminal, func(t *testing.T) {
			machine := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}, Spec: model.MachineSpec{NodeName: "worker-1", PowerState: "Running"}, Status: model.MachineStatus{NodeName: "worker-1", Phase: "Running"}}
			migration := model.MachineMigration{Metadata: model.ObjectMeta{Name: "move-db", Namespace: "prod"}, Spec: model.MachineMigrationSpec{MachineName: "db", Strategy: "live"}, Status: model.MachineMigrationStatus{Phase: terminal}}
			patched := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
					_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
					_ = json.NewEncoder(w).Encode(model.NodeList{})
				case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
					_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: []model.MachineMigration{migration}})
				case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshots":
					http.NotFound(w, r)
				case r.Method == http.MethodPatch:
					patched = true
					w.WriteHeader(http.StatusOK)
				default:
					http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
				}
			}))
			defer srv.Close()
			kc, _ := kube.New(srv.URL, "", "", false)
			kc.HTTP = srv.Client()
			ctl := &Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
			if err := ctl.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			if patched {
				t.Fatalf("expected no PATCH for terminal phase %q", terminal)
			}
		})
	}
}

func TestMigrationBlockedWhenMachineUnscheduled(t *testing.T) {
	machine := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}, Spec: model.MachineSpec{PowerState: "Running"}}
	migration := model.MachineMigration{Metadata: model.ObjectMeta{Name: "move-db", Namespace: "prod"}, Spec: model.MachineMigrationSpec{MachineName: "db", Strategy: "live"}}
	phase, message := "", ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(model.NodeList{})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: []model.MachineMigration{migration}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshots":
			http.NotFound(w, r)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinemigrations/move-db/status":
			var p struct {
				Status model.MachineMigrationStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			phase, message = p.Status.Phase, p.Status.Message
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/db/status":
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if phase != "Blocked" || message != "Machine has not been scheduled yet" {
		t.Fatalf("phase=%q message=%q", phase, message)
	}
}

func TestMigrationBlockedWhenNoEligibleTarget(t *testing.T) {
	machine := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}, Spec: model.MachineSpec{NodeName: "worker-1", PowerState: "Running"}, Status: model.MachineStatus{NodeName: "worker-1", Phase: "Running"}}
	migration := model.MachineMigration{Metadata: model.ObjectMeta{Name: "move-db", Namespace: "prod"}, Spec: model.MachineMigrationSpec{MachineName: "db", Strategy: "live"}}
	phase, message := "", ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			// Only the source node exists (Choose excludes it), so no target is eligible.
			_ = json.NewEncoder(w).Encode(model.NodeList{Items: []model.Node{readyCapableNode("worker-1")}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: []model.MachineMigration{migration}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshots":
			http.NotFound(w, r)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinemigrations/move-db/status":
			var p struct {
				Status model.MachineMigrationStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			phase, message = p.Status.Phase, p.Status.Message
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if phase != "Blocked" || !strings.Contains(message, "no migration target available") {
		t.Fatalf("phase=%q message=%q", phase, message)
	}
}

func TestMigrationBlockedWhenStrategyUnsupported(t *testing.T) {
	machine := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}, Spec: model.MachineSpec{NodeName: "worker-1", PowerState: "Running"}, Status: model.MachineStatus{NodeName: "worker-1", Phase: "Running"}}
	migration := model.MachineMigration{Metadata: model.ObjectMeta{Name: "move-db", Namespace: "prod"}, Spec: model.MachineMigrationSpec{MachineName: "db", Strategy: "warm"}}
	phase, message := "", ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(model.NodeList{Items: []model.Node{readyCapableNode("worker-2")}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: []model.MachineMigration{migration}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshots":
			http.NotFound(w, r)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinemigrations/move-db/status":
			var p struct {
				Status model.MachineMigrationStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			phase, message = p.Status.Phase, p.Status.Message
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if phase != "Blocked" || !strings.Contains(message, `unsupported migration strategy "warm"`) {
		t.Fatalf("phase=%q message=%q", phase, message)
	}
}

func TestMigrationBlockedWhenLiveOnNonQemuBackend(t *testing.T) {
	machine := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}, Spec: model.MachineSpec{NodeName: "worker-1", PowerState: "Running", Runtime: model.RuntimeSpec{Backend: "container"}}, Status: model.MachineStatus{NodeName: "worker-1", Phase: "Running"}}
	migration := model.MachineMigration{Metadata: model.ObjectMeta{Name: "move-db", Namespace: "prod"}, Spec: model.MachineMigrationSpec{MachineName: "db", Strategy: "live"}}
	phase, message := "", ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(model.NodeList{Items: []model.Node{readyCapableNode("worker-2")}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: []model.MachineMigration{migration}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshots":
			http.NotFound(w, r)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinemigrations/move-db/status":
			var p struct {
				Status model.MachineMigrationStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			phase, message = p.Status.Phase, p.Status.Message
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if phase != "Blocked" || !strings.Contains(message, "live migration requires qemu backend") {
		t.Fatalf("phase=%q message=%q", phase, message)
	}
}

func TestReconcileFailedWrapperPatchesFailedStatus(t *testing.T) {
	migration := model.MachineMigration{Metadata: model.ObjectMeta{Name: "move-db", Namespace: "prod"}, Spec: model.MachineMigrationSpec{MachineName: ""}}
	phase, message := "", ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(model.NodeList{})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: []model.MachineMigration{migration}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshots":
			http.NotFound(w, r)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinemigrations/move-db/status":
			var p struct {
				Status model.MachineMigrationStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			phase, message = p.Status.Phase, p.Status.Message
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if phase != "Failed" || !strings.Contains(message, "spec.machineName is required") {
		t.Fatalf("phase=%q message=%q", phase, message)
	}
}

func TestReconcileMigrationUnknownPhaseErrors(t *testing.T) {
	machine := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}, Spec: model.MachineSpec{NodeName: "worker-1", PowerState: "Running"}, Status: model.MachineStatus{NodeName: "worker-1", Phase: "Running"}}
	migration := model.MachineMigration{Metadata: model.ObjectMeta{Name: "move-db", Namespace: "prod"}, Spec: model.MachineMigrationSpec{MachineName: "db", Strategy: "live"}, Status: model.MachineMigrationStatus{Phase: "Bogus"}}
	phase, message := "", ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(model.NodeList{})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: []model.MachineMigration{migration}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshots":
			http.NotFound(w, r)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinemigrations/move-db/status":
			var p struct {
				Status model.MachineMigrationStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			phase, message = p.Status.Phase, p.Status.Message
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if phase != "Failed" || !strings.Contains(message, `unknown migration status phase "Bogus"`) {
		t.Fatalf("phase=%q message=%q", phase, message)
	}
}

func TestEffectiveStrategyAutoFallsBackToCold(t *testing.T) {
	machine := model.Machine{}
	for _, strategy := range []string{"", "auto"} {
		got, err := effectiveStrategy(machine, model.MachineMigration{Spec: model.MachineMigrationSpec{Strategy: strategy}})
		if err != nil || got != "cold" {
			t.Fatalf("strategy=%q got=%q err=%v", strategy, got, err)
		}
	}
}

func TestEffectiveStrategyRejectsUnsupported(t *testing.T) {
	_, err := effectiveStrategy(model.Machine{}, model.MachineMigration{Spec: model.MachineMigrationSpec{Strategy: "warm"}})
	if err == nil || !strings.Contains(err.Error(), `unsupported migration strategy "warm"`) {
		t.Fatalf("err=%v", err)
	}
}

func TestEffectiveStrategyLiveRequiresQemu(t *testing.T) {
	for _, backend := range []string{"", "auto", "qemu"} {
		machine := model.Machine{Spec: model.MachineSpec{Runtime: model.RuntimeSpec{Backend: backend}}}
		got, err := effectiveStrategy(machine, model.MachineMigration{Spec: model.MachineMigrationSpec{Strategy: "live"}})
		if err != nil || got != "live" {
			t.Fatalf("backend=%q got=%q err=%v", backend, got, err)
		}
	}
	machine := model.Machine{Spec: model.MachineSpec{Runtime: model.RuntimeSpec{Backend: "container"}}}
	if _, err := effectiveStrategy(machine, model.MachineMigration{Spec: model.MachineMigrationSpec{Strategy: "live"}}); err == nil {
		t.Fatal("expected an error for a non-qemu backend")
	}
}

func TestSnapshotFailsOnMissingVolumeFields(t *testing.T) {
	machine := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}, Spec: model.MachineSpec{NodeName: "worker-1", PowerState: "Running", Volumes: []model.MachineVolume{{Name: "data", ClaimName: ""}}}}
	snapshot := model.MachineSnapshot{Metadata: model.ObjectMeta{Name: "before-upgrade", Namespace: "prod"}, Spec: model.MachineSnapshotSpec{MachineName: "db"}}
	phase, message := "", ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(model.NodeList{})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			http.NotFound(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshots":
			_ = json.NewEncoder(w).Encode(model.MachineSnapshotList{Items: []model.MachineSnapshot{snapshot}})
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshots/before-upgrade/status":
			var p struct {
				Status model.MachineSnapshotStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			phase, message = p.Status.Phase, p.Status.Message
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if phase != "Failed" || !strings.Contains(message, "Machine volume requires name and claimName") {
		t.Fatalf("phase=%q message=%q", phase, message)
	}
}

func TestSnapshotFailsOnVolumeSnapshotError(t *testing.T) {
	machine := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}, Spec: model.MachineSpec{NodeName: "worker-1", PowerState: "Running", Volumes: []model.MachineVolume{{Name: "data", ClaimName: "db-data"}}}}
	snapshot := model.MachineSnapshot{Metadata: model.ObjectMeta{Name: "before-upgrade", Namespace: "prod"}, Spec: model.MachineSnapshotSpec{MachineName: "db"}}
	phase, message := "", ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(model.NodeList{})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			http.NotFound(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshots":
			_ = json.NewEncoder(w).Encode(model.MachineSnapshotList{Items: []model.MachineSnapshot{snapshot}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/snapshot.storage.k8s.io/v1/namespaces/prod/volumesnapshots/before-upgrade-data":
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/apis/snapshot.storage.k8s.io/v1/namespaces/prod/volumesnapshots":
			var vs model.VolumeSnapshot
			_ = json.NewDecoder(r.Body).Decode(&vs)
			msg := "backend quota exceeded"
			vs.Status.Error = &model.VolumeSnapshotError{Message: &msg}
			_ = json.NewEncoder(w).Encode(vs)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshots/before-upgrade/status":
			var p struct {
				Status model.MachineSnapshotStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			phase, message = p.Status.Phase, p.Status.Message
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if phase != "Failed" || !strings.Contains(message, "backend quota exceeded") {
		t.Fatalf("phase=%q message=%q", phase, message)
	}
}

func TestSnapshotPendingWhenNotYetReady(t *testing.T) {
	machine := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}, Spec: model.MachineSpec{NodeName: "worker-1", PowerState: "Running", Volumes: []model.MachineVolume{{Name: "data", ClaimName: "db-data"}}}}
	snapshot := model.MachineSnapshot{Metadata: model.ObjectMeta{Name: "before-upgrade", Namespace: "prod"}, Spec: model.MachineSnapshotSpec{MachineName: "db"}}
	phase, message := "", ""
	var readyToUse *bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(model.NodeList{})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			http.NotFound(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinesnapshots":
			_ = json.NewEncoder(w).Encode(model.MachineSnapshotList{Items: []model.MachineSnapshot{snapshot}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/snapshot.storage.k8s.io/v1/namespaces/prod/volumesnapshots/before-upgrade-data":
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/apis/snapshot.storage.k8s.io/v1/namespaces/prod/volumesnapshots":
			var vs model.VolumeSnapshot
			_ = json.NewDecoder(r.Body).Decode(&vs)
			// vs.Status.ReadyToUse left nil -- not yet ready.
			_ = json.NewEncoder(w).Encode(vs)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinesnapshots/before-upgrade/status":
			var p struct {
				Status model.MachineSnapshotStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			phase, message = p.Status.Phase, p.Status.Message
			readyToUse = &p.Status.ReadyToUse
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if phase != "Pending" || !strings.Contains(message, "waiting for CSI VolumeSnapshots") || readyToUse == nil || *readyToUse {
		t.Fatalf("phase=%q message=%q readyToUse=%v", phase, message, readyToUse)
	}
}
