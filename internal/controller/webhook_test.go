// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zyvorai/kairon/internal/admission"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

// newWebhookTestController wires a *Controller to an in-memory fake
// Kubernetes API serving exactly the namespaced list endpoints
// validateMachine/validateMachineMigration call -- mirrors
// TestReconcileSchedulesMachine's own inline-httptest-server convention
// above rather than introducing a new test-double abstraction.
func newWebhookTestController(t *testing.T, ns string, quotas []model.MachineQuota, machines []model.Machine, budgets []model.MachineDisruptionBudget, migrations []model.MachineMigration) *Controller {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/"+ns+"/machinequotas":
			_ = json.NewEncoder(w).Encode(model.MachineQuotaList{Items: quotas})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/"+ns+"/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: machines})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/"+ns+"/machinedisruptionbudgets":
			_ = json.NewEncoder(w).Encode(model.MachineDisruptionBudgetList{Items: budgets})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/"+ns+"/machinemigrations":
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: migrations})
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
	return &Controller{Kube: kc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func admissionReq(t *testing.T, resource, namespace, operation string, obj any) (*http.Request, *admission.Request) {
	t.Helper()
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal object: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/validate", nil).WithContext(context.Background())
	return r, &admission.Request{
		UID:       "test-uid",
		Resource:  admission.GroupVersionResource{Group: "kairon.zyvor.dev", Version: "v1alpha1", Resource: resource},
		Namespace: namespace,
		Operation: operation,
		Object:    raw,
	}
}

func TestValidateMachineIgnoresNonMachineResourcesAndUpdates(t *testing.T) {
	ctl := newWebhookTestController(t, "prod", []model.MachineQuota{{Spec: model.MachineQuotaSpec{MaxMachines: intPtr(0)}, Metadata: model.ObjectMeta{Namespace: "prod", Name: "q"}}}, nil, nil, nil)

	r, req := admissionReq(t, "machinemigrations", "prod", admission.OperationCreate, model.Machine{})
	if d := ctl.validateMachine(r, req); !d.Allowed {
		t.Fatalf("expected non-machines resource to be ignored, got denied: %s", d.Reason)
	}

	r, req = admissionReq(t, "machines", "prod", admission.OperationUpdate, model.Machine{Metadata: model.ObjectMeta{Namespace: "prod"}})
	if d := ctl.validateMachine(r, req); !d.Allowed {
		t.Fatalf("expected UPDATE to be ignored (quota is create-only, matching the reconcile loop), got denied: %s", d.Reason)
	}
}

func TestValidateMachineAllowsCreateWithNoQuotasConfigured(t *testing.T) {
	ctl := newWebhookTestController(t, "prod", nil, nil, nil, nil)
	r, req := admissionReq(t, "machines", "prod", admission.OperationCreate, model.Machine{Metadata: model.ObjectMeta{Namespace: "prod", Name: "vm-1"}})
	if d := ctl.validateMachine(r, req); !d.Allowed {
		t.Fatalf("expected allow with no MachineQuota in the namespace, got denied: %s", d.Reason)
	}
}

func TestValidateMachineDeniesCreateOverMaxMachines(t *testing.T) {
	quota := model.MachineQuota{Metadata: model.ObjectMeta{Namespace: "prod", Name: "q"}, Spec: model.MachineQuotaSpec{MaxMachines: intPtr(1)}}
	existing := []model.Machine{{Metadata: model.ObjectMeta{Namespace: "prod", Name: "already-scheduled"}, Spec: model.MachineSpec{NodeName: "worker-1", PowerState: "Running"}}}
	ctl := newWebhookTestController(t, "prod", []model.MachineQuota{quota}, existing, nil, nil)

	incoming := model.Machine{Metadata: model.ObjectMeta{Namespace: "prod", Name: "new-vm"}}
	r, req := admissionReq(t, "machines", "prod", admission.OperationCreate, incoming)
	d := ctl.validateMachine(r, req)
	if d.Allowed {
		t.Fatal("expected the second Machine to be denied once maxMachines=1 is already used")
	}
	if d.Reason == "" {
		t.Fatal("expected a non-empty denial reason")
	}
}

func TestValidateMachineAllowsCreateUnderQuota(t *testing.T) {
	quota := model.MachineQuota{Metadata: model.ObjectMeta{Namespace: "prod", Name: "q"}, Spec: model.MachineQuotaSpec{MaxMachines: intPtr(5)}}
	ctl := newWebhookTestController(t, "prod", []model.MachineQuota{quota}, nil, nil, nil)
	incoming := model.Machine{Metadata: model.ObjectMeta{Namespace: "prod", Name: "vm-1"}}
	r, req := admissionReq(t, "machines", "prod", admission.OperationCreate, incoming)
	if d := ctl.validateMachine(r, req); !d.Allowed {
		t.Fatalf("expected allow under quota, got denied: %s", d.Reason)
	}
}

func TestValidateMachineMigrationIgnoresNonMigrationResourcesAndUpdates(t *testing.T) {
	ctl := newWebhookTestController(t, "prod", nil, nil, nil, nil)
	r, req := admissionReq(t, "machines", "prod", admission.OperationCreate, model.MachineMigration{})
	if d := ctl.validateMachineMigration(r, req); !d.Allowed {
		t.Fatalf("expected non-migration resource to be ignored, got denied: %s", d.Reason)
	}
	r, req = admissionReq(t, "machinemigrations", "prod", admission.OperationUpdate, model.MachineMigration{})
	if d := ctl.validateMachineMigration(r, req); !d.Allowed {
		t.Fatalf("expected UPDATE to be ignored, got denied: %s", d.Reason)
	}
}

func TestValidateMachineMigrationAllowsWithNoBudgetsConfigured(t *testing.T) {
	ctl := newWebhookTestController(t, "prod", nil, nil, nil, nil)
	mig := model.MachineMigration{Metadata: model.ObjectMeta{Namespace: "prod", Name: "m1"}, Spec: model.MachineMigrationSpec{MachineName: "web-1"}}
	r, req := admissionReq(t, "machinemigrations", "prod", admission.OperationCreate, mig)
	if d := ctl.validateMachineMigration(r, req); !d.Allowed {
		t.Fatalf("expected allow with no MachineDisruptionBudget in the namespace, got denied: %s", d.Reason)
	}
}

func TestValidateMachineMigrationAllowsWhenTargetMachineNotFound(t *testing.T) {
	budget := model.MachineDisruptionBudget{Metadata: model.ObjectMeta{Namespace: "prod", Name: "b"}, Spec: model.MachineDisruptionBudgetSpec{Selector: map[string]string{"tier": "web"}, MinAvailable: "100"}}
	ctl := newWebhookTestController(t, "prod", nil, nil, []model.MachineDisruptionBudget{budget}, nil)
	mig := model.MachineMigration{Metadata: model.ObjectMeta{Namespace: "prod", Name: "m1"}, Spec: model.MachineMigrationSpec{MachineName: "no-such-machine"}}
	r, req := admissionReq(t, "machinemigrations", "prod", admission.OperationCreate, mig)
	if d := ctl.validateMachineMigration(r, req); !d.Allowed {
		t.Fatalf("expected allow when the referenced Machine doesn't exist (nothing to check a budget's selector against), got denied: %s", d.Reason)
	}
}

func TestValidateMachineMigrationDeniesWhenBudgetExhausted(t *testing.T) {
	machines := []model.Machine{
		{Metadata: model.ObjectMeta{Namespace: "prod", Name: "web-1", Labels: map[string]string{"tier": "web"}}, Status: model.MachineStatus{Phase: "Running"}},
		{Metadata: model.ObjectMeta{Namespace: "prod", Name: "web-2", Labels: map[string]string{"tier": "web"}}, Status: model.MachineStatus{Phase: "Running"}},
	}
	budget := model.MachineDisruptionBudget{Metadata: model.ObjectMeta{Namespace: "prod", Name: "b"}, Spec: model.MachineDisruptionBudgetSpec{Selector: map[string]string{"tier": "web"}, MinAvailable: "2"}}
	ctl := newWebhookTestController(t, "prod", nil, machines, []model.MachineDisruptionBudget{budget}, nil)
	mig := model.MachineMigration{Metadata: model.ObjectMeta{Namespace: "prod", Name: "m1"}, Spec: model.MachineMigrationSpec{MachineName: "web-1"}}
	r, req := admissionReq(t, "machinemigrations", "prod", admission.OperationCreate, mig)
	d := ctl.validateMachineMigration(r, req)
	if d.Allowed {
		t.Fatal("expected deny: minAvailable=2 with only 2 healthy web machines allows zero disruptions")
	}
}

func TestValidateMachineMigrationAllowsWithinBudget(t *testing.T) {
	machines := []model.Machine{
		{Metadata: model.ObjectMeta{Namespace: "prod", Name: "web-1", Labels: map[string]string{"tier": "web"}}, Status: model.MachineStatus{Phase: "Running"}},
		{Metadata: model.ObjectMeta{Namespace: "prod", Name: "web-2", Labels: map[string]string{"tier": "web"}}, Status: model.MachineStatus{Phase: "Running"}},
		{Metadata: model.ObjectMeta{Namespace: "prod", Name: "web-3", Labels: map[string]string{"tier": "web"}}, Status: model.MachineStatus{Phase: "Running"}},
	}
	budget := model.MachineDisruptionBudget{Metadata: model.ObjectMeta{Namespace: "prod", Name: "b"}, Spec: model.MachineDisruptionBudgetSpec{Selector: map[string]string{"tier": "web"}, MinAvailable: "2"}}
	ctl := newWebhookTestController(t, "prod", nil, machines, []model.MachineDisruptionBudget{budget}, nil)
	mig := model.MachineMigration{Metadata: model.ObjectMeta{Namespace: "prod", Name: "m1"}, Spec: model.MachineMigrationSpec{MachineName: "web-1"}}
	r, req := admissionReq(t, "machinemigrations", "prod", admission.OperationCreate, mig)
	if d := ctl.validateMachineMigration(r, req); !d.Allowed {
		t.Fatalf("expected allow: minAvailable=2 with 3 healthy web machines allows 1 disruption, got denied: %s", d.Reason)
	}
}

// TestWebhookHandlerEndToEnd exercises the real AdmissionReview HTTP
// envelope (internal/admission), not just the Validator functions directly
// -- confirms encode/decode round-trips through WebhookHandler correctly.
func TestWebhookHandlerEndToEnd(t *testing.T) {
	quota := model.MachineQuota{Metadata: model.ObjectMeta{Namespace: "prod", Name: "q"}, Spec: model.MachineQuotaSpec{MaxMachines: intPtr(0)}}
	ctl := newWebhookTestController(t, "prod", []model.MachineQuota{quota}, nil, nil, nil)
	h := ctl.WebhookHandler()

	incoming := model.Machine{Metadata: model.ObjectMeta{Namespace: "prod", Name: "vm-1"}}
	raw, _ := json.Marshal(incoming)
	review := admission.Review{
		APIVersion: admission.APIVersion,
		Kind:       "AdmissionReview",
		Request: &admission.Request{
			UID:       "abc-123",
			Resource:  admission.GroupVersionResource{Group: "kairon.zyvor.dev", Version: "v1alpha1", Resource: "machines"},
			Namespace: "prod",
			Operation: admission.OperationCreate,
			Object:    raw,
		},
	}
	body, _ := json.Marshal(review)
	httpReq := httptest.NewRequest(http.MethodPost, "/validate-machine", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httpReq)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (AdmissionReview responses are always 200, the decision is in the body), got %d", rr.Code)
	}
	var out admission.Review
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Response == nil {
		t.Fatal("expected a response")
	}
	if out.Response.UID != "abc-123" {
		t.Fatalf("expected the response UID to echo the request UID, got %q", out.Response.UID)
	}
	if out.Response.Allowed {
		t.Fatal("expected deny (maxMachines=0)")
	}
	if out.Response.Status == nil || out.Response.Status.Message == "" {
		t.Fatal("expected a denial message")
	}
}
