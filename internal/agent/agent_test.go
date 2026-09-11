package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zyvorai/kairon/internal/fluxvm"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

func TestReconcileCreatesFluxVMAndUpdatesStatus(t *testing.T) {
	machine := model.Machine{
		Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"},
		Spec: model.MachineSpec{
			NodeName:   "worker-1",
			Image:      model.ImageSpec{Path: "/images/db.qcow2"},
			Resources:  model.ResourceSpec{CPU: "2", Memory: "2Gi"},
			Runtime:    model.RuntimeSpec{Backend: "qemu"},
			Network:    model.NetworkSpec{Mode: "tap", NetNS: true},
			PowerState: "Running",
		},
	}
	var finalizerPatched, statusPatched bool
	var status model.MachineStatus
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/db":
			finalizerPatched = true
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/db/status":
			var p struct {
				Status model.MachineStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			status = p.Status
			statusPatched = true
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer ks.Close()

	var created bool
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vms":
			_ = json.NewEncoder(w).Encode([]fluxvm.Record{})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/vms":
			created = true
			_ = json.NewEncoder(w).Encode(fluxvm.Record{UUID: "vm-123", Name: "kairon-prod-db", Status: "Running", GuestIP: "10.44.0.8"})
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer fs.Close()

	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{NodeName: "worker-1", Kube: kc, Flux: fc, DefaultBackend: "qemu", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := a.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !finalizerPatched || !created || !statusPatched {
		t.Fatalf("finalizer=%v created=%v status=%v", finalizerPatched, created, statusPatched)
	}
	if status.RuntimeID != "vm-123" || status.Phase != "Running" || status.GuestIP != "10.44.0.8" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestImageRootRejectsTraversal(t *testing.T) {
	machine := model.Machine{
		Metadata: model.ObjectMeta{Name: "bad", Namespace: "prod", Finalizers: []string{model.Finalizer}},
		Spec:     model.MachineSpec{NodeName: "worker-1", Image: model.ImageSpec{Path: "/etc/shadow"}, Resources: model.ResourceSpec{CPU: "1", Memory: "1Gi"}, PowerState: "Running"},
	}
	statusWasError := false
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/bad/status":
			var p struct {
				Status model.MachineStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			statusWasError = p.Status.Phase == "Error"
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer ks.Close()
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("FluxVM should not be called for rejected image path")
	}))
	defer fs.Close()
	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{NodeName: "worker-1", Kube: kc, Flux: fc, ImageRoot: "/var/lib/fluxvm/images", DefaultBackend: "qemu", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := a.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !statusWasError {
		t.Fatal("expected Error status for image outside allowed root")
	}
}

func TestAdoptOnlyNeverCreatesDuplicateRuntime(t *testing.T) {
	machine := model.Machine{
		Metadata: model.ObjectMeta{Name: "db", Namespace: "prod", Finalizers: []string{model.Finalizer}, Annotations: map[string]string{model.AnnotationAdoptOnly: "true"}},
		Spec:     model.MachineSpec{NodeName: "worker-2", Image: model.ImageSpec{Path: "/images/db.qcow2"}, Resources: model.ResourceSpec{CPU: "2", Memory: "2Gi"}, Runtime: model.RuntimeSpec{Backend: "qemu"}, PowerState: "Running"},
	}
	blocked := false
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/db/status":
			var p struct {
				Status model.MachineStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			blocked = p.Status.Phase == "Blocked" && strings.Contains(p.Status.Message, "refusing to create a duplicate")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			http.NotFound(w, r)
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer ks.Close()
	createCalled := false
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/vms" {
			createCalled = true
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/vms" {
			_ = json.NewEncoder(w).Encode([]fluxvm.Record{})
			return
		}
		http.Error(w, "unexpected", http.StatusNotFound)
	}))
	defer fs.Close()
	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{NodeName: "worker-2", Kube: kc, Flux: fc, DefaultBackend: "qemu", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := a.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if createCalled {
		t.Fatal("adopt-only reconciliation must never create a new FluxVM runtime")
	}
	if !blocked {
		t.Fatal("expected Machine to be visibly Blocked when incoming runtime is absent")
	}
}

