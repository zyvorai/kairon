// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kaironctl

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

// captureStdout runs fn with os.Stdout redirected to an in-memory pipe and
// returns everything fn printed -- describeSnapshotSchedule (unlike every
// verb this file otherwise tests, which is exercised purely via the HTTP
// boundary or a pure function's return value) prints its "Matching
// machines" preview straight to stdout, so this is the only way to assert
// on it directly.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()
	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("closing pipe writer: %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	return buf.String()
}

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
	cmdCreateSnapshotSchedule(context.Background(), kc, []string{"nightly", "--selector", "tier=web", "--interval-seconds", "3600", "--volume-snapshot-class", "csi-hostpath-snapclass", "--keep-last", "3"})
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
	if spec["keepLast"] != float64(3) {
		t.Errorf("keepLast = %v, want 3", spec["keepLast"])
	}
}

// TestCmdCreateSnapshotScheduleOmitsKeepLastByDefault confirms omitting
// --keep-last entirely never sends a keepLast field at all (MachineSnapshotScheduleSpec.KeepLast
// carries `omitempty`, matching Suspend/VolumeSnapshotClassName's own
// zero-value-omitted convention) -- the model's own KeepLast doc comment
// promises the zero value (also the JSON-absent case the apiserver defaults
// to) never prunes.
func TestCmdCreateSnapshotScheduleOmitsKeepLastByDefault(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdCreateSnapshotSchedule(context.Background(), kc, []string{"nightly", "--selector", "tier=web", "--interval-seconds", "3600"})
	spec, _ := s.body["spec"].(map[string]any)
	if _, present := spec["keepLast"]; present {
		t.Errorf("keepLast should not be present when --keep-last is omitted, got %v", spec)
	}
}

// TestCmdCreateSnapshotScheduleStartingDeadlineSeconds confirms
// --starting-deadline-seconds is wired through to
// spec.startingDeadlineSeconds, and (mirroring
// TestCmdCreateSnapshotScheduleOmitsKeepLastByDefault) that omitting the
// flag never sends the field at all, since MachineSnapshotScheduleSpec.
// StartingDeadlineSeconds carries `omitempty` and 0 is meant to mean
// "no deadline," matching every schedule's behavior before this field
// existed.
func TestCmdCreateSnapshotScheduleStartingDeadlineSeconds(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdCreateSnapshotSchedule(context.Background(), kc, []string{"nightly", "--selector", "tier=web", "--interval-seconds", "3600", "--starting-deadline-seconds", "300"})
	spec, _ := s.body["spec"].(map[string]any)
	if spec["startingDeadlineSeconds"] != float64(300) {
		t.Errorf("startingDeadlineSeconds = %v, want 300", spec["startingDeadlineSeconds"])
	}

	s2 := &recordingServer{}
	kc2 := testClient(t, s2)
	cmdCreateSnapshotSchedule(context.Background(), kc2, []string{"nightly", "--selector", "tier=web", "--interval-seconds", "3600"})
	spec2, _ := s2.body["spec"].(map[string]any)
	if _, present := spec2["startingDeadlineSeconds"]; present {
		t.Errorf("startingDeadlineSeconds should not be present when --starting-deadline-seconds is omitted, got %v", spec2)
	}
}

