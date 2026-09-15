// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package fluxvm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zyvorai/kairon/internal/model"
)

func sandboxMachine(name string) model.Machine {
	return model.Machine{
		Metadata: model.ObjectMeta{Name: name, Namespace: "prod"},
		Spec: model.MachineSpec{
			Image:     model.ImageSpec{Path: "/images/sandbox.qcow2"},
			Resources: model.ResourceSpec{CPU: "1", Memory: "512Mi"},
			Sandbox:   &model.SandboxSpec{},
		},
	}
}

func TestCreateSandboxForMachineWithSpec(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(Record{UUID: "sandbox-1", Status: "Running"})
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()

	rec, err := c.CreateSandboxForMachine(context.Background(), sandboxMachine("agent-run"))
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/sandboxes" {
		t.Fatalf("unexpected path: %q", gotPath)
	}
	if rec.Status != "Running" {
		t.Fatalf("unexpected result: %+v", rec)
	}
	spec, ok := gotBody["spec"].(map[string]any)
	if !ok {
		t.Fatalf("expected an embedded spec, got %+v", gotBody)
	}
	if spec["image"] != "/images/sandbox.qcow2" || spec["backend"] != "flux-vm" {
		t.Fatalf("unexpected embedded spec: %+v", spec)
	}
	if _, hasTemplate := gotBody["template"]; hasTemplate {
		t.Fatalf("did not expect a template field when Spec is used, got %+v", gotBody)
	}
}

func TestCreateSandboxForMachineWithTemplate(t *testing.T) {
	var gotBody map[string]any
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(Record{UUID: "sandbox-1", Status: "Running"})
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()

	m := sandboxMachine("agent-run")
	m.Spec.Sandbox = &model.SandboxSpec{TemplateName: "alpine-agent"}
	if _, err := c.CreateSandboxForMachine(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if gotBody["template"] != "alpine-agent" {
		t.Fatalf("expected template to be forwarded, got %+v", gotBody)
	}
	if _, hasSpec := gotBody["spec"]; hasSpec {
		t.Fatalf("did not expect an embedded spec when a template is used, got %+v", gotBody)
	}
}

func TestCreateSandboxForMachineRefusesDeviceClaims(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("FluxVM should not be dialed when device claims are present")
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()

	m := sandboxMachine("agent-run")
	m.Spec.DeviceClaims = []model.DeviceClaimReference{{Name: "gpu-0"}}
	if _, err := c.CreateSandboxForMachine(context.Background(), m); err == nil {
		t.Fatal("expected an error for a sandbox Machine with deviceClaims")
	}
}

func TestListSandboxes(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sandboxes" {
			http.Error(w, "bad route", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []Record{{UUID: "sandbox-1", Status: "Running"}}})
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()

	items, err := c.ListSandboxes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].UUID != "sandbox-1" {
		t.Fatalf("unexpected result: %+v", items)
	}
}

func TestSnapshotSandbox(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()

	if err := c.SnapshotSandbox(context.Background(), "sandbox-1", "/tmp/snap.img"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/sandboxes/sandbox-1/snapshot" || gotBody["path"] != "/tmp/snap.img" {
		t.Fatalf("unexpected request: path=%q body=%+v", gotPath, gotBody)
	}
}

func TestListTemplates(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/templates" {
			http.Error(w, "bad route", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []TemplateInfo{{Name: "alpine-agent", Path: "/var/lib/fluxvm/templates/alpine-agent", Snapshot: true}}})
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()

	items, err := c.ListTemplates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "alpine-agent" || !items[0].Snapshot {
		t.Fatalf("unexpected result: %+v", items)
	}
}

func TestBuildTemplate(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(TemplateInfo{Name: "alpine-agent", Path: "/var/lib/fluxvm/templates/alpine-agent", Snapshot: false})
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()

	out, err := c.BuildTemplate(context.Background(), "alpine-agent", "docker.io/library/alpine:3.19")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/templates" || gotBody["name"] != "alpine-agent" || gotBody["image_ref"] != "docker.io/library/alpine:3.19" {
		t.Fatalf("unexpected request: path=%q body=%+v", gotPath, gotBody)
	}
	if out.Name != "alpine-agent" {
		t.Fatalf("unexpected result: %+v", out)
	}
}

func TestBuildTemplatePropagatesErrors(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "skopeo copy failed: image not found", http.StatusBadGateway)
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()

	if _, err := c.BuildTemplate(context.Background(), "bad", "docker.io/nope:latest"); err == nil {
		t.Fatal("expected an error when the server rejects the request")
	}
}
