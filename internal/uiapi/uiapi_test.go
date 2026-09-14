// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package uiapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

// fakeKube is a minimal in-memory Kubernetes API double, just enough to
// exercise every internal/uiapi handler against a real *kube.Client over
// real HTTP (not a hand-rolled uiapi.Server double) -- the same intent as
// internal/integration's fakeCluster, but scoped to what this package's
// handlers actually call.
type fakeKube struct {
	mu         sync.Mutex
	machines   map[string]model.Machine
	migrations map[string]model.MachineMigration
	snapshots  map[string]model.MachineSnapshot
	nodes      []model.Node
	// secrets holds only stringData, keyed by "namespace/name" -- enough
	// to exercise Client.PatchSecretStringData (see
	// uiapi.Server.persistUsers) without modeling a full core/v1 Secret.
	secrets map[string]map[string]string
}

func newFakeKube() *fakeKube {
	return &fakeKube{
		machines:   map[string]model.Machine{},
		migrations: map[string]model.MachineMigration{},
		snapshots:  map[string]model.MachineSnapshot{},
		secrets:    map[string]map[string]string{},
	}
}

func (f *fakeKube) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			items := make([]model.Machine, 0, len(f.machines))
			for _, m := range f.machines {
				items = append(items, m)
			}
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: items})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines":
			items := make([]model.Machine, 0, len(f.machines))
			for _, m := range f.machines {
				items = append(items, m)
			}
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: items})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines/"):
			name := strings.TrimPrefix(r.URL.Path, "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines/")
			m, ok := f.machines[name]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(m)
		case r.Method == http.MethodPost && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines":
			var m model.Machine
			_ = json.NewDecoder(r.Body).Decode(&m)
			f.machines[m.Metadata.Name] = m
			_ = json.NewEncoder(w).Encode(m)
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines/"):
			name := strings.TrimPrefix(r.URL.Path, "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines/")
			m, ok := f.machines[name]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			var patch struct {
				Spec struct {
					PowerState string `json:"powerState"`
				} `json:"spec"`
			}
			_ = json.NewDecoder(r.Body).Decode(&patch)
			m.Spec.PowerState = patch.Spec.PowerState
			f.machines[name] = m
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines/"):
			name := strings.TrimPrefix(r.URL.Path, "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines/")
			if _, ok := f.machines[name]; !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			delete(f.machines, name)
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			items := make([]model.MachineMigration, 0, len(f.migrations))
			for _, m := range f.migrations {
				items = append(items, m)
			}
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: items})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinemigrations":
			items := make([]model.MachineMigration, 0, len(f.migrations))
			for _, m := range f.migrations {
				items = append(items, m)
			}
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: items})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinemigrations/"):
			name := strings.TrimPrefix(r.URL.Path, "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinemigrations/")
			m, ok := f.migrations[name]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(m)
		case r.Method == http.MethodPost && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinemigrations":
			var m model.MachineMigration
			_ = json.NewDecoder(r.Body).Decode(&m)
			f.migrations[m.Metadata.Name] = m
			_ = json.NewEncoder(w).Encode(m)
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinemigrations/"):
			name := strings.TrimPrefix(r.URL.Path, "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinemigrations/")
			m, ok := f.migrations[name]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			var patch struct {
				Spec struct {
					Recovery *model.MachineMigrationRecoverySpec `json:"recovery"`
				} `json:"spec"`
			}
			_ = json.NewDecoder(r.Body).Decode(&patch)
			m.Spec.Recovery = patch.Spec.Recovery
			f.migrations[name] = m
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinesnapshots":
			items := make([]model.MachineSnapshot, 0, len(f.snapshots))
			for _, s := range f.snapshots {
				items = append(items, s)
			}
			_ = json.NewEncoder(w).Encode(model.MachineSnapshotList{Items: items})
		case r.Method == http.MethodPost && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinesnapshots":
			var s model.MachineSnapshot
			_ = json.NewDecoder(r.Body).Decode(&s)
			f.snapshots[s.Metadata.Name] = s
			_ = json.NewEncoder(w).Encode(s)

		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(model.NodeList{Items: f.nodes})

		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/v1/namespaces/") && strings.Contains(r.URL.Path, "/secrets/"):
			key := strings.TrimPrefix(r.URL.Path, "/api/v1/namespaces/")
			key = strings.Replace(key, "/secrets/", "/", 1)
			var patch struct {
				StringData map[string]string `json:"stringData"`
			}
			_ = json.NewDecoder(r.Body).Decode(&patch)
			existing := f.secrets[key]
			if existing == nil {
				existing = map[string]string{}
			}
			for k, v := range patch.StringData {
				existing[k] = v
			}
			f.secrets[key] = existing
			w.WriteHeader(http.StatusOK)

		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
}

