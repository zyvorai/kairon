// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zyvorai/kairon/internal/fluxvm"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/migration"
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
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && (strings.Contains(r.URL.Path, "networksecuritygroups") || strings.Contains(r.URL.Path, "machinenetworkpolicies") || strings.Contains(r.URL.Path, "machinemigrations")):
			w.WriteHeader(http.StatusNotFound)
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

// TestMultipleDRAClaimsMapToMultipleAllowedVFIOBDFs is the SR-IOV
// regression guard: resolveVFIODevices already loops over every entry in
// spec.deviceClaims, not just the first -- proving a Machine can combine
// a GPU passthrough claim with an SR-IOV NIC VF claim (or any two BDFs)
// in one create, with FluxVM's own CreateVmRequest.vfio_devices carrying
// both, distinct devices with no per-device-type code anywhere in this
// path.
func TestMultipleDRAClaimsMapToMultipleAllowedVFIOBDFs(t *testing.T) {
	machine := model.Machine{
		Metadata: model.ObjectMeta{Name: "gpu-and-nic", Namespace: "prod", Finalizers: []string{model.Finalizer}},
		Spec: model.MachineSpec{
			NodeName: "worker-1", Image: model.ImageSpec{Path: "/images/gpu.qcow2"}, Resources: model.ResourceSpec{CPU: "4", Memory: "8Gi"}, Runtime: model.RuntimeSpec{Backend: "qemu"}, PowerState: "Running",
			DeviceClaims: []model.DeviceClaimReference{{Name: "gpu0"}, {Name: "nic-vf0"}},
		},
	}
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/resource.k8s.io/v1/namespaces/prod/resourceclaims/gpu0":
			_ = json.NewEncoder(w).Encode(model.ResourceClaim{Metadata: model.ObjectMeta{Name: "gpu0", Namespace: "prod", Annotations: map[string]string{model.AnnotationVFIOBDF: "65:00.0"}}, Status: model.ResourceClaimStatus{Allocation: &model.ResourceClaimAllocation{}}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/resource.k8s.io/v1/namespaces/prod/resourceclaims/nic-vf0":
			// A real SR-IOV VF, already bound to vfio-pci on the host out of
			// band (see docs/guides/machine-sriov.md) -- resolved here via the
			// exact same manual kairon.zyvor.dev/vfio-bdf annotation fallback
			// a DRA-driver-less ResourceClaim for a GPU already uses above.
			_ = json.NewEncoder(w).Encode(model.ResourceClaim{Metadata: model.ObjectMeta{Name: "nic-vf0", Namespace: "prod", Annotations: map[string]string{model.AnnotationVFIOBDF: "81:10.1"}}, Status: model.ResourceClaimStatus{Allocation: &model.ResourceClaimAllocation{}}})
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/gpu-and-nic/status":
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
			_ = json.NewEncoder(w).Encode(fluxvm.Record{UUID: "gpu-and-nic-vm", Name: machine.RuntimeName(), Status: "Running"})
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer fs.Close()
	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	allow, err := ParseVFIOAllowlist("0000:65:00.0,0000:81:10.1")
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{NodeName: "worker-1", Kube: kc, Flux: fc, DefaultBackend: "qemu", VFIOAllowlist: allow, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := a.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(got.VFIODevices) != 2 {
		t.Fatalf("expected both BDFs to reach FluxVM's own vfio_devices, got %v", got.VFIODevices)
	}
	want := map[string]bool{"0000:65:00.0": true, "0000:81:10.1": true}
	for _, bdf := range got.VFIODevices {
		if !want[bdf] {
			t.Fatalf("unexpected BDF %q in vfio_devices=%v", bdf, got.VFIODevices)
		}
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

type fakeSourceMigrator struct {
	startStatus migration.TransferStatus
	startErr    error
	starts      int
	last        migration.SourceRequest
}

func (f *fakeSourceMigrator) Start(_ context.Context, req migration.SourceRequest) (migration.TransferStatus, error) {
	f.starts++
	f.last = req
	return f.startStatus, f.startErr
}
func (f *fakeSourceMigrator) Status(context.Context, migration.Session, string) (migration.TransferStatus, error) {
	return f.startStatus, nil
}
func (f *fakeSourceMigrator) Abort(context.Context, migration.Session, string) error { return nil }

type fakePeerDestination struct {
	result      migration.PrepareResult
	commitErr   error
	prepares    int
	commits     int
	aborts      int
	lastSession migration.Session

	// diagnosis, when non-nil, makes this fake implement migration.Diagnosable.
	diagnosis    *migration.DiagnosisResult
	diagnosisErr error
}

func (f *fakePeerDestination) Prepare(_ context.Context, session migration.Session) (migration.PrepareResult, error) {
	f.prepares++
	f.lastSession = session
	return f.result, nil
}
func (f *fakePeerDestination) Commit(context.Context, migration.Session) error {
	f.commits++
	return f.commitErr
}
func (f *fakePeerDestination) Abort(context.Context, migration.Session) error {
	f.aborts++
	return nil
}
func (f *fakePeerDestination) Diagnose(_ context.Context, session migration.Session) (migration.DiagnosisResult, error) {
	if f.diagnosis == nil {
		return migration.DiagnosisResult{SessionPhase: session.Phase}, f.diagnosisErr
	}
	return *f.diagnosis, f.diagnosisErr
}

func TestSourceAgentPreparesTargetThenCompletesTransfer(t *testing.T) {
	destination := &fakePeerDestination{result: migration.PrepareResult{TransferSupported: true, Endpoint: "opaque://incoming/session", Backend: "test-adapter"}}
	peerServer := httptest.NewServer((&migration.Server{NodeName: "worker-2", Store: migration.NewFileStore(t.TempDir()), Driver: destination}).Handler())
	defer peerServer.Close()
	source := &fakeSourceMigrator{startStatus: migration.TransferStatus{TransferID: "xfer-1", Phase: "completed", RAMTotal: 4096, RAMTransferred: 4096}}
	status := runLiveAgentReconcile(t, peerServer, destination, source)
	if destination.prepares != 1 || destination.commits != 1 || destination.aborts != 0 {
		t.Fatalf("destination prepares=%d commits=%d aborts=%d", destination.prepares, destination.commits, destination.aborts)
	}
	if source.starts != 1 || source.last.Endpoint != "opaque://incoming/session" || source.last.Options.BandwidthMbps != 800 {
		t.Fatalf("source starts=%d request=%+v", source.starts, source.last)
	}
	if status.Phase != "Cutover" || status.RuntimeID != "vm-1" || status.SessionID == "" || status.TransferID != "xfer-1" || status.TransferPhase != "completed" {
		t.Fatalf("status=%+v", status)
	}
}

func TestSourceAgentPopulatesMigrationNetworkFromSpec(t *testing.T) {
	destination := &fakePeerDestination{result: migration.PrepareResult{TransferSupported: true, Endpoint: "opaque://incoming/session", Backend: "test-adapter"}}
	peerServer := httptest.NewServer((&migration.Server{NodeName: "worker-2", Store: migration.NewFileStore(t.TempDir()), Driver: destination}).Handler())
	defer peerServer.Close()
	source := &fakeSourceMigrator{startStatus: migration.TransferStatus{TransferID: "xfer-1", Phase: "completed"}}
	runLiveAgentReconcileWithNetwork(t, peerServer, source, "migration-fast")
	if destination.lastSession.MigrationNetwork != "migration-fast" {
		t.Fatalf("expected MigrationNetwork %q to flow from spec, got %q", "migration-fast", destination.lastSession.MigrationNetwork)
	}
}

func TestUnsupportedTargetBlocksBeforeSourceTransfer(t *testing.T) {
	destination := &fakePeerDestination{result: migration.PrepareResult{TransferSupported: false, Reason: "adapter unavailable"}}
	peerServer := httptest.NewServer((&migration.Server{NodeName: "worker-2", Store: migration.NewFileStore(t.TempDir()), Driver: destination}).Handler())
	defer peerServer.Close()
	source := &fakeSourceMigrator{startStatus: migration.TransferStatus{TransferID: "must-not-run", Phase: "completed"}}
	status := runLiveAgentReconcile(t, peerServer, destination, source)
	if source.starts != 0 {
		t.Fatalf("source transfer started %d times", source.starts)
	}
	if status.Phase != "Blocked" || !strings.Contains(status.Message, "source runtime was left untouched") {
		t.Fatalf("status=%+v", status)
	}
}

func TestSourceStartFailureAbortsPreparedTarget(t *testing.T) {
	destination := &fakePeerDestination{result: migration.PrepareResult{TransferSupported: true, Endpoint: "opaque://incoming/session"}}
	peerServer := httptest.NewServer((&migration.Server{NodeName: "worker-2", Store: migration.NewFileStore(t.TempDir()), Driver: destination}).Handler())
	defer peerServer.Close()
	source := &fakeSourceMigrator{startErr: fmt.Errorf("source adapter start failed")}
	status := runLiveAgentReconcile(t, peerServer, destination, source)
	if destination.aborts != 1 {
		t.Fatalf("target aborts=%d, want 1", destination.aborts)
	}
	if status.Phase != "Failed" || !strings.Contains(status.Message, "source adapter start failed") {
		t.Fatalf("status=%+v", status)
	}
}

func TestCommitFailureStopsAtNeedsRecovery(t *testing.T) {
	destination := &fakePeerDestination{result: migration.PrepareResult{TransferSupported: true, Endpoint: "opaque://incoming/session"}, commitErr: fmt.Errorf("commit failed")}
	peerServer := httptest.NewServer((&migration.Server{NodeName: "worker-2", Store: migration.NewFileStore(t.TempDir()), Driver: destination}).Handler())
	defer peerServer.Close()
	source := &fakeSourceMigrator{startStatus: migration.TransferStatus{TransferID: "xfer-1", Phase: "completed"}}
	status := runLiveAgentReconcile(t, peerServer, destination, source)
	if status.Phase != "NeedsRecovery" || !strings.Contains(status.Message, "avoid split brain") {
		t.Fatalf("status=%+v", status)
	}
}

func runLiveAgentReconcile(t *testing.T, peerServer *httptest.Server, _ *fakePeerDestination, source *fakeSourceMigrator) model.MachineMigrationStatus {
	t.Helper()
	return runLiveAgentReconcileWithNetwork(t, peerServer, source, "")
}

func runLiveAgentReconcileWithNetwork(t *testing.T, peerServer *httptest.Server, source *fakeSourceMigrator, migrationNetwork string) model.MachineMigrationStatus {
	t.Helper()
	machine := model.Machine{
		Metadata: model.ObjectMeta{Name: "db", Namespace: "prod", Finalizers: []string{model.Finalizer}},
		Spec:     model.MachineSpec{NodeName: "worker-1", Image: model.ImageSpec{Path: "/images/db.qcow2"}, Resources: model.ResourceSpec{CPU: "2", Memory: "2Gi"}, Runtime: model.RuntimeSpec{Backend: "qemu"}, PowerState: "Running"},
		Status:   model.MachineStatus{RuntimeID: "vm-1", Phase: "Running", NodeName: "worker-1"},
	}
	item := model.MachineMigration{
		Metadata: model.ObjectMeta{Name: "move-db", Namespace: "prod", UID: "migration-uid-1"},
		Spec:     model.MachineMigrationSpec{MachineName: "db", Strategy: "live", Mode: "pre-copy", BandwidthMbps: 800, MigrationNetwork: migrationNetwork},
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
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: []model.MachineMigration{item}})
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
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-1" {
			_ = json.NewEncoder(w).Encode(fluxvm.Record{UUID: "vm-1", Name: machine.RuntimeName(), Status: "Running"})
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer fs.Close()
	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{
		NodeName: "worker-1", Kube: kc, Flux: fc, DefaultBackend: "qemu",
		MigrationPeer: migration.NewClient(peerServer.Client()), SourceMigrator: source,
		MigrationPeerURL: func(context.Context, string) (string, error) { return peerServer.URL, nil },
		Log:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if err := a.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	return migrationStatus
}

// runNeedsRecoveryReconcile drives a single reconcile of a migration already
// parked in NeedsRecovery, with sourceNode/targetNode/sessionID/runtimeID
// pre-populated (as a real Starting-phase reconcile would have left them).
// storedSessionPhase seeds the peer server's own session store, mirroring
// what the destination's Commit sequence would have actually left behind.
func runNeedsRecoveryReconcile(t *testing.T, recovery *model.MachineMigrationRecoverySpec, destination *fakePeerDestination, storedSessionPhase string, fluxHandler http.HandlerFunc) model.MachineMigrationStatus {
	t.Helper()
	machine := model.Machine{
		Metadata: model.ObjectMeta{Name: "db", Namespace: "prod", Finalizers: []string{model.Finalizer}},
		Spec:     model.MachineSpec{NodeName: "worker-1", Image: model.ImageSpec{Path: "/images/db.qcow2"}, Resources: model.ResourceSpec{CPU: "2", Memory: "2Gi"}, Runtime: model.RuntimeSpec{Backend: "qemu"}, PowerState: "Running"},
		Status:   model.MachineStatus{RuntimeID: "vm-1", Phase: "Running", NodeName: "worker-1"},
	}
	item := model.MachineMigration{
		Metadata: model.ObjectMeta{Name: "move-db", Namespace: "prod", UID: "migration-uid-1"},
		Spec:     model.MachineMigrationSpec{MachineName: "db", Strategy: "live", Recovery: recovery},
		Status: model.MachineMigrationStatus{
			Phase: "NeedsRecovery", SourceNode: "worker-1", TargetNode: "worker-2", EffectiveStrategy: "live",
			RuntimeID: "vm-1", SessionID: "sess-1",
		},
	}
	store := migration.NewFileStore(t.TempDir())
	if err := store.Put(migration.Session{ID: "sess-1", Namespace: "prod", Machine: "db", SourceNode: "worker-1", TargetNode: "worker-2", Phase: storedSessionPhase}); err != nil {
		t.Fatal(err)
	}
	peerServer := httptest.NewServer((&migration.Server{NodeName: "worker-2", Store: store, Driver: destination}).Handler())
	defer peerServer.Close()

	var migrationStatus model.MachineMigrationStatus
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/db/status":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: []model.MachineMigration{item}})
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
	fs := httptest.NewServer(fluxHandler)
	defer fs.Close()
	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	a := &Agent{
		NodeName: "worker-1", Kube: kc, Flux: fc, DefaultBackend: "qemu",
		MigrationPeer:    migration.NewClient(peerServer.Client()),
		MigrationPeerURL: func(context.Context, string) (string, error) { return peerServer.URL, nil },
		Log:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if err := a.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	return migrationStatus
}

func defaultNeedsRecoveryFluxHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-1" {
			_ = json.NewEncoder(w).Encode(fluxvm.Record{UUID: "vm-1", Name: "kairon-prod-db", Status: "Running"})
			return
		}
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/vms/vm-1" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

func TestNeedsRecoveryDiagnosisPopulatesStatus(t *testing.T) {
	destination := &fakePeerDestination{diagnosis: &migration.DiagnosisResult{SessionPhase: "Prepared", DestinationRuntimeFound: true, DestinationRuntimeStatus: "Running"}}
	status := runNeedsRecoveryReconcile(t, nil, destination, "Prepared", defaultNeedsRecoveryFluxHandler(t))
	if status.Phase != "NeedsRecovery" {
		t.Fatalf("expected phase to stay NeedsRecovery with no spec.recovery set, got %+v", status)
	}
	if status.Recovery == nil {
		t.Fatal("expected status.Recovery to be populated")
	}
	if status.Recovery.SourceRuntimeStatus != "Running" {
		t.Errorf("expected sourceRuntimeStatus=Running, got %q", status.Recovery.SourceRuntimeStatus)
	}
	if status.Recovery.DestinationSessionPhase != "Prepared" || !status.Recovery.DestinationRuntimeFound || status.Recovery.DestinationRuntimeStatus != "Running" {
		t.Errorf("unexpected recovery diagnosis: %+v", status.Recovery)
	}
	if status.Recovery.DiagnosedAt == nil {
		t.Error("expected DiagnosedAt to be set")
	}
}

func TestNeedsRecoveryRejectsMismatchedDiagnosis(t *testing.T) {
	destination := &fakePeerDestination{diagnosis: &migration.DiagnosisResult{DestinationRuntimeFound: false}}
	recovery := &model.MachineMigrationRecoverySpec{
		Action:                model.RecoveryActionConfirmDestinationCommitted,
		AcknowledgedDiagnosis: model.RecoveryDiagnosisDestinationNotCommitted,
		Reason:                "I saw the destination was not running",
	}
	status := runNeedsRecoveryReconcile(t, recovery, destination, "Prepared", defaultNeedsRecoveryFluxHandler(t))
	if status.Phase != "NeedsRecovery" {
		t.Fatalf("expected phase to stay NeedsRecovery on a mismatched diagnosis, got %+v", status)
	}
	if !strings.Contains(status.Message, "does not match") {
		t.Errorf("expected a mismatch rejection message, got %q", status.Message)
	}
	if destination.commits != 0 || destination.aborts != 0 {
		t.Errorf("expected no commit/abort to be attempted, got commits=%d aborts=%d", destination.commits, destination.aborts)
	}
}

func TestNeedsRecoveryConfirmCommittedTransitionsToCutover(t *testing.T) {
	destination := &fakePeerDestination{diagnosis: &migration.DiagnosisResult{DestinationRuntimeFound: true, DestinationRuntimeStatus: "Running"}}
	recovery := &model.MachineMigrationRecoverySpec{
		Action:                model.RecoveryActionConfirmDestinationCommitted,
		AcknowledgedDiagnosis: model.RecoveryDiagnosisDestinationCommitted,
		Reason:                "Confirmed the guest is running on the destination via console",
	}
	status := runNeedsRecoveryReconcile(t, recovery, destination, "Prepared", defaultNeedsRecoveryFluxHandler(t))
	if status.Phase != "Cutover" {
		t.Fatalf("expected phase Cutover, got %+v", status)
	}
	if destination.commits != 1 {
		t.Errorf("expected exactly one commit retry, got %d", destination.commits)
	}
	if status.Recovery == nil || status.Recovery.AppliedAction != model.RecoveryActionConfirmDestinationCommitted || status.Recovery.AppliedAt == nil {
		t.Errorf("expected an audit record of the applied action, got %+v", status.Recovery)
	}
}

func TestNeedsRecoveryConfirmNotCommittedRetriesThenFails(t *testing.T) {
	destination := &fakePeerDestination{commitErr: fmt.Errorf("destination rejects commit: unknown session")}
	recovery := &model.MachineMigrationRecoverySpec{
		Action:                model.RecoveryActionConfirmDestinationNotCommitted,
		AcknowledgedDiagnosis: model.RecoveryDiagnosisDestinationNotCommitted,
		Reason:                "Destination FluxVM has no record of this VM",
	}
	status := runNeedsRecoveryReconcile(t, recovery, destination, "Prepared", defaultNeedsRecoveryFluxHandler(t))
	if status.Phase != "Failed" {
		t.Fatalf("expected phase Failed after the retried commit also fails, got %+v", status)
	}
	if destination.commits != 1 {
		t.Errorf("expected exactly one retried commit attempt, got %d", destination.commits)
	}
	if destination.aborts != 1 {
		t.Errorf("expected an abort after the retried commit failed, got %d", destination.aborts)
	}
}

func TestNeedsRecoveryForceAbortLeavesSourceRuntimeAlone(t *testing.T) {
	destination := &fakePeerDestination{}
	fluxCalls := map[string]int{}
	fluxHandler := func(w http.ResponseWriter, r *http.Request) {
		fluxCalls[r.Method+" "+r.URL.Path]++
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-1":
			_ = json.NewEncoder(w).Encode(fluxvm.Record{UUID: "vm-1", Name: "kairon-prod-db", Status: "Running"})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/vms/vm-1":
			t.Error("ForceAbort must never delete the source runtime")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}
	recovery := &model.MachineMigrationRecoverySpec{
		Action:                model.RecoveryActionForceAbort,
		AcknowledgedDiagnosis: model.RecoveryDiagnosisUnknown,
		Reason:                "Cannot determine which side is authoritative; forcing abort as an escape hatch",
	}
	status := runNeedsRecoveryReconcile(t, recovery, destination, "Prepared", fluxHandler)
	if status.Phase != "Failed" {
		t.Fatalf("expected phase Failed, got %+v", status)
	}
	if destination.aborts != 1 {
		t.Errorf("expected exactly one abort, got %d", destination.aborts)
	}
	if fluxCalls["DELETE /v1/vms/vm-1"] != 0 {
		t.Errorf("expected the source runtime to never be deleted, got %d DELETE calls", fluxCalls["DELETE /v1/vms/vm-1"])
	}
}