func TestCmdEditSnapshotScheduleStartingDeadlineSeconds(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdEdit(context.Background(), kc, []string{"snapshotschedule", "nightly", "--starting-deadline-seconds", "120"})
	spec, _ := s.body["spec"].(map[string]any)
	if spec["startingDeadlineSeconds"] != float64(120) {
		t.Errorf("startingDeadlineSeconds = %v, want 120", spec["startingDeadlineSeconds"])
	}
	if _, present := spec["suspend"]; present {
		t.Errorf("suspend should not be present when --suspend wasn't passed, got %v", spec)
	}
	if _, present := spec["keepLast"]; present {
		t.Errorf("keepLast should not be present when --keep-last wasn't passed, got %v", spec)
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
	if _, present := spec["keepLast"]; present {
		t.Errorf("keepLast should not be present when --keep-last wasn't passed, got %v", spec)
	}
}

func TestCmdEditSnapshotScheduleKeepLast(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdEdit(context.Background(), kc, []string{"snapshotschedule", "nightly", "--keep-last", "5"})
	spec, _ := s.body["spec"].(map[string]any)
	if spec["keepLast"] != float64(5) {
		t.Errorf("keepLast = %v, want 5", spec["keepLast"])
	}
	if _, present := spec["suspend"]; present {
		t.Errorf("suspend should not be present when --suspend wasn't passed, got %v", spec)
	}
}

func TestCmdCreateQuotaPostsExpectedSpec(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdCreateQuota(context.Background(), kc, []string{"team-payments", "--max-machines", "20", "--max-total-cpu", "40", "--max-total-memory", "160Gi"})
	if s.method != http.MethodPost || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinequotas" {
		t.Fatalf("method=%s path=%s", s.method, s.path)
	}
	spec, _ := s.body["spec"].(map[string]any)
	if spec["maxMachines"] != float64(20) {
		t.Errorf("maxMachines = %v, want 20", spec["maxMachines"])
	}
	if spec["maxTotalCpu"] != "40" || spec["maxTotalMemory"] != "160Gi" {
		t.Errorf("spec = %v", spec)
	}
}

// TestCmdCreateQuotaOmitsMaxMachinesWhenNotPassed confirms omitting
// --max-machines entirely leaves spec.maxMachines absent (nil, matching its
// own *int/omitempty "no cap on this dimension" contract) rather than
// sending a spurious 0 -- 0 is itself a meaningful, valid cap (zero
// Machines allowed), so the zero value can't double as "unset" the way a
// plain int flag can elsewhere in this file.
func TestCmdCreateQuotaOmitsMaxMachinesWhenNotPassed(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdCreateQuota(context.Background(), kc, []string{"team-payments", "--max-total-cpu", "40"})
	spec, _ := s.body["spec"].(map[string]any)
	if _, present := spec["maxMachines"]; present {
		t.Errorf("maxMachines should not be present when --max-machines is omitted, got %v", spec)
	}
}

func TestCmdEditQuotaOnlyPatchesFlagsActuallySet(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdEdit(context.Background(), kc, []string{"quota", "team-payments", "--max-machines", "30"})
	if s.method != http.MethodPatch || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinequotas/team-payments" {
		t.Fatalf("method=%s path=%s", s.method, s.path)
	}
	spec, _ := s.body["spec"].(map[string]any)
	if spec["maxMachines"] != float64(30) {
		t.Errorf("maxMachines = %v, want 30", spec["maxMachines"])
	}
	if _, present := spec["maxTotalCpu"]; present {
		t.Errorf("maxTotalCpu should not be present when --max-total-cpu wasn't passed, got %v", spec)
	}
}

func TestCmdCreateBudgetMinAvailablePostsExpectedSpec(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdCreateBudget(context.Background(), kc, []string{"web-tier", "--selector", "tier=web", "--min-available", "2"})
	if s.method != http.MethodPost || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinedisruptionbudgets" {
		t.Fatalf("method=%s path=%s", s.method, s.path)
	}
	spec, _ := s.body["spec"].(map[string]any)
	selector, _ := spec["selector"].(map[string]any)
	if selector["tier"] != "web" {
		t.Errorf("selector = %v", selector)
	}
	if spec["minAvailable"] != "2" {
		t.Errorf("minAvailable = %v, want 2", spec["minAvailable"])
	}
	if _, present := spec["maxUnavailable"]; present {
		t.Errorf("maxUnavailable should not be present when --max-unavailable wasn't passed, got %v", spec)
	}
}

