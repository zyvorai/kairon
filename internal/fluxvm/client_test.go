package fluxvm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zyvorai/kairon/internal/model"
)

func TestCreateMapping(t *testing.T) {
	var got CreateRequest
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/vms" {
			http.Error(w, "bad route", 404)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(Record{UUID: "u1", Name: got.Name, Status: "Running"})
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	m := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}, Spec: model.MachineSpec{Image: model.ImageSpec{Path: "/images/db.qcow2"}, Resources: model.ResourceSpec{CPU: "1500m", Memory: "2Gi"}, Runtime: model.RuntimeSpec{Backend: "qemu"}, Network: model.NetworkSpec{Mode: "tap", NetNS: true}}}
	r, err := c.Create(context.Background(), m, "qemu")
	if err != nil {
		t.Fatal(err)
	}
	if r.ID() != "u1" || got.Name != "kairon-prod-db" || got.Tenant != "prod" || got.VCPUs != 2 || got.MemoryMiB != 2048 {
		t.Fatalf("record=%+v request=%+v", r, got)
	}
	if got.Network["mode"] != "tap" || got.Network["netns"] != true {
		t.Fatalf("network=%v", got.Network)
	}
}

func TestLookupArray(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]Record{{IDValue: "id1", Name: "vm"}})
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	r, err := c.LookupByName(context.Background(), "vm")
	if err != nil || r == nil || r.ID() != "id1" {
		t.Fatalf("r=%+v err=%v", r, err)
	}
}

func TestDeleteNotFoundIsIdempotent(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "bad", http.StatusBadRequest)
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	if err := c.Delete(context.Background(), "missing"); err != nil {
		t.Fatalf("delete should be idempotent: %v", err)
	}
}

func TestMigrationContract(t *testing.T) {
	var got MigrationStartRequest
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/vms/vm-1/migration/start":
			_ = json.NewDecoder(r.Body).Decode(&got)
			_ = json.NewEncoder(w).Encode(MigrationStatus{Phase: "active", Status: "active"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-1/migration/status":
			remaining := uint64(4096)
			_ = json.NewEncoder(w).Encode(MigrationStatus{Phase: "active", RAMRemaining: &remaining})
		default:
			http.Error(w, "bad route", http.StatusNotFound)
		}
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	st, err := c.StartMigration(context.Background(), "vm-1", MigrationStartRequest{Destination: "tcp:10.0.0.2:4444", Mode: "pre-copy", BandwidthMbps: 512, MaxDowntimeMs: 150, MultifdChannels: 4})
	if err != nil || st.Phase != "active" {
		t.Fatalf("status=%+v err=%v", st, err)
	}
	if got.Destination != "tcp:10.0.0.2:4444" || got.BandwidthMbps != 512 || got.MaxDowntimeMs != 150 || got.MultifdChannels != 4 {
		t.Fatalf("unexpected migration request: %+v", got)
	}
	st, err = c.MigrationStatus(context.Background(), "vm-1")
	if err != nil || st.RAMRemaining == nil || *st.RAMRemaining != 4096 {
		t.Fatalf("status=%+v err=%v", st, err)
	}
}

func TestCreateWithVFIO(t *testing.T) {
	var got CreateRequest
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(Record{UUID: "u1", Name: got.Name, Status: "Running"})
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	m := model.Machine{Metadata: model.ObjectMeta{Name: "gpu", Namespace: "prod"}, Spec: model.MachineSpec{Image: model.ImageSpec{Path: "/images/gpu.qcow2"}, Resources: model.ResourceSpec{CPU: "2", Memory: "4Gi"}, Runtime: model.RuntimeSpec{Backend: "qemu"}}}
	if _, err := c.CreateWithVFIO(context.Background(), m, "qemu", []string{"0000:65:00.0"}); err != nil {
		t.Fatal(err)
	}
	if len(got.VFIODevices) != 1 || got.VFIODevices[0] != "0000:65:00.0" {
		t.Fatalf("vfio_devices=%v", got.VFIODevices)
	}
}