func TestDRAClaimMapsToAllowedVFIOBDF(t *testing.T) {
	machine := model.Machine{
		Metadata: model.ObjectMeta{Name: "gpu", Namespace: "prod", Finalizers: []string{model.Finalizer}},
		Spec:     model.MachineSpec{NodeName: "worker-1", Image: model.ImageSpec{Path: "/images/gpu.qcow2"}, Resources: model.ResourceSpec{CPU: "4", Memory: "8Gi"}, Runtime: model.RuntimeSpec{Backend: "qemu"}, PowerState: "Running", DeviceClaims: []model.DeviceClaimReference{{Name: "gpu0"}}},
	}
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/resource.k8s.io/v1/namespaces/prod/resourceclaims/gpu0":
			_ = json.NewEncoder(w).Encode(model.ResourceClaim{Metadata: model.ObjectMeta{Name: "gpu0", Namespace: "prod", Annotations: map[string]string{model.AnnotationVFIOBDF: "65:00.0"}}, Status: model.ResourceClaimStatus{Allocation: &model.ResourceClaimAllocation{}}})
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/gpu/status":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			http.NotFound(w, r)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer ks.Close()
	var got fluxvm.CreateRequest
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vms":
			_ = json.NewEncoder(w).Encode([]fluxvm.Record{})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/vms":
			_ = json.NewDecoder(r.Body).Decode(&got)
			_ = json.NewEncoder(w).Encode(fluxvm.Record{UUID: "gpu-vm", Name: machine.RuntimeName(), Status: "Running"})
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer fs.Close()
	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	allow, err := ParseVFIOAllowlist("0000:65:00.0")
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{NodeName: "worker-1", Kube: kc, Flux: fc, DefaultBackend: "qemu", VFIOAllowlist: allow, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := a.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(got.VFIODevices) != 1 || got.VFIODevices[0] != "0000:65:00.0" {
		t.Fatalf("vfio_devices=%v", got.VFIODevices)
	}
}

func TestDRAClaimFailsClosedWithoutAllowlist(t *testing.T) {
	machine := model.Machine{Metadata: model.ObjectMeta{Name: "gpu", Namespace: "prod", Finalizers: []string{model.Finalizer}}, Spec: model.MachineSpec{NodeName: "worker-1", Image: model.ImageSpec{Path: "/images/gpu.qcow2"}, Resources: model.ResourceSpec{CPU: "2", Memory: "4Gi"}, DeviceClaims: []model.DeviceClaimReference{{Name: "gpu0"}}}}
	statusError := false
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/machines/gpu/status"):
			var p struct {
				Status model.MachineStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			statusError = p.Status.Phase == "Error" && strings.Contains(p.Status.Message, "VFIO_ALLOWLIST")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			http.NotFound(w, r)
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer ks.Close()
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/vms" {
			_ = json.NewEncoder(w).Encode([]fluxvm.Record{})
			return
		}
		if r.Method == http.MethodPost {
			t.Fatal("runtime create must not be attempted without a VFIO allowlist")
		}
		http.Error(w, "unexpected", http.StatusNotFound)
	}))
	defer fs.Close()
	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{NodeName: "worker-1", Kube: kc, Flux: fc, DefaultBackend: "qemu", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := a.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !statusError {
		t.Fatal("expected fail-closed Error status")
	}
}

func TestValidateMigrationDestination(t *testing.T) {
	for _, good := range []string{"tcp:10.0.0.2:4444", "tcp:[2001:db8::2]:4444", "tcp:migrate.internal:49152"} {
		if err := ValidateMigrationDestination(good); err != nil {
			t.Fatalf("%s: %v", good, err)
		}
	}
	for _, bad := range []string{"exec:/bin/sh", "unix:/tmp/migrate.sock", "tcp:10.0.0.2", "tcp::4444", "tcp:host:70000"} {
		if err := ValidateMigrationDestination(bad); err == nil {
			t.Fatalf("expected %s to be rejected", bad)
		}
	}
}

func TestSourceAgentStartsLiveMigrationAndProjectsCompletion(t *testing.T) {
	machine := model.Machine{
		Metadata: model.ObjectMeta{Name: "db", Namespace: "prod", Finalizers: []string{model.Finalizer}},
		Spec:     model.MachineSpec{NodeName: "worker-1", Image: model.ImageSpec{Path: "/images/db.qcow2"}, Resources: model.ResourceSpec{CPU: "2", Memory: "2Gi"}, Runtime: model.RuntimeSpec{Backend: "qemu"}, PowerState: "Running"},
		Status:   model.MachineStatus{RuntimeID: "vm-1", Phase: "Running", NodeName: "worker-1"},
	}
	migration := model.MachineMigration{
		Metadata: model.ObjectMeta{Name: "move-db", Namespace: "prod"},
		Spec:     model.MachineMigrationSpec{MachineName: "db", Strategy: "live", Destination: "tcp:10.0.0.2:4444", Mode: "pre-copy", BandwidthMbps: 800},
		Status:   model.MachineMigrationStatus{Phase: "Starting", SourceNode: "worker-1", TargetNode: "worker-2", EffectiveStrategy: "live"},
	}
	var migrationStatus model.MachineMigrationStatus
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/db/status":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: []model.MachineMigration{migration}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/db":
			_ = json.NewEncoder(w).Encode(machine)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machinemigrations/move-db/status":
			var p struct {
				Status model.MachineMigrationStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			migrationStatus = p.Status
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer ks.Close()
	var request fluxvm.MigrationStartRequest
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-1":
			_ = json.NewEncoder(w).Encode(fluxvm.Record{UUID: "vm-1", Name: machine.RuntimeName(), Status: "Running"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/vms/vm-1/migration/start":
			_ = json.NewDecoder(r.Body).Decode(&request)
			remaining := uint64(0)
			_ = json.NewEncoder(w).Encode(fluxvm.MigrationStatus{Phase: "completed", Status: "completed", RAMRemaining: &remaining})
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer fs.Close()
	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{NodeName: "worker-1", Kube: kc, Flux: fc, DefaultBackend: "qemu", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := a.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if request.Destination != "tcp:10.0.0.2:4444" || request.BandwidthMbps != 800 {
		t.Fatalf("migration request=%+v", request)
	}
	if migrationStatus.Phase != "Cutover" || migrationStatus.RuntimeID != "vm-1" || migrationStatus.FluxPhase != "completed" {
		t.Fatalf("migration status=%+v", migrationStatus)
	}
}
