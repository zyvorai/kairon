package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
