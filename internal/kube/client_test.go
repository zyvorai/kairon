// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
