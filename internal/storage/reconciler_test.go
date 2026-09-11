package storage

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
)

func TestReconcileHostPathDiskAndImage(t *testing.T) {
	var imageStatus model.MachineImageStatus
	var diskStatus model.VirtualDiskStatus
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machineimages":
			_ = json.NewEncoder(w).Encode(model.MachineImageList{Items: []model.MachineImage{{
				Metadata: model.ObjectMeta{Name: "ubuntu", Namespace: "default", Generation: 1},
				Spec:     model.MachineImageSpec{Source: model.MachineImageSource{Path: "/var/lib/fluxvm/images/u.qcow2"}},
			}}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/virtualdisks":
			_ = json.NewEncoder(w).Encode(model.VirtualDiskList{Items: []model.VirtualDisk{{
				Metadata: model.ObjectMeta{Name: "root", Namespace: "default", Generation: 2},
				Spec:     model.VirtualDiskSpec{Source: model.VirtualDiskSource{HostPath: "/var/lib/fluxvm/disks/root.qcow2"}},
			}}})
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machineimages/ubuntu/status":
			var p struct {
				Status model.MachineImageStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			imageStatus = p.Status
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/virtualdisks/root/status":
			var p struct {
				Status model.VirtualDiskStatus `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&p)
			diskStatus = p.Status
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	r := &Reconciler{Kube: kc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if imageStatus.Phase != "Ready" || imageStatus.ResolvedPath == "" {
		t.Fatalf("image status=%+v", imageStatus)
	}
	if diskStatus.Phase != "Bound" || diskStatus.Path == "" {
		t.Fatalf("disk status=%+v", diskStatus)
	}
}

func TestResolvePVCHostPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/namespaces/default/persistentvolumeclaims/disk":
			_ = json.NewEncoder(w).Encode(model.PersistentVolumeClaim{
				Metadata: model.ObjectMeta{Name: "disk"},
				Spec: struct {
					VolumeName string `json:"volumeName,omitempty"`
				}{VolumeName: "pv-1"},
			})
		case r.URL.Path == "/api/v1/persistentvolumes/pv-1":
			pv := model.PersistentVolume{Metadata: model.ObjectMeta{Name: "pv-1"}}
			pv.Spec.HostPath = &struct {
				Path string `json:"path"`
			}{Path: "/mnt/disks/pv-1"}
			_ = json.NewEncoder(w).Encode(pv)
		default:
			http.Error(w, "no", 404)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	d := model.VirtualDisk{
		Metadata: model.ObjectMeta{Name: "d", Namespace: "default"},
		Spec: model.VirtualDiskSpec{Source: model.VirtualDiskSource{
			PersistentVolumeClaim: &model.PVCRef{ClaimName: "disk"},
		}},
	}
	path, vol, msg, phase := resolveDisk(context.Background(), kc, d)
	if phase != "Bound" || path != "/mnt/disks/pv-1" || vol != "pv-1" || msg != "" {
		t.Fatalf("path=%q vol=%q msg=%q phase=%q", path, vol, msg, phase)
	}
}
