package controller

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
