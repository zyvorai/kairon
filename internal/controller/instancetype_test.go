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
	"testing"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

func newInstanceTypeTestController(t *testing.T, instanceTypes []model.MachineInstanceType) (*Controller, *[]string) {
	t.Helper()
	var patchedResources []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machineinstancetypes":
			_ = json.NewEncoder(w).Encode(model.MachineInstanceTypeList{Items: instanceTypes})
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/vm-1":
			var patch struct {
				Spec struct {
					Resources model.ResourceSpec `json:"resources"`
				} `json:"spec"`
			}
			_ = json.NewDecoder(r.Body).Decode(&patch)
			patchedResources = append(patchedResources, patch.Spec.Resources.CPU+"/"+patch.Spec.Resources.Memory)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	kc.HTTP = srv.Client()
	return &Controller{Kube: kc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, &patchedResources
}

func TestResolveInstanceTypesFillsInResources(t *testing.T) {
	it := model.MachineInstanceType{
		Metadata: model.ObjectMeta{Namespace: "prod", Name: "standard-2x4"},
		Spec:     model.MachineInstanceTypeSpec{Resources: model.ResourceSpec{CPU: "2", Memory: "4Gi"}},
	}
	ctl, patched := newInstanceTypeTestController(t, []model.MachineInstanceType{it})
	machine := model.Machine{
		Metadata: model.ObjectMeta{Namespace: "prod", Name: "vm-1"},
		Spec:     model.MachineSpec{InstanceTypeName: "standard-2x4"},
	}
	out := ctl.resolveInstanceTypes(context.Background(), []model.Machine{machine})
	if out[0].Spec.Resources.CPU != "2" || out[0].Spec.Resources.Memory != "4Gi" {
		t.Fatalf("expected resolved resources, got %+v", out[0].Spec.Resources)
	}
	if len(*patched) != 1 || (*patched)[0] != "2/4Gi" {
		t.Fatalf("expected the API object to be patched with resolved resources, got %v", *patched)
	}
}

func TestResolveInstanceTypesLeavesExplicitResourcesAlone(t *testing.T) {
	it := model.MachineInstanceType{
		Metadata: model.ObjectMeta{Namespace: "prod", Name: "standard-2x4"},
		Spec:     model.MachineInstanceTypeSpec{Resources: model.ResourceSpec{CPU: "2", Memory: "4Gi"}},
	}
	ctl, patched := newInstanceTypeTestController(t, []model.MachineInstanceType{it})
	machine := model.Machine{
		Metadata: model.ObjectMeta{Namespace: "prod", Name: "vm-1"},
		Spec:     model.MachineSpec{InstanceTypeName: "standard-2x4", Resources: model.ResourceSpec{CPU: "8", Memory: "16Gi"}},
	}
	out := ctl.resolveInstanceTypes(context.Background(), []model.Machine{machine})
	if out[0].Spec.Resources.CPU != "8" || out[0].Spec.Resources.Memory != "16Gi" {
		t.Fatalf("expected explicit resources to win over instanceTypeName, got %+v", out[0].Spec.Resources)
	}
	if len(*patched) != 0 {
		t.Fatal("expected no patch when resources were already explicitly set")
	}
}

func TestResolveInstanceTypesSkipsMachinesWithoutInstanceTypeName(t *testing.T) {
	ctl, patched := newInstanceTypeTestController(t, nil)
	machine := model.Machine{
		Metadata: model.ObjectMeta{Namespace: "prod", Name: "vm-1"},
		Spec:     model.MachineSpec{Resources: model.ResourceSpec{CPU: "1", Memory: "1Gi"}},
	}
	ctl.resolveInstanceTypes(context.Background(), []model.Machine{machine})
	if len(*patched) != 0 {
		t.Fatal("expected no API call at all for a Machine with no instanceTypeName")
	}
}

func TestResolveInstanceTypesLeavesUnresolvedWhenNamedTypeMissing(t *testing.T) {
	ctl, patched := newInstanceTypeTestController(t, nil) // no instance types exist
	machine := model.Machine{
		Metadata: model.ObjectMeta{Namespace: "prod", Name: "vm-1"},
		Spec:     model.MachineSpec{InstanceTypeName: "does-not-exist"},
	}
	out := ctl.resolveInstanceTypes(context.Background(), []model.Machine{machine})
	if out[0].Spec.Resources.CPU != "" || out[0].Spec.Resources.Memory != "" {
		t.Fatalf("expected resources to stay empty when the named instance type doesn't exist, got %+v", out[0].Spec.Resources)
	}
	if len(*patched) != 0 {
		t.Fatal("expected no patch when the named instance type doesn't exist")
	}
}