func TestCmdCreateBudgetMaxUnavailablePostsExpectedSpec(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdCreateBudget(context.Background(), kc, []string{"web-tier", "--selector", "tier=web", "--max-unavailable", "1"})
	spec, _ := s.body["spec"].(map[string]any)
	if spec["maxUnavailable"] != "1" {
		t.Errorf("maxUnavailable = %v, want 1", spec["maxUnavailable"])
	}
	if _, present := spec["minAvailable"]; present {
		t.Errorf("minAvailable should not be present when --min-available wasn't passed, got %v", spec)
	}
}

func TestCmdEditBudgetOnlyPatchesFlagsActuallySet(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdEdit(context.Background(), kc, []string{"budget", "web-tier", "--min-available", "3"})
	if s.method != http.MethodPatch || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinedisruptionbudgets/web-tier" {
		t.Fatalf("method=%s path=%s", s.method, s.path)
	}
	spec, _ := s.body["spec"].(map[string]any)
	if spec["minAvailable"] != "3" {
		t.Errorf("minAvailable = %v, want 3", spec["minAvailable"])
	}
	if _, present := spec["selector"]; present {
		t.Errorf("selector should not be present when --selector wasn't passed, got %v", spec)
	}
	if _, present := spec["maxUnavailable"]; present {
		t.Errorf("maxUnavailable should not be present when --max-unavailable wasn't passed, got %v", spec)
	}
}

