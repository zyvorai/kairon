// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

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

// TestReconcileSelfHealsAFluxVMStoppedRuntime covers the safety net
// fluxvm.Client.RestoreSnapshot's own doc comment describes: if a
// stop->start-from-snapshot orchestration is interrupted after the stop
// succeeds, FluxVM is left reporting the runtime Stopped while
// spec.powerState still wants Running. Reconcile must notice the mismatch
// and call Start (not start-from-snapshot -- this recovers to the VM's
// last-known-good disk state, it never re-attempts the restore itself).
func TestReconcileSelfHealsAFluxVMStoppedRuntime(t *testing.T) {
	m := pausedMachine("Running")
	var gotStatus model.MachineStatus
	ks := fakeKubeForMachine(t, &m, &gotStatus)
	defer ks.Close()

	var startCalled bool
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-1":
			status := "Stopped"
			if startCalled {
				status = "Running"
			}
			_ = json.NewEncoder(w).Encode(fluxvm.Record{UUID: "vm-1", Status: status})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/vms/vm-1/start":
			startCalled = true
			_ = json.NewEncoder(w).Encode(fluxvm.Record{UUID: "vm-1", Status: "Running"})
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
	if !startCalled {
		t.Fatal("expected Start to be called against FluxVM to recover a Stopped runtime")
	}
	if gotStatus.Phase != "Running" {
		t.Fatalf("unexpected status: %+v", gotStatus)
	}
}

// TestReconcileNeverStartsAnIntentionallyStoppedMachine is the regression
// guard on the branch's own DesiredPowerState() != "Stopped" condition:
// spec.powerState: Stopped goes through ensureStopped's own Delete path
// entirely, never this one -- a Machine with no runtime left has no
// "/v1/vms/vm-1" route to even answer a GET, so any accidental Start call
// here would hit a 404 the fake server turns into a fatal test failure.
func TestReconcileNeverStartsAnIntentionallyStoppedMachine(t *testing.T) {
	m := pausedMachine("Stopped")
	m.Status.RuntimeID = ""
	var gotStatus model.MachineStatus
	ks := fakeKubeForMachine(t, &m, &gotStatus)
	defer ks.Close()

	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/vms" && r.URL.Query().Get("name") == "kairon-prod-db":
			_ = json.NewEncoder(w).Encode([]fluxvm.Record{})
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
	if gotStatus.Phase != "Stopped" {
		t.Fatalf("unexpected status: %+v", gotStatus)
	}
}
