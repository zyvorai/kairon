// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kaironctl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

func TestMigrationStillPending(t *testing.T) {
	for phase, want := range map[string]bool{
		"":          true, // not yet reconciled -- must NOT be treated as safe to recreate
		"Starting":  true,
		"Running":   true,
		"Cutover":   true,
		"Succeeded": false,
		"Failed":    false,
		"Blocked":   false,
	} {
		if got := migrationStillPending(phase); got != want {
			t.Errorf("migrationStillPending(%q) = %v, want %v", phase, got, want)
		}
	}
}

// evacuateTestServer is a minimal in-memory fake of the endpoints
// evacuatePass calls, mirroring the inline-httptest-server convention
// already used throughout internal/controller's own tests.
type evacuateTestServer struct {
	machines   []model.Machine
	migrations []model.MachineMigration
	budgets    []model.MachineDisruptionBudget
	created    []model.MachineMigration
}

func (s *evacuateTestServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: s.machines})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations":
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: s.migrations})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinedisruptionbudgets":
			_ = json.NewEncoder(w).Encode(model.MachineDisruptionBudgetList{Items: s.budgets})
		case r.Method == http.MethodPost && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinemigrations":
			var m model.MachineMigration
			_ = json.NewDecoder(r.Body).Decode(&m)
			s.created = append(s.created, m)
			_ = json.NewEncoder(w).Encode(m)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
}

func evacuateMachine(name string) model.Machine {
	return model.Machine{Metadata: model.ObjectMeta{Name: name, Namespace: "default"}, Spec: model.MachineSpec{NodeName: "worker-1"}, Status: model.MachineStatus{Phase: "Running"}}
}

func TestEvacuatePassCreatesMigrationsForEligibleMachines(t *testing.T) {
	s := &evacuateTestServer{machines: []model.Machine{evacuateMachine("vm-1"), evacuateMachine("vm-2")}}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	kc.HTTP = srv.Client()

	created, skipped, remaining, err := evacuatePass(context.Background(), kc, "worker-1", "cold")
	if err != nil {
		t.Fatalf("evacuatePass: %v", err)
	}
	if created != 2 || skipped != 0 || remaining != 2 {
		t.Fatalf("created=%d skipped=%d remaining=%d, want 2/0/2", created, skipped, remaining)
	}
	if len(s.created) != 2 {
		t.Fatalf("expected 2 MachineMigrations created, got %d", len(s.created))
	}
}

func TestEvacuatePassSkipsMachineAlreadyMidMigrationInsteadOfDuplicating(t *testing.T) {
	s := &evacuateTestServer{
		machines: []model.Machine{evacuateMachine("vm-1")},
		migrations: []model.MachineMigration{
			{Metadata: model.ObjectMeta{Namespace: "default", Name: "existing"}, Spec: model.MachineMigrationSpec{MachineName: "vm-1"}, Status: model.MachineMigrationStatus{Phase: "Running"}},
		},
	}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	kc.HTTP = srv.Client()

	created, skipped, remaining, err := evacuatePass(context.Background(), kc, "worker-1", "cold")
	if err != nil {
		t.Fatalf("evacuatePass: %v", err)
	}
	// Already mid-migration: not created again, not counted as
	// budget-skipped either, but still remaining (hasn't left the node yet).
	if created != 0 || skipped != 0 || remaining != 1 {
		t.Fatalf("created=%d skipped=%d remaining=%d, want 0/0/1", created, skipped, remaining)
	}
	if len(s.created) != 0 {
		t.Fatalf("expected no new MachineMigration, got %d", len(s.created))
	}
}