func TestCmdEditBudgetSelectorReplacesWholeMap(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdEdit(context.Background(), kc, []string{"budget", "web-tier", "--selector", "tier=web", "--selector", "env=prod"})
	spec, _ := s.body["spec"].(map[string]any)
	selector, _ := spec["selector"].(map[string]any)
	if selector["tier"] != "web" || selector["env"] != "prod" || len(selector) != 2 {
		t.Errorf("selector = %v", selector)
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

func TestCmdCreatePriorityFlag(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdCreate(context.Background(), kc, []string{"urgent-vm", "--image", "/img.qcow2", "--priority", "10"})
	spec, _ := s.body["spec"].(map[string]any)
	if spec["priority"] != float64(10) {
		t.Errorf("priority = %v, want 10", spec["priority"])
	}
}

// TestCmdCreateOmitsPriorityByDefault confirms an unset --priority produces
// no "priority" key at all (MachineSpec.Priority's own omitempty), not an
// explicit 0 -- matching every other create verb's "unset means the API's
// own zero-value default, not an explicit override" convention.
func TestCmdCreateOmitsPriorityByDefault(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdCreate(context.Background(), kc, []string{"plain-vm", "--image", "/img.qcow2"})
	spec, _ := s.body["spec"].(map[string]any)
	if _, present := spec["priority"]; present {
		t.Errorf("priority should be omitted by default, got %v", spec)
	}
}

// TestCmdCreateMachineSetPropagatesPriority confirms --priority flows
// through machineSpecFromFlags into a MachineSet's per-replica template,
// exactly like --cpu/--image already do -- every replica a MachineSet
// creates competes for scarce capacity at the same priority as the
// MachineSet itself was asked to run at.
func TestCmdCreateMachineSetPropagatesPriority(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdCreateMachineSet(context.Background(), kc, []string{"web", "--image", "/img.qcow2", "--priority", "7"})
	spec, _ := s.body["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	tmplSpec, _ := template["spec"].(map[string]any)
	if tmplSpec["priority"] != float64(7) {
		t.Errorf("template.spec.priority = %v, want 7", tmplSpec["priority"])
	}
}

func TestCmdEditMachinePriority(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdEdit(context.Background(), kc, []string{"machine", "urgent-vm", "--priority", "20"})
	if s.method != http.MethodPatch || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines/urgent-vm" {
		t.Fatalf("method=%s path=%s", s.method, s.path)
	}
	spec, _ := s.body["spec"].(map[string]any)
	if spec["priority"] != float64(20) {
		t.Errorf("priority = %v, want 20", spec["priority"])
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

// TestFormatNextRun exercises `kaironctl get snapshotschedules`' NEXTRUN
// column logic directly. The "suspended after a prior fire" case is the one
// that matters most: it confirms formatNextRun trusts the live spec.suspend
// flag over a stale, already-computed Status.NextRunTime that was only ever
// projected as of the schedule's last actual run (see
// MachineSnapshotScheduleStatus.NextRunTime's own doc comment) -- otherwise
// a suspended schedule could show an already-passed timestamp as if a run
// were still pending.
func TestFormatNextRun(t *testing.T) {
	fired := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	nextRun := fired.Add(time.Hour)
	tests := []struct {
		name string
		sch  model.MachineSnapshotSchedule
		want string
	}{
		{
			name: "never yet run shows pending",
			sch:  model.MachineSnapshotSchedule{Spec: model.MachineSnapshotScheduleSpec{IntervalSeconds: 3600}},
			want: "pending",
		},
		{
			name: "fired once shows the projected next run",
			sch: model.MachineSnapshotSchedule{
				Spec:   model.MachineSnapshotScheduleSpec{IntervalSeconds: 3600},
				Status: model.MachineSnapshotScheduleStatus{LastRunTime: fired, NextRunTime: nextRun},
			},
			want: nextRun.Format(time.RFC3339),
		},
		{
			name: "suspended after a prior fire shows suspended, not the stale projection",
			sch: model.MachineSnapshotSchedule{
				Spec:   model.MachineSnapshotScheduleSpec{IntervalSeconds: 3600, Suspend: true},
				Status: model.MachineSnapshotScheduleStatus{LastRunTime: fired, NextRunTime: nextRun},
			},
			want: "suspended",
		},
		{
			name: "suspended and never run shows suspended",
			sch:  model.MachineSnapshotSchedule{Spec: model.MachineSnapshotScheduleSpec{IntervalSeconds: 3600, Suspend: true}},
			want: "suspended",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatNextRun(tc.sch); got != tc.want {
				t.Errorf("formatNextRun() = %q, want %q", got, tc.want)
			}
		})
	}
}

// describeScheduleTestServer is a minimal in-memory fake of the two
// endpoints describeSnapshotSchedule calls, mirroring
// evacuateTestServer's own inline-httptest-server convention above.
type describeScheduleTestServer struct {
	schedule model.MachineSnapshotSchedule
	machines []model.Machine
}

func (s *describeScheduleTestServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinesnapshotschedules/"+s.schedule.Metadata.Name:
			_ = json.NewEncoder(w).Encode(s.schedule)
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: s.machines})
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
}

// TestDescribeSnapshotScheduleShowsMatchingMachinesWhenDue confirms the
// "what would fire right now" preview: only Machines matching
// spec.selector are listed, printed sorted rather than in arbitrary
// listing order, and a never-yet-run schedule (immediately Due, per
// Spec.Due's own doc comment) is reported as "due now".
func TestDescribeSnapshotScheduleShowsMatchingMachinesWhenDue(t *testing.T) {
	sched := model.MachineSnapshotSchedule{
		Metadata: model.ObjectMeta{Name: "nightly", Namespace: "default"},
		Spec:     model.MachineSnapshotScheduleSpec{Selector: map[string]string{"tier": "web"}, IntervalSeconds: 3600},
	}
	s := &describeScheduleTestServer{
		schedule: sched,
		machines: []model.Machine{
			{Metadata: model.ObjectMeta{Name: "vm-2", Namespace: "default", Labels: map[string]string{"tier": "web"}}},
			{Metadata: model.ObjectMeta{Name: "vm-1", Namespace: "default", Labels: map[string]string{"tier": "web"}}},
			{Metadata: model.ObjectMeta{Name: "vm-db", Namespace: "default", Labels: map[string]string{"tier": "db"}}},
		},
	}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	kc.HTTP = srv.Client()

	out := captureStdout(t, func() {
		describeSnapshotSchedule(context.Background(), kc, "default", "nightly")
	})
	if !strings.Contains(out, "due now") {
		t.Errorf("expected \"due now\" in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Matching machines (2)") {
		t.Errorf("expected 2 matches, got:\n%s", out)
	}
	if i1, i2 := strings.Index(out, "vm-1"), strings.Index(out, "vm-2"); i1 == -1 || i2 == -1 || i1 > i2 {
		t.Errorf("expected vm-1 listed before vm-2 (sorted), got:\n%s", out)
	}
	if strings.Contains(out, "vm-db") {
		t.Errorf("vm-db doesn't match spec.selector, must not appear:\n%s", out)
	}
}

// TestDescribeSnapshotScheduleNotDueYetShowsProjectedNextRun confirms a
// schedule that ran recently (well inside its own interval) is reported
// as not due, alongside its projected next run, and a selector matching
// zero Machines prints the explicit "(none ...)" hint rather than an
// empty, unexplained list.
func TestDescribeSnapshotScheduleNotDueYetShowsProjectedNextRun(t *testing.T) {
	lastRun := time.Now().Add(-time.Minute)
	sched := model.MachineSnapshotSchedule{
		Metadata: model.ObjectMeta{Name: "nightly", Namespace: "default"},
		Spec:     model.MachineSnapshotScheduleSpec{Selector: map[string]string{"tier": "web"}, IntervalSeconds: 3600},
		Status:   model.MachineSnapshotScheduleStatus{LastRunTime: lastRun, NextRunTime: lastRun.Add(time.Hour)},
	}
	s := &describeScheduleTestServer{schedule: sched}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	kc.HTTP = srv.Client()

	out := captureStdout(t, func() {
		describeSnapshotSchedule(context.Background(), kc, "default", "nightly")
	})
	if !strings.Contains(out, "not due yet") {
		t.Errorf("expected \"not due yet\" in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Matching machines (0)") {
		t.Errorf("expected 0 matches, got:\n%s", out)
	}
	if !strings.Contains(out, "(none") {
		t.Errorf("expected the no-matches hint, got:\n%s", out)
	}
}

// TestDescribeSnapshotScheduleDueButDeadlineExceededShowsSkipped confirms
// the third preview outcome: a schedule that IS due (an interval has
// elapsed) but whose window is already past its own
// spec.startingDeadlineSeconds is reported as "will be SKIPPED", distinct
// from both "due now" (would actually fire) and "not due yet".
func TestDescribeSnapshotScheduleDueButDeadlineExceededShowsSkipped(t *testing.T) {
	sched := model.MachineSnapshotSchedule{
		Metadata: model.ObjectMeta{Name: "nightly", Namespace: "default"},
		Spec: model.MachineSnapshotScheduleSpec{
			Selector:                map[string]string{"tier": "web"},
			IntervalSeconds:         60,
			StartingDeadlineSeconds: 60,
		},
		// Due 24h ago and still never caught up -- far past any 60s deadline.
		Status: model.MachineSnapshotScheduleStatus{LastRunTime: time.Now().Add(-24 * time.Hour)},
	}
	s := &describeScheduleTestServer{
		schedule: sched,
		machines: []model.Machine{
			{Metadata: model.ObjectMeta{Name: "vm-1", Namespace: "default", Labels: map[string]string{"tier": "web"}}},
		},
	}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc, err := kube.New(srv.URL, "", "", false)
	if err != nil {
		t.Fatalf("kube.New: %v", err)
	}
	kc.HTTP = srv.Client()

	out := captureStdout(t, func() {
		describeSnapshotSchedule(context.Background(), kc, "default", "nightly")
	})
	if !strings.Contains(out, "will be SKIPPED") {
		t.Errorf("expected the SKIPPED outcome, got:\n%s", out)
	}
	if strings.Contains(out, "due now, the next reconcile") {
		t.Errorf("must not also report the normal due-now outcome, got:\n%s", out)
	}
}
