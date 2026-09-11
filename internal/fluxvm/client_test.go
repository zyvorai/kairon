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
	m := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}, Spec: model.MachineSpec{
		Image:     model.ImageSpec{Path: "/images/db.qcow2"},
		Resources: model.ResourceSpec{CPU: "1500m", Memory: "2Gi"},
		Runtime:   model.RuntimeSpec{Backend: "qemu"},
		Network:   model.NetworkSpec{Mode: "tap", NetNS: true},
		CloudInit: model.CloudInitSpec{UserData: "#cloud-config\n", SSHPublicKeys: []string{"ssh-ed25519 AAAA"}},
		Security:  model.SecuritySpec{SecureBoot: true},
	}}
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
	if got.CloudInit == nil || len(got.CloudInit.SSHAuthorizedKeys) != 1 || !got.SecureBoot {
		t.Fatalf("cloud-init/security not forwarded: %+v", got)
	}
	if got.Storage != "default" {
		t.Fatalf("storage=%q", got.Storage)
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