// TestEvacuatePassSkipsMachineWithFreshlyCreatedMigrationTooEmptyPhase is a
// regression test for a real bug found running evacuate twice in quick
// succession against a real cluster: a MachineMigration's status.phase is
// "" until kairon-controller's own reconcile loop picks it up (up to one
// reconcile interval later), and an earlier version of migrationStillPending
// (copy-pasted from a different helper with different semantics) treated
// "" as terminal, so a fast second evacuate call created a real duplicate
// migration for the same Machine.
func TestEvacuatePassSkipsMachineWithFreshlyCreatedMigrationTooEmptyPhase(t *testing.T) {
	s := &evacuateTestServer{
		machines: []model.Machine{evacuateMachine("vm-1")},
		migrations: []model.MachineMigration{
			{Metadata: model.ObjectMeta{Namespace: "default", Name: "existing"}, Spec: model.MachineMigrationSpec{MachineName: "vm-1"}, Status: model.MachineMigrationStatus{Phase: ""}},
		},
	}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	kc.HTTP = srv.Client()

	created, _, remaining, err := evacuatePass(context.Background(), kc, "worker-1", "cold")
	if err != nil {
		t.Fatalf("evacuatePass: %v", err)
	}
	if created != 0 || remaining != 1 {
		t.Fatalf("created=%d remaining=%d, want 0/1", created, remaining)
	}
	if len(s.created) != 0 {
		t.Fatalf("expected no new MachineMigration for a Machine with a pending (phase=\"\") migration already, got %d", len(s.created))
	}
}

func TestEvacuatePassSkipsMachineBlockedByDisruptionBudget(t *testing.T) {
	blocked := evacuateMachine("vm-1")
	blocked.Metadata.Labels = map[string]string{"tier": "web"}
	s := &evacuateTestServer{
		machines: []model.Machine{blocked},
		budgets: []model.MachineDisruptionBudget{
			{Metadata: model.ObjectMeta{Name: "pdb", Namespace: "default"}, Spec: model.MachineDisruptionBudgetSpec{Selector: map[string]string{"tier": "web"}, MinAvailable: "1"}},
		},
	}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	kc.HTTP = srv.Client()

	created, skipped, remaining, err := evacuatePass(context.Background(), kc, "worker-1", "cold")
	if err != nil {
		t.Fatalf("evacuatePass: %v", err)
	}
	if created != 0 || skipped != 1 || remaining != 1 {
		t.Fatalf("created=%d skipped=%d remaining=%d, want 0/1/1", created, skipped, remaining)
	}
}

func TestEvacuatePassIgnoresMachinesOnOtherNodes(t *testing.T) {
	other := evacuateMachine("vm-elsewhere")
	other.Spec.NodeName = "worker-2"
	s := &evacuateTestServer{machines: []model.Machine{other}}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	kc.HTTP = srv.Client()

	created, skipped, remaining, err := evacuatePass(context.Background(), kc, "worker-1", "cold")
	if err != nil {
		t.Fatalf("evacuatePass: %v", err)
	}
	if created != 0 || skipped != 0 || remaining != 0 {
		t.Fatalf("created=%d skipped=%d remaining=%d, want 0/0/0", created, skipped, remaining)
	}
}

func TestResourceKindAndName(t *testing.T) {
	// A single positional arg is NAME alone, kind defaults to "machine" --
	// preserves the exact prior `describe NAME`/`delete NAME` calling
	// convention untouched by this change.
	kind, name := resourceKindAndName("delete", []string{"my-vm"})
	if kind != "machine" || name != "my-vm" {
		t.Fatalf("kind=%q name=%q, want machine/my-vm", kind, name)
	}
	// Two positional args are KIND NAME -- disambiguated purely by count,
	// never by matching NAME's own text against the alias list, so a
	// Machine literally named e.g. "snapshot" is never misrouted.
	kind, name = resourceKindAndName("delete", []string{"snapshot", "my-snap"})
	if kind != "snapshot" || name != "my-snap" {
		t.Fatalf("kind=%q name=%q, want snapshot/my-snap", kind, name)
	}
	kind, name = resourceKindAndName("describe", []string{"snapshot", "snapshot"})
	if kind != "snapshot" || name != "snapshot" {
		t.Fatalf("kind=%q name=%q, want snapshot/snapshot (a resource literally named after its own kind)", kind, name)
	}
}