func newTestServer(t *testing.T, fk *fakeKube, token string) *Server {
	t.Helper()
	srv := httptest.NewServer(fk.handler())
	t.Cleanup(srv.Close)
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	return &Server{Kube: kc, Token: token}
}

func doJSON(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestAuthRejectsMissingOrWrongToken(t *testing.T) {
	s := newTestServer(t, newFakeKube(), "secret")
	h := s.Handler()

	if rr := doJSON(t, h, http.MethodGet, "/api/v1/machines", "", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("no token: expected 401, got %d", rr.Code)
	}
	if rr := doJSON(t, h, http.MethodGet, "/api/v1/machines", "wrong", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: expected 401, got %d", rr.Code)
	}
	if rr := doJSON(t, h, http.MethodGet, "/api/v1/machines", "secret", nil); rr.Code != http.StatusOK {
		t.Fatalf("correct token: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	// Health probes must never require the token -- Kubernetes never sends one.
	if rr := doJSON(t, h, http.MethodGet, "/healthz", "", nil); rr.Code != http.StatusOK {
		t.Fatalf("healthz: expected 200 without token, got %d", rr.Code)
	}
}

func TestAuditLogsMutatingRequestsNotReads(t *testing.T) {
	var logBuf bytes.Buffer
	fk := newFakeKube()
	fk.machines["db"] = model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "default"}}
	srv := httptest.NewServer(fk.handler())
	t.Cleanup(srv.Close)
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	s := &Server{Kube: kc, Log: slog.New(slog.NewTextHandler(&logBuf, nil))}
	h := s.Handler()

	// A read (GET) must not be audited.
	doJSON(t, h, http.MethodGet, "/api/v1/machines", "", nil)
	if logBuf.Len() != 0 {
		t.Fatalf("expected no audit log for a GET request, got: %s", logBuf.String())
	}

	// A mutating request must be audited, including its resulting status.
	doJSON(t, h, http.MethodPost, "/api/v1/machines/default/db/stop", "", nil)
	logged := logBuf.String()
	if !strings.Contains(logged, "uiapi request") ||
		!strings.Contains(logged, "method=POST") ||
		!strings.Contains(logged, "path=/api/v1/machines/default/db/stop") ||
		!strings.Contains(logged, "status=204") {
		t.Fatalf("expected an audit log line with method/path/status, got: %s", logged)
	}
}

func TestCreateAndListMachines(t *testing.T) {
	s := newTestServer(t, newFakeKube(), "")
	h := s.Handler()

	rr := doJSON(t, h, http.MethodPost, "/api/v1/machines", "", createMachineRequest{Name: "db", Image: "/images/db.qcow2"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var created model.Machine
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Spec.Resources.CPU != "2" || created.Spec.Resources.Memory != "2Gi" || created.Spec.Runtime.Backend != "qemu" {
		t.Fatalf("expected defaults applied, got %+v", created.Spec)
	}

	rr = doJSON(t, h, http.MethodGet, "/api/v1/machines", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", rr.Code)
	}
	var items []model.Machine
	if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(items) != 1 || items[0].Metadata.Name != "db" {
		t.Fatalf("expected one machine named db, got %+v", items)
	}
}

func TestCreateMachineRequiresNameAndImage(t *testing.T) {
	s := newTestServer(t, newFakeKube(), "")
	h := s.Handler()
	rr := doJSON(t, h, http.MethodPost, "/api/v1/machines", "", createMachineRequest{Name: "db"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 missing image, got %d", rr.Code)
	}
}

func TestGetMachineNotFoundMapsTo404(t *testing.T) {
	s := newTestServer(t, newFakeKube(), "")
	h := s.Handler()
	rr := doJSON(t, h, http.MethodGet, "/api/v1/machines/default/nope", "", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestPowerMachine(t *testing.T) {
	fk := newFakeKube()
	fk.machines["db"] = model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "default"}}
	s := newTestServer(t, fk, "")
	h := s.Handler()

	rr := doJSON(t, h, http.MethodPost, "/api/v1/machines/default/db/stop", "", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("stop: expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
	fk.mu.Lock()
	got := fk.machines["db"].Spec.PowerState
	fk.mu.Unlock()
	if got != "Stopped" {
		t.Fatalf("expected powerState Stopped, got %q", got)
	}
}

func TestDeleteMachine(t *testing.T) {
	fk := newFakeKube()
	fk.machines["db"] = model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "default"}}
	s := newTestServer(t, fk, "")
	h := s.Handler()
	rr := doJSON(t, h, http.MethodDelete, "/api/v1/machines/default/db", "", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
	fk.mu.Lock()
	_, ok := fk.machines["db"]
	fk.mu.Unlock()
	if ok {
		t.Fatalf("expected db to be deleted")
	}
}

func TestCreateMigrationDefaultsAndAutoName(t *testing.T) {
	s := newTestServer(t, newFakeKube(), "")
	h := s.Handler()
	rr := doJSON(t, h, http.MethodPost, "/api/v1/migrations", "", createMigrationRequest{Machine: "db", TargetNode: "worker-2"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var out model.MachineMigration
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Metadata.Name == "" || !strings.HasPrefix(out.Metadata.Name, "migration-db-") {
		t.Fatalf("expected auto-generated name prefixed migration-db-, got %q", out.Metadata.Name)
	}
	if out.Spec.Strategy != "auto" || out.Spec.Mode != "pre-copy" {
		t.Fatalf("expected default strategy/mode applied, got %+v", out.Spec)
	}
}

func TestCreateMigrationRequiresMachine(t *testing.T) {
	s := newTestServer(t, newFakeKube(), "")
	h := s.Handler()
	rr := doJSON(t, h, http.MethodPost, "/api/v1/migrations", "", createMigrationRequest{})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestEvacuateCreatesOneMigrationPerAssignedMachine(t *testing.T) {
	fk := newFakeKube()
	fk.machines["a"] = model.Machine{Metadata: model.ObjectMeta{Name: "a", Namespace: "default"}, Spec: model.MachineSpec{NodeName: "worker-1"}}
	fk.machines["b"] = model.Machine{Metadata: model.ObjectMeta{Name: "b", Namespace: "default"}, Spec: model.MachineSpec{NodeName: "worker-1"}}
	fk.machines["c"] = model.Machine{Metadata: model.ObjectMeta{Name: "c", Namespace: "default"}, Spec: model.MachineSpec{NodeName: "worker-2"}}
	s := newTestServer(t, fk, "")
	h := s.Handler()

	rr := doJSON(t, h, http.MethodPost, "/api/v1/migrations/evacuate", "", evacuateRequest{Node: "worker-1"})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var out evacuateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Created) != 2 {
		t.Fatalf("expected 2 migrations created for worker-1, got %d: %+v", len(out.Created), out.Created)
	}
}

func TestRecoverMigrationRequiresNeedsRecoveryPhase(t *testing.T) {
	fk := newFakeKube()
	fk.migrations["m1"] = model.MachineMigration{Metadata: model.ObjectMeta{Name: "m1", Namespace: "default"}, Status: model.MachineMigrationStatus{Phase: "Running"}}
	s := newTestServer(t, fk, "")
	h := s.Handler()

	rr := doJSON(t, h, http.MethodPost, "/api/v1/migrations/default/m1/recover", "", recoverRequest{
		Action: "ForceAbort", AcknowledgedDiagnosis: "Unknown", Reason: "test",
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 for non-NeedsRecovery phase, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRecoverMigrationRequiresAllFields(t *testing.T) {
	s := newTestServer(t, newFakeKube(), "")
	h := s.Handler()
	rr := doJSON(t, h, http.MethodPost, "/api/v1/migrations/default/m1/recover", "", recoverRequest{Action: "ForceAbort"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 missing diagnosis/reason, got %d", rr.Code)
	}
}

func TestRecoverMigrationAppliesPatchWhenValid(t *testing.T) {
	fk := newFakeKube()
	fk.migrations["m1"] = model.MachineMigration{Metadata: model.ObjectMeta{Name: "m1", Namespace: "default"}, Status: model.MachineMigrationStatus{Phase: "NeedsRecovery"}}
	s := newTestServer(t, fk, "")
	h := s.Handler()

	rr := doJSON(t, h, http.MethodPost, "/api/v1/migrations/default/m1/recover", "", recoverRequest{
		Action: "ForceAbort", AcknowledgedDiagnosis: "Unknown", Reason: "operator drill",
	})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
	fk.mu.Lock()
	spec := fk.migrations["m1"].Spec.Recovery
	fk.mu.Unlock()
	if spec == nil || spec.Action != "ForceAbort" || spec.Reason != "operator drill" {
		t.Fatalf("expected recovery spec patched, got %+v", spec)
	}
}

func TestCreateSnapshot(t *testing.T) {
	s := newTestServer(t, newFakeKube(), "")
	h := s.Handler()
	rr := doJSON(t, h, http.MethodPost, "/api/v1/snapshots", "", createSnapshotRequest{Machine: "db"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestListSnapshots(t *testing.T) {
	fk := newFakeKube()
	fk.snapshots["s1"] = model.MachineSnapshot{Metadata: model.ObjectMeta{Name: "s1", Namespace: "default"}}
	s := newTestServer(t, fk, "")
	h := s.Handler()
	rr := doJSON(t, h, http.MethodGet, "/api/v1/snapshots", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var items []model.MachineSnapshot
	if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 1 || items[0].Metadata.Name != "s1" {
		t.Fatalf("expected one snapshot named s1, got %+v", items)
	}
}

func TestListAndGetMigrations(t *testing.T) {
	fk := newFakeKube()
	fk.migrations["m1"] = model.MachineMigration{Metadata: model.ObjectMeta{Name: "m1", Namespace: "default"}, Status: model.MachineMigrationStatus{Phase: "Running"}}
	s := newTestServer(t, fk, "")
	h := s.Handler()

	rr := doJSON(t, h, http.MethodGet, "/api/v1/migrations", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var items []model.MachineMigration
	if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(items) != 1 || items[0].Metadata.Name != "m1" {
		t.Fatalf("expected one migration named m1, got %+v", items)
	}

	rr = doJSON(t, h, http.MethodGet, "/api/v1/migrations/default/m1", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var got model.MachineMigration
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if got.Status.Phase != "Running" {
		t.Fatalf("expected phase Running, got %q", got.Status.Phase)
	}

	rr = doJSON(t, h, http.MethodGet, "/api/v1/migrations/default/nope", "", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("get missing: expected 404, got %d", rr.Code)
	}
}

func TestListNodes(t *testing.T) {
	fk := newFakeKube()
	fk.nodes = []model.Node{{Metadata: model.ObjectMeta{Name: "worker-1"}}, {Metadata: model.ObjectMeta{Name: "worker-2"}}}
	s := newTestServer(t, fk, "")
	h := s.Handler()
	rr := doJSON(t, h, http.MethodGet, "/api/v1/nodes", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var items []model.Node
	if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(items))
	}
}

func TestOverviewAggregatesCounts(t *testing.T) {
	fk := newFakeKube()
	fk.machines["a"] = model.Machine{Metadata: model.ObjectMeta{Name: "a"}, Status: model.MachineStatus{Phase: "Running"}}
	fk.machines["b"] = model.Machine{Metadata: model.ObjectMeta{Name: "b"}, Status: model.MachineStatus{Phase: "Running"}}
	fk.migrations["m1"] = model.MachineMigration{Metadata: model.ObjectMeta{Name: "m1"}, Status: model.MachineMigrationStatus{Phase: "NeedsRecovery"}}
	fk.migrations["m2"] = model.MachineMigration{Metadata: model.ObjectMeta{Name: "m2"}, Status: model.MachineMigrationStatus{Phase: "Succeeded"}}
	fk.nodes = []model.Node{{Metadata: model.ObjectMeta{Name: "worker-1"}}}
	s := newTestServer(t, fk, "")
	h := s.Handler()

	rr := doJSON(t, h, http.MethodGet, "/api/v1/overview", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var out overviewResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Machines.Total != 2 || out.Machines.ByPhase["Running"] != 2 {
		t.Fatalf("expected 2 running machines, got %+v", out.Machines)
	}
	if out.Migrations.Total != 2 || out.Migrations.NeedsRecovery != 1 || out.Migrations.Active != 1 {
		t.Fatalf("expected 1 NeedsRecovery / 1 active of 2 total migrations, got %+v", out.Migrations)
	}
	if out.Nodes != 1 {
		t.Fatalf("expected 1 node, got %d", out.Nodes)
	}
}

func TestServeWebFallsBackToIndexAndBlocksTraversal(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "webdir")
	secret := filepath.Join(parent, "secret")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir webdir: %v", err)
	}
	if err := os.MkdirAll(secret, 0o755); err != nil {
		t.Fatalf("mkdir secret: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>kairon-ui</html>"), 0o600); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log(1)"), 0o600); err != nil {
		t.Fatalf("write app.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(secret, "outside.txt"), []byte("do not serve me"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	s := &Server{Kube: mustKubeClient(t), WebDir: dir}
	h := s.Handler()

	rr := doJSON(t, h, http.MethodGet, "/does-not-exist", "", nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "kairon-ui") {
		t.Fatalf("expected SPA fallback to index.html, got %d: %s", rr.Code, rr.Body.String())
	}

	rr = doJSON(t, h, http.MethodGet, "/app.js", "", nil)
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") == "" {
		t.Fatalf("expected app.js served with a Content-Type, got %d %q", rr.Code, rr.Header().Get("Content-Type"))
	}

	// http.ServeMux itself already cleans ".." out of the request path and
	// redirects before dispatch, so a request through the full Handler()
	// never actually reaches serveWeb's own guard with a raw ".." path --
	// call it directly to exercise that guard specifically, the same way
	// an attacker who found a way to smuggle an uncleaned path to this
	// handler (e.g. a future refactor that stops using http.ServeMux)
	// would be stopped by serveWeb itself, not just by the mux in front of it.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.URL.Path = "/../secret/outside.txt"
	rr = httptest.NewRecorder()
	s.serveWeb(rr, req)
	if strings.Contains(rr.Body.String(), "do not serve me") {
		t.Fatalf("path traversal was not blocked: %s", rr.Body.String())
	}
}

func mustKubeClient(t *testing.T) *kube.Client {
	t.Helper()
	kc, err := kube.New("http://127.0.0.1:0", "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	return kc
}
