// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zyvorai/kairon/internal/model"
)

func TestListAndPatchMachine(t *testing.T) {
	var patched bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{{Metadata: model.ObjectMeta{Name: "vm1", Namespace: "default"}}}})
		case r.Method == http.MethodPatch:
			patched = r.Header.Get("Content-Type") == "application/merge-patch+json"
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer s.Close()
	c, _ := New(s.URL, "", "", false)
	c.HTTP = s.Client()
	items, err := c.ListMachines(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%v err=%v", items, err)
	}
	if err := c.PatchMachine(context.Background(), "default", "vm1", map[string]any{"spec": map[string]any{"nodeName": "n1"}}); err != nil {
		t.Fatal(err)
	}
	if !patched {
		t.Fatal("expected merge patch")
	}
}

func TestListMachineInstanceTypesNamespaceAndListMigrationPoliciesNamespace(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machineinstancetypes":
			_ = json.NewEncoder(w).Encode(model.MachineInstanceTypeList{Items: []model.MachineInstanceType{{Metadata: model.ObjectMeta{Name: "large", Namespace: "prod"}}}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/migrationpolicies":
			_ = json.NewEncoder(w).Encode(model.MigrationPolicyList{Items: []model.MigrationPolicy{{Metadata: model.ObjectMeta{Name: "default", Namespace: "prod"}}}})
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer s.Close()
	c, _ := New(s.URL, "", "", false)
	c.HTTP = s.Client()

	types, err := c.ListMachineInstanceTypesNamespace(context.Background(), "prod")
	if err != nil || len(types) != 1 || types[0].Metadata.Name != "large" {
		t.Fatalf("types=%v err=%v", types, err)
	}
	policies, err := c.ListMigrationPoliciesNamespace(context.Background(), "prod")
	if err != nil || len(policies) != 1 || policies[0].Metadata.Name != "default" {
		t.Fatalf("policies=%v err=%v", policies, err)
	}
}

func TestLeaseCreateGetUpdateAndConflict(t *testing.T) {
	const wantPath = "/apis/coordination.k8s.io/v1/namespaces/kairon-system/leases/kairon-controller"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/apis/coordination.k8s.io/v1/namespaces/kairon-system/leases":
			var l model.Lease
			_ = json.NewDecoder(r.Body).Decode(&l)
			l.Metadata.ResourceVersion = "1"
			_ = json.NewEncoder(w).Encode(l)
		case r.Method == http.MethodGet && r.URL.Path == wantPath:
			holder := "a"
			_ = json.NewEncoder(w).Encode(model.Lease{
				Metadata: model.ObjectMeta{Name: "kairon-controller", Namespace: "kairon-system", ResourceVersion: "1"},
				Spec:     model.LeaseSpec{HolderIdentity: &holder},
			})
		case r.Method == http.MethodPut && r.URL.Path == wantPath:
			var l model.Lease
			_ = json.NewDecoder(r.Body).Decode(&l)
			if l.Metadata.ResourceVersion != "1" {
				http.Error(w, "conflict", http.StatusConflict)
				return
			}
			l.Metadata.ResourceVersion = "2"
			_ = json.NewEncoder(w).Encode(l)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer s.Close()
	c, _ := New(s.URL, "", "", false)
	c.HTTP = s.Client()
	ctx := context.Background()

	created, err := c.CreateLease(ctx, "kairon-system", model.Lease{Metadata: model.ObjectMeta{Name: "kairon-controller", Namespace: "kairon-system"}})
	if err != nil || created.Metadata.ResourceVersion != "1" {
		t.Fatalf("CreateLease: created=%+v err=%v", created, err)
	}

	got, err := c.GetLease(ctx, "kairon-system", "kairon-controller")
	if err != nil || got.Metadata.ResourceVersion != "1" {
		t.Fatalf("GetLease: got=%+v err=%v", got, err)
	}

	// A stale resourceVersion is rejected with 409, surfaced via IsConflict.
	stale := got
	stale.Metadata.ResourceVersion = "0"
	if _, err := c.UpdateLease(ctx, "kairon-system", stale); !IsConflict(err) {
		t.Fatalf("UpdateLease with stale resourceVersion: err=%v, want a conflict", err)
	}

	updated, err := c.UpdateLease(ctx, "kairon-system", got)
	if err != nil || updated.Metadata.ResourceVersion != "2" {
		t.Fatalf("UpdateLease: updated=%+v err=%v", updated, err)
	}
}

func TestObserveCalledForEverySuccessfulAndFailedRequest(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			_ = json.NewEncoder(w).Encode(model.MachineList{})
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer s.Close()
	c, _ := New(s.URL, "", "", false)
	c.HTTP = s.Client()

	var calls []struct {
		method  string
		errored bool
	}
	c.Observe = func(method string, d time.Duration, err error) {
		if d < 0 {
			t.Errorf("negative duration observed: %v", d)
		}
		calls = append(calls, struct {
			method  string
			errored bool
		}{method, err != nil})
	}

	_ = c.request(context.Background(), http.MethodGet, "/ok", nil, nil, "")
	_ = c.request(context.Background(), http.MethodGet, "/fail", nil, nil, "")

	if len(calls) != 2 {
		t.Fatalf("Observe called %d times, want 2", len(calls))
	}
	if calls[0].method != http.MethodGet || calls[0].errored {
		t.Errorf("call[0] = %+v, want ok GET", calls[0])
	}
	if calls[1].method != http.MethodGet || !calls[1].errored {
		t.Errorf("call[1] = %+v, want errored GET", calls[1])
	}
}