func TestParseKeyValues(t *testing.T) {
	got, err := parseKeyValues([]string{"tier=web", "env=prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["tier"] != "web" || got["env"] != "prod" || len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if got, err := parseKeyValues(nil); err != nil || got != nil {
		t.Fatalf("empty input: got %v, %v, want nil, nil", got, err)
	}
	if _, err := parseKeyValues([]string{"no-equals-sign"}); err == nil {
		t.Fatal("expected an error for a value with no '='")
	}
	if _, err := parseKeyValues([]string{"=v"}); err == nil {
		t.Fatal("expected an error for an empty key")
	}
}

// recordingServer captures the last request it received (method, path, and
// decoded JSON body) so a test can assert exactly what kaironctl sent
// without standing up a full fake apiserver per resource kind -- these new
// create/scale/edit verbs are exercised at this HTTP boundary rather than
// against internal/kube.Client's own methods directly, so a bug in the
// flag-to-JSON mapping is what actually gets caught.
type recordingServer struct {
	method string
	path   string
	body   map[string]any
}

func (s *recordingServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.method, s.path, s.body = r.Method, r.URL.Path, body
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
}

func testClient(t *testing.T, s *recordingServer) *kube.Client {
	t.Helper()
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	return &kube.Client{BaseURL: srv.URL, HTTP: srv.Client()}
}

func TestCmdCreateMachineSetPostsExpectedSpec(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdCreateMachineSet(context.Background(), kc, []string{"web", "--image", "/img.qcow2", "--replicas", "3", "--cpu", "4", "--label", "tier=web"})
	if s.method != http.MethodPost || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinesets" {
		t.Fatalf("method=%s path=%s", s.method, s.path)
	}
	spec, _ := s.body["spec"].(map[string]any)
	if spec["replicas"] != float64(3) {
		t.Errorf("replicas = %v, want 3", spec["replicas"])
	}
	template, _ := spec["template"].(map[string]any)
	labels, _ := template["labels"].(map[string]any)
	if labels["tier"] != "web" {
		t.Errorf("labels = %v, want tier=web", labels)
	}
	tmplSpec, _ := template["spec"].(map[string]any)
	resources, _ := tmplSpec["resources"].(map[string]any)
	if resources["cpu"] != "4" {
		t.Errorf("template.spec.resources.cpu = %v, want 4", resources["cpu"])
	}
	image, _ := tmplSpec["image"].(map[string]any)
	if image["path"] != "/img.qcow2" {
		t.Errorf("template.spec.image.path = %v, want /img.qcow2", image["path"])
	}
}

func TestCmdCreateInstanceTypePostsExpectedSpec(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdCreateInstanceType(context.Background(), kc, []string{"small", "--cpu", "2", "--memory", "4Gi", "--hugepages"})
	if s.method != http.MethodPost || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machineinstancetypes" {
		t.Fatalf("method=%s path=%s", s.method, s.path)
	}
	spec, _ := s.body["spec"].(map[string]any)
	resources, _ := spec["resources"].(map[string]any)
	if resources["cpu"] != "2" || resources["memory"] != "4Gi" || resources["hugepages"] != true {
		t.Errorf("resources = %v", resources)
	}
}

func TestCmdCreateMigrationPolicyPostsExpectedSpec(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdCreateMigrationPolicy(context.Background(), kc, []string{"fast-tier", "--selector", "tier=fast", "--bandwidth-mbps", "500", "--max-concurrent", "2"})
	if s.method != http.MethodPost || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/migrationpolicies" {
		t.Fatalf("method=%s path=%s", s.method, s.path)
	}
	spec, _ := s.body["spec"].(map[string]any)
	selector, _ := spec["selector"].(map[string]any)
	if selector["tier"] != "fast" {
		t.Errorf("selector = %v", selector)
	}
	if spec["bandwidthMbps"] != float64(500) || spec["maxConcurrent"] != float64(2) {
		t.Errorf("spec = %v", spec)
	}
}

func TestCmdScalePatchesReplicas(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdScale(context.Background(), kc, []string{"machineset", "web", "--replicas", "5"})
	if s.method != http.MethodPatch || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinesets/web" {
		t.Fatalf("method=%s path=%s", s.method, s.path)
	}
	spec, _ := s.body["spec"].(map[string]any)
	if spec["replicas"] != float64(5) {
		t.Errorf("replicas = %v, want 5", spec["replicas"])
	}
}

func TestCmdEditMigrationPolicyOnlyPatchesFlagsActuallySet(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdEdit(context.Background(), kc, []string{"migrationpolicy", "fast-tier", "--max-concurrent", "3"})
	if s.method != http.MethodPatch || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/migrationpolicies/fast-tier" {
		t.Fatalf("method=%s path=%s", s.method, s.path)
	}
	spec, _ := s.body["spec"].(map[string]any)
	if spec["maxConcurrent"] != float64(3) {
		t.Errorf("maxConcurrent = %v, want 3", spec["maxConcurrent"])
	}
	if _, present := spec["bandwidthMbps"]; present {
		t.Errorf("bandwidthMbps should not be present when --bandwidth-mbps wasn't passed, got %v", spec)
	}
}

func TestCmdCreateSnapshotSchedulePostsExpectedSpec(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdCreateSnapshotSchedule(context.Background(), kc, []string{"nightly", "--selector", "tier=web", "--interval-seconds", "3600", "--volume-snapshot-class", "csi-hostpath-snapclass"})
	if s.method != http.MethodPost || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinesnapshotschedules" {
		t.Fatalf("method=%s path=%s", s.method, s.path)
	}
	spec, _ := s.body["spec"].(map[string]any)
	selector, _ := spec["selector"].(map[string]any)
	if selector["tier"] != "web" {
		t.Errorf("selector = %v", selector)
	}
	if spec["intervalSeconds"] != float64(3600) {
		t.Errorf("intervalSeconds = %v, want 3600", spec["intervalSeconds"])
	}
	if spec["volumeSnapshotClassName"] != "csi-hostpath-snapclass" {
		t.Errorf("volumeSnapshotClassName = %v", spec["volumeSnapshotClassName"])
	}
}

func TestCmdEditSnapshotScheduleOnlyPatchesFlagsActuallySet(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdEdit(context.Background(), kc, []string{"snapshotschedule", "nightly", "--suspend", "true"})
	if s.method != http.MethodPatch || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinesnapshotschedules/nightly" {
		t.Fatalf("method=%s path=%s", s.method, s.path)
	}
	spec, _ := s.body["spec"].(map[string]any)
	if spec["suspend"] != true {
		t.Errorf("suspend = %v, want true", spec["suspend"])
	}
	if _, present := spec["intervalSeconds"]; present {
		t.Errorf("intervalSeconds should not be present when --interval-seconds wasn't passed, got %v", spec)
	}
}

// TestCmdCreateStillCreatesAPlainMachine is a regression test: `kaironctl
// create NAME --image ... [flags]` (no KIND argument) must keep creating a
// Machine exactly as it did before create machineset/instancetype/
// migrationpolicy existed.
func TestCmdCreateStillCreatesAPlainMachine(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdCreate(context.Background(), kc, []string{"my-vm", "--image", "/img.qcow2", "--cpu", "4", "--memory", "8Gi"})
	if s.method != http.MethodPost || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines" {
		t.Fatalf("method=%s path=%s", s.method, s.path)
	}
	spec, _ := s.body["spec"].(map[string]any)
	resources, _ := spec["resources"].(map[string]any)
	if resources["cpu"] != "4" || resources["memory"] != "8Gi" {
		t.Errorf("resources = %v", resources)
	}
	metadata, _ := s.body["metadata"].(map[string]any)
	if metadata["name"] != "my-vm" {
		t.Errorf("metadata.name = %v, want my-vm", metadata["name"])
	}
}

// TestCmdCreateDispatchesToMachineSetByKeyword confirms `create machineset
// NAME` is recognized as a KIND, not treated as a Machine named
// "machineset" -- the design tradeoff documented on cmdCreate itself.
func TestCmdCreateDispatchesToMachineSetByKeyword(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdCreate(context.Background(), kc, []string{"machineset", "web", "--image", "/img.qcow2"})
	if s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinesets" {
		t.Fatalf("path=%s, want a machinesets POST (create dispatched to cmdCreateMachineSet)", s.path)
	}
}
