// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kaironctl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
		"Cancelled": false,
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

// TestSelectorFilter unit-tests selectorFilter directly (no HTTP fixture
// needed, unlike the cmdGet-level tests below): a nil/empty selector must
// be a true no-op returning items unchanged, a non-empty selector must
// keep only items whose labels satisfy every pair (model.LabelsMatch's own
// AND semantics), and the result must never alias the input slice's
// backing array.
func TestSelectorFilter(t *testing.T) {
	items := []model.Machine{
		labeledMachine("web-1", map[string]string{"tier": "web"}),
		labeledMachine("db-1", map[string]string{"tier": "db"}),
		labeledMachine("web-2", map[string]string{"tier": "web", "env": "prod"}),
	}
	labels := func(m model.Machine) map[string]string { return m.Metadata.Labels }

	if got := selectorFilter(items, nil, labels); len(got) != len(items) {
		t.Fatalf("nil selector: got %d items, want all %d unchanged", len(got), len(items))
	}

	got := selectorFilter(items, map[string]string{"tier": "web"}, labels)
	var names []string
	for _, m := range got {
		names = append(names, m.Metadata.Name)
	}
	if !equalStrings(names, []string{"web-1", "web-2"}) {
		t.Errorf("tier=web: got %v, want [web-1 web-2]", names)
	}

	got = selectorFilter(items, map[string]string{"tier": "web", "env": "prod"}, labels)
	if len(got) != 1 || got[0].Metadata.Name != "web-2" {
		t.Errorf("tier=web,env=prod: got %v, want just web-2", got)
	}

	if got := selectorFilter(items, map[string]string{"tier": "staging"}, labels); len(got) != 0 {
		t.Errorf("non-matching selector: got %v, want empty", got)
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

// TestCmdGetDescribeDeleteNetworkPolicyAndSecurityGroup covers the one
// piece of kaironctl wiring that was entirely missing for these two CRDs
// before this change -- get/describe/delete, exactly what every other
// kind already has (see resourceKindAndName's doc comment for why KIND is
// always the first of two positional args here).
func TestCmdGetDescribeDeleteNetworkPolicyAndSecurityGroup(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	ctx := context.Background()

	cmdGet(ctx, kc, []string{"networkpolicies"})
	if s.method != http.MethodGet || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinenetworkpolicies" {
		t.Fatalf("get networkpolicies: method=%s path=%s", s.method, s.path)
	}
	cmdGet(ctx, kc, []string{"securitygroups"})
	if s.method != http.MethodGet || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/networksecuritygroups" {
		t.Fatalf("get securitygroups: method=%s path=%s", s.method, s.path)
	}

	cmdDescribe(ctx, kc, []string{"networkpolicy", "web-edge"})
	if s.method != http.MethodGet || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinenetworkpolicies/web-edge" {
		t.Fatalf("describe networkpolicy: method=%s path=%s", s.method, s.path)
	}
	cmdDescribe(ctx, kc, []string{"securitygroup", "frontend"})
	if s.method != http.MethodGet || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/networksecuritygroups/frontend" {
		t.Fatalf("describe securitygroup: method=%s path=%s", s.method, s.path)
	}

	cmdDelete(ctx, kc, []string{"networkpolicy", "web-edge"})
	if s.method != http.MethodDelete || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinenetworkpolicies/web-edge" {
		t.Fatalf("delete networkpolicy: method=%s path=%s", s.method, s.path)
	}
	cmdDelete(ctx, kc, []string{"securitygroup", "frontend"})
	if s.method != http.MethodDelete || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/networksecuritygroups/frontend" {
		t.Fatalf("delete securitygroup: method=%s path=%s", s.method, s.path)
	}
}

// cancelMigrationTestServer is a minimal fake standing in for the GET-then-
// PATCH sequence cmdCancelMigration performs -- recordingServer alone can't
// cover this command, since its blanket GET response (an empty decoded
// body) would never satisfy cmdCancelMigration's own Starting/Running,
// live-strategy guard and would exit the test process via fatal().
type cancelMigrationTestServer struct {
	migration model.MachineMigration
	patchBody map[string]any
}

func (s *cancelMigrationTestServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinemigrations/move-db":
			_ = json.NewEncoder(w).Encode(s.migration)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinemigrations/move-db":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.patchBody = body
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
}

func TestCmdCancelMigrationPatchesSpecCancel(t *testing.T) {
	s := &cancelMigrationTestServer{migration: model.MachineMigration{
		Metadata: model.ObjectMeta{Name: "move-db", Namespace: "default"},
		Status:   model.MachineMigrationStatus{Phase: "Running", EffectiveStrategy: "live"},
	}}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc := &kube.Client{BaseURL: srv.URL, HTTP: srv.Client()}

	cmdCancelMigration(context.Background(), kc, []string{"move-db"})

	spec, _ := s.patchBody["spec"].(map[string]any)
	if spec["cancel"] != true {
		t.Fatalf("expected a spec.cancel=true patch, got body %v", s.patchBody)
	}
}

// TestCmdCancelMigrationAlreadyRequestedSkipsPatch proves cmdCancelMigration
// is idempotent from the operator's point of view: re-running it against a
// migration that already has spec.cancel set prints a status message
// instead of sending a redundant PATCH.
func TestCmdCancelMigrationAlreadyRequestedSkipsPatch(t *testing.T) {
	s := &cancelMigrationTestServer{migration: model.MachineMigration{
		Metadata: model.ObjectMeta{Name: "move-db", Namespace: "default"},
		Spec:     model.MachineMigrationSpec{Cancel: true},
		Status:   model.MachineMigrationStatus{Phase: "Running", EffectiveStrategy: "live"},
	}}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc := &kube.Client{BaseURL: srv.URL, HTTP: srv.Client()}

	captureStdout(t, func() { cmdCancelMigration(context.Background(), kc, []string{"move-db"}) })

	if s.patchBody != nil {
		t.Fatalf("expected no PATCH when cancel was already requested, got %v", s.patchBody)
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

// TestCmdCreateNetworkPolicyPostsExpectedSpec covers the create half of the
// CRUD gap TestCmdGetDescribeDeleteNetworkPolicyAndSecurityGroup's own doc
// comment describes: get/describe/delete existed for MachineNetworkPolicy
// before this change, but create/edit did not.
func TestCmdCreateNetworkPolicyPostsExpectedSpec(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdCreateNetworkPolicy(context.Background(), kc, []string{
		"web-edge", "--selector", "app=web",
		"--allow-cidr", "10.0.0.0/8", "--allow-port", "tcp/443",
		"--policy-group", "frontend", "--max-egress-mbps", "250", "--audit-mode",
	})
	if s.method != http.MethodPost || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinenetworkpolicies" {
		t.Fatalf("method=%s path=%s", s.method, s.path)
	}
	spec, _ := s.body["spec"].(map[string]any)
	selector, _ := spec["selector"].(map[string]any)
	if selector["app"] != "web" {
		t.Errorf("selector = %v", selector)
	}
	policy, _ := spec["policy"].(map[string]any)
	allowCidrs, _ := policy["allowCidrs"].([]any)
	if len(allowCidrs) != 1 || allowCidrs[0] != "10.0.0.0/8" {
		t.Errorf("allowCidrs = %v", policy["allowCidrs"])
	}
	if policy["maxEgressMbps"] != float64(250) {
		t.Errorf("maxEgressMbps = %v", policy["maxEgressMbps"])
	}
	if policy["auditMode"] != true {
		t.Errorf("auditMode = %v, want true", policy["auditMode"])
	}
}

// TestCmdCreateSecurityGroupPostsExpectedSpec covers the create half of the
// CRUD gap for NetworkSecurityGroup, the exact counterpart of the
// MachineNetworkPolicy test above.
func TestCmdCreateSecurityGroupPostsExpectedSpec(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdCreateSecurityGroup(context.Background(), kc, []string{
		"frontend", "--group-label", "tier=frontend", "--priority", "100",
		"--description", "HTTPS egress", "--allow-cidr", "10.0.0.0/8", "--allow-icmp",
	})
	if s.method != http.MethodPost || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/networksecuritygroups" {
		t.Fatalf("method=%s path=%s", s.method, s.path)
	}
	spec, _ := s.body["spec"].(map[string]any)
	labels, _ := spec["labels"].([]any)
	if len(labels) != 1 || labels[0] != "tier=frontend" {
		t.Errorf("labels = %v", spec["labels"])
	}
	if spec["priority"] != float64(100) || spec["description"] != "HTTPS egress" {
		t.Errorf("spec = %v", spec)
	}
	policy, _ := spec["policy"].(map[string]any)
	if policy["allowIcmp"] != true {
		t.Errorf("allowIcmp = %v, want true", policy["allowIcmp"])
	}
}

// TestCmdEditNetworkPolicyOnlyPatchesFlagsActuallySet mirrors
// TestCmdEditBudgetOnlyPatchesFlagsActuallySet's own convention: an
// untouched field must stay entirely absent from the merge-patch body, not
// present with a zero value, so an omitted flag can never clobber an
// already-set value.
func TestCmdEditNetworkPolicyOnlyPatchesFlagsActuallySet(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdEdit(context.Background(), kc, []string{"networkpolicy", "web-edge", "--audit-mode"})
	if s.method != http.MethodPatch || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinenetworkpolicies/web-edge" {
		t.Fatalf("method=%s path=%s", s.method, s.path)
	}
	spec, _ := s.body["spec"].(map[string]any)
	if _, present := spec["machineName"]; present {
		t.Errorf("machineName should not be present when --machine-name wasn't passed, got %v", spec)
	}
	if _, present := spec["selector"]; present {
		t.Errorf("selector should not be present when --selector wasn't passed, got %v", spec)
	}
	policy, _ := spec["policy"].(map[string]any)
	if policy["auditMode"] != true {
		t.Errorf("policy.auditMode = %v, want true", policy["auditMode"])
	}
	if _, present := policy["allowCidrs"]; present {
		t.Errorf("policy.allowCidrs should not be present when --allow-cidr wasn't passed, got %v", policy)
	}
}

// TestCmdEditSecurityGroupOnlyPatchesFlagsActuallySet is
// TestCmdEditNetworkPolicyOnlyPatchesFlagsActuallySet's exact counterpart
// for NetworkSecurityGroup's own top-level spec fields.
func TestCmdEditSecurityGroupOnlyPatchesFlagsActuallySet(t *testing.T) {
	s := &recordingServer{}
	kc := testClient(t, s)
	cmdEdit(context.Background(), kc, []string{"securitygroup", "frontend", "--priority", "50"})
	if s.method != http.MethodPatch || s.path != "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/networksecuritygroups/frontend" {
		t.Fatalf("method=%s path=%s", s.method, s.path)
	}
	spec, _ := s.body["spec"].(map[string]any)
	if spec["priority"] != float64(50) {
		t.Errorf("priority = %v, want 50", spec["priority"])
	}
	if _, present := spec["labels"]; present {
		t.Errorf("labels should not be present when --group-label wasn't passed, got %v", spec)
	}
	if _, present := spec["policy"]; present {
		t.Errorf("policy should not be present when no policy flag was passed, got %v", spec)
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

// describeMigrationPolicyTestServer is a minimal in-memory fake of the four
// endpoints describeMigrationPolicy calls, mirroring
// describeScheduleTestServer's own convention above. policies/migrations
// are served for every MigrationPolicy/MachineMigration in the namespace
// (not just the one named in the URL), matching what
// ListMigrationPoliciesNamespace/ListMachineMigrationsNamespace actually
// return.
type describeMigrationPolicyTestServer struct {
	policies   []model.MigrationPolicy
	machines   []model.Machine
	migrations []model.MachineMigration
}

func (s *describeMigrationPolicyTestServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: s.machines})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinemigrations":
			_ = json.NewEncoder(w).Encode(model.MachineMigrationList{Items: s.migrations})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/migrationpolicies":
			_ = json.NewEncoder(w).Encode(model.MigrationPolicyList{Items: s.policies})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/migrationpolicies/"):
			name := strings.TrimPrefix(r.URL.Path, "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/migrationpolicies/")
			for _, p := range s.policies {
				if p.Metadata.Name == name {
					_ = json.NewEncoder(w).Encode(p)
					return
				}
			}
			http.Error(w, "not found", http.StatusNotFound)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
}

// TestDescribeMigrationPolicyShowsAdmissionAndBandwidth confirms the
// preview's two per-machine decisions: only Machines matching
// spec.selector are listed (sorted by name), status.activeMigrations/
// spec.maxConcurrent is reported up front, matching bandwidthMbps is
// applied, and once MaxConcurrent's shared cap is spent by an earlier
// Machine in the walk, every later match correctly reports BLOCKED --
// exactly what a real sequential batch of migration creations would hit.
func TestDescribeMigrationPolicyShowsAdmissionAndBandwidth(t *testing.T) {
	policy := model.MigrationPolicy{
		Metadata: model.ObjectMeta{Name: "web-policy", Namespace: "default"},
		Spec:     model.MigrationPolicySpec{Selector: map[string]string{"tier": "web"}, BandwidthMbps: 500, MaxConcurrent: 1},
	}
	s := &describeMigrationPolicyTestServer{
		policies: []model.MigrationPolicy{policy},
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
		describeMigrationPolicy(context.Background(), kc, "default", "web-policy")
	})
	if !strings.Contains(out, "Matching machines (2)") {
		t.Errorf("expected 2 matches, got:\n%s", out)
	}
	if !strings.Contains(out, "status.activeMigrations 0/1") {
		t.Errorf("expected the active/max header, got:\n%s", out)
	}
	if strings.Contains(out, "vm-db") {
		t.Errorf("vm-db doesn't match spec.selector, must not appear:\n%s", out)
	}
	i1, i2 := strings.Index(out, "vm-1"), strings.Index(out, "vm-2")
	if i1 == -1 || i2 == -1 || i1 > i2 {
		t.Fatalf("expected vm-1 listed before vm-2 (sorted), got:\n%s", out)
	}
	vm1Line := out[i1:i2]
	if !strings.Contains(vm1Line, "admitted") || !strings.Contains(vm1Line, "500 Mbps (this policy)") {
		t.Errorf("expected vm-1 admitted with this policy's bandwidth, got:\n%s", vm1Line)
	}
	vm2Line := out[i2:]
	if !strings.Contains(vm2Line, "BLOCKED") {
		t.Errorf("expected vm-2 BLOCKED once MaxConcurrent=1 is spent by vm-1, got:\n%s", vm2Line)
	}
}

// TestDescribeMigrationPolicyBandwidthFromOtherPolicy confirms that when an
// earlier (list-order) overlapping MigrationPolicy also matches a Machine
// and sets its own spec.bandwidthMbps, the preview attributes the winning
// value to THAT policy by name, not to the policy being described.
func TestDescribeMigrationPolicyBandwidthFromOtherPolicy(t *testing.T) {
	earlier := model.MigrationPolicy{
		Metadata: model.ObjectMeta{Name: "policy-a", Namespace: "default"},
		Spec:     model.MigrationPolicySpec{Selector: map[string]string{"tier": "web"}, BandwidthMbps: 200},
	}
	describing := model.MigrationPolicy{
		Metadata: model.ObjectMeta{Name: "policy-b", Namespace: "default"},
		Spec:     model.MigrationPolicySpec{Selector: map[string]string{"tier": "web"}, BandwidthMbps: 900},
	}
	s := &describeMigrationPolicyTestServer{
		policies: []model.MigrationPolicy{earlier, describing}, // list order matters: earlier wins
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
		describeMigrationPolicy(context.Background(), kc, "default", "policy-b")
	})
	if !strings.Contains(out, `200 Mbps (from "policy-a", an earlier-matching MigrationPolicy, not this one)`) {
		t.Errorf("expected policy-a's bandwidth attributed to policy-a, got:\n%s", out)
	}
	if strings.Contains(out, "900 Mbps") {
		t.Errorf("policy-b's own bandwidth must not win when policy-a matches first, got:\n%s", out)
	}
}

func TestHasFlag(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"machine"}, false},
		{[]string{"machine", "my-vm"}, false},
		{[]string{"machine", "--selector", "tier=web"}, true},
		{[]string{"machine", "--selector=tier=web"}, true},
		{[]string{"machine", "--dry-run", "--selector", "tier=web"}, true},
		// "--selectorish" must not false-positive on a "--selector" prefix
		// match -- hasFlag requires the flag name to end exactly at "="
		// or the argument boundary.
		{[]string{"machine", "--selectorish", "x"}, false},
	}
	for _, c := range cases {
		if got := hasFlag(c.args, "selector"); got != c.want {
			t.Errorf("hasFlag(%v, %q) = %v, want %v", c.args, "selector", got, c.want)
		}
	}
}

func TestCanonicalKind(t *testing.T) {
	for _, alias := range []string{"machine", "machines", "vm", "vms"} {
		got, err := canonicalKind(alias)
		if err != nil || got != "machine" {
			t.Errorf("canonicalKind(%q) = %q, %v, want machine, nil", alias, got, err)
		}
	}
	for _, alias := range []string{"securitygroup", "securitygroups", "networksecuritygroups"} {
		got, err := canonicalKind(alias)
		if err != nil || got != "securitygroup" {
			t.Errorf("canonicalKind(%q) = %q, %v, want securitygroup, nil", alias, got, err)
		}
	}
	if _, err := canonicalKind("bogus"); err == nil {
		t.Fatal("expected an error for an unrecognized resource kind")
	}
}

// deleteSelectorTestServer is a minimal in-memory fake covering exactly the
// endpoints cmdDeleteSelector's bulk-delete path needs end-to-end: a
// namespaced list per kind (matchingNames) and a per-name DELETE
// (deleteByKindName) -- mirroring the inline-httptest-server convention
// evacuateTestServer above already uses.
type deleteSelectorTestServer struct {
	machines    []model.Machine
	machineSets []model.MachineSet
	deleted     []string // e.g. "machine:vm-1", recorded in request order
}

func (s *deleteSelectorTestServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: s.machines})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinesets":
			_ = json.NewEncoder(w).Encode(model.MachineSetList{Items: s.machineSets})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines/"):
			name := strings.TrimPrefix(r.URL.Path, "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines/")
			s.deleted = append(s.deleted, "machine:"+name)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinesets/"):
			name := strings.TrimPrefix(r.URL.Path, "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinesets/")
			s.deleted = append(s.deleted, "machineset:"+name)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
}

func labeledMachine(name string, labels map[string]string) model.Machine {
	return model.Machine{Metadata: model.ObjectMeta{Name: name, Namespace: "default", Labels: labels}}
}

// TestCmdDeleteSelectorDeletesOnlyMatchingMachines is the core bulk-delete
// happy path: three Machines, two labeled tier=web, one tier=db, and
// `delete machine --selector tier=web` must delete exactly the two web
// ones (in deterministic, sorted order) and leave the db one alone.
func TestCmdDeleteSelectorDeletesOnlyMatchingMachines(t *testing.T) {
	s := &deleteSelectorTestServer{machines: []model.Machine{
		labeledMachine("web-2", map[string]string{"tier": "web"}),
		labeledMachine("db-1", map[string]string{"tier": "db"}),
		labeledMachine("web-1", map[string]string{"tier": "web"}),
	}}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc := &kube.Client{BaseURL: srv.URL, HTTP: srv.Client()}

	out := captureStdout(t, func() {
		cmdDelete(context.Background(), kc, []string{"machine", "--selector", "tier=web"})
	})
	if got, want := s.deleted, []string{"machine:web-1", "machine:web-2"}; !equalStrings(got, want) {
		t.Fatalf("deleted = %v, want %v (sorted, web-only)", got, want)
	}
	if !strings.Contains(out, "machine/web-1 deleted") || !strings.Contains(out, "machine/web-2 deleted") {
		t.Errorf("expected both deletions reported, got:\n%s", out)
	}
	if strings.Contains(out, "db-1") {
		t.Errorf("must not mention the non-matching db-1 machine, got:\n%s", out)
	}
}

// TestCmdDeleteSelectorDryRunDeletesNothing asserts --dry-run's entire
// point: the selector still resolves against the live list, but no DELETE
// request is ever sent.
func TestCmdDeleteSelectorDryRunDeletesNothing(t *testing.T) {
	s := &deleteSelectorTestServer{machines: []model.Machine{
		labeledMachine("web-1", map[string]string{"tier": "web"}),
	}}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc := &kube.Client{BaseURL: srv.URL, HTTP: srv.Client()}

	out := captureStdout(t, func() {
		cmdDelete(context.Background(), kc, []string{"machine", "--selector", "tier=web", "--dry-run"})
	})
	if len(s.deleted) != 0 {
		t.Fatalf("--dry-run must not delete anything, got deleted=%v", s.deleted)
	}
	if !strings.Contains(out, "machine/web-1 (dry-run, not deleted)") {
		t.Errorf("expected a dry-run preview line, got:\n%s", out)
	}
}

// TestCmdDeleteSelectorNoMatchesDeletesNothing covers a selector that
// matches no existing Machine: no DELETE calls, and a plain "nothing to
// delete" message rather than silence or an error.
func TestCmdDeleteSelectorNoMatchesDeletesNothing(t *testing.T) {
	s := &deleteSelectorTestServer{machines: []model.Machine{
		labeledMachine("db-1", map[string]string{"tier": "db"}),
	}}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc := &kube.Client{BaseURL: srv.URL, HTTP: srv.Client()}

	out := captureStdout(t, func() {
		cmdDelete(context.Background(), kc, []string{"machine", "--selector", "tier=web"})
	})
	if len(s.deleted) != 0 {
		t.Fatalf("expected no deletions, got %v", s.deleted)
	}
	if !strings.Contains(out, "nothing to delete") {
		t.Errorf("expected a \"nothing to delete\" message, got:\n%s", out)
	}
}

// TestCmdDeleteSelectorWorksAcrossKinds exercises the same bulk-delete
// path against a second, unrelated kind (MachineSet) to check
// matchingNames/deleteByKindName's per-kind switches are wired correctly
// beyond just the Machine case above -- this is not an exhaustive sweep
// of every one of the 12 kinds delete already supports (get/describe's
// own tests don't do that either), just enough to confirm the dispatch
// generalizes.
func TestCmdDeleteSelectorWorksAcrossKinds(t *testing.T) {
	s := &deleteSelectorTestServer{machineSets: []model.MachineSet{
		{Metadata: model.ObjectMeta{Name: "web", Namespace: "default", Labels: map[string]string{"tier": "web"}}},
		{Metadata: model.ObjectMeta{Name: "db", Namespace: "default", Labels: map[string]string{"tier": "db"}}},
	}}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc := &kube.Client{BaseURL: srv.URL, HTTP: srv.Client()}

	captureStdout(t, func() {
		cmdDelete(context.Background(), kc, []string{"machineset", "--selector", "tier=web"})
	})
	if got, want := s.deleted, []string{"machineset:web"}; !equalStrings(got, want) {
		t.Fatalf("deleted = %v, want %v", got, want)
	}
}

// TestCmdDeleteSelectorRespectsNamespaceFlag confirms `-n`/`--namespace`
// (stripped by nsFlag before cmdDeleteSelector ever sees args) still
// scopes the bulk listing/deletion, exactly as it already does for every
// other verb.
func TestCmdDeleteSelectorRespectsNamespaceFlag(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/staging/machines" {
			called = true
			_ = json.NewEncoder(w).Encode(model.MachineList{})
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer srv.Close()
	kc := &kube.Client{BaseURL: srv.URL, HTTP: srv.Client()}

	captureStdout(t, func() {
		cmdDelete(context.Background(), kc, []string{"-n", "staging", "machine", "--selector", "tier=web"})
	})
	if !called {
		t.Fatal("expected the bulk list to be scoped to the -n staging namespace")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// scaleSelectorTestServer is a minimal in-memory fake covering exactly the
// endpoints cmdScaleSelector's bulk-scale path needs end-to-end: a
// namespaced MachineSet list (matchingNames) and a per-name PATCH
// recording the replicas value it carried -- mirroring
// deleteSelectorTestServer's shape for the same bulk-selector pattern.
type scaleSelectorTestServer struct {
	machineSets []model.MachineSet
	scaled      []string // e.g. "web:5", recorded in request order
}

func (s *scaleSelectorTestServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinesets":
			_ = json.NewEncoder(w).Encode(model.MachineSetList{Items: s.machineSets})
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinesets/"):
			name := strings.TrimPrefix(r.URL.Path, "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machinesets/")
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			spec, _ := body["spec"].(map[string]any)
			s.scaled = append(s.scaled, fmt.Sprintf("%s:%v", name, spec["replicas"]))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(body)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
}

func labeledMachineSet(name string, labels map[string]string) model.MachineSet {
	return model.MachineSet{Metadata: model.ObjectMeta{Name: name, Namespace: "default", Labels: labels}}
}

// TestCmdScaleSelectorScalesOnlyMatching is `kaironctl scale`'s bulk-mode
// counterpart to TestCmdDeleteSelectorDeletesOnlyMatchingMachines: three
// MachineSets, two labeled tier=web, one tier=db, and
// `scale machineset --selector tier=web --replicas 0` must PATCH exactly
// the two web ones (in deterministic, sorted order) to replicas=0 and
// leave the db one alone.
func TestCmdScaleSelectorScalesOnlyMatching(t *testing.T) {
	s := &scaleSelectorTestServer{machineSets: []model.MachineSet{
		labeledMachineSet("web-2", map[string]string{"tier": "web"}),
		labeledMachineSet("db-1", map[string]string{"tier": "db"}),
		labeledMachineSet("web-1", map[string]string{"tier": "web"}),
	}}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc := &kube.Client{BaseURL: srv.URL, HTTP: srv.Client()}

	out := captureStdout(t, func() {
		cmdScale(context.Background(), kc, []string{"machineset", "--selector", "tier=web", "--replicas", "0"})
	})
	if got, want := s.scaled, []string{"web-1:0", "web-2:0"}; !equalStrings(got, want) {
		t.Fatalf("scaled = %v, want %v (sorted, web-only)", got, want)
	}
	if !strings.Contains(out, "machineset/web-1 scaled to 0 replicas") || !strings.Contains(out, "machineset/web-2 scaled to 0 replicas") {
		t.Errorf("expected both scale results reported, got:\n%s", out)
	}
	if strings.Contains(out, "db-1") {
		t.Errorf("must not mention the non-matching db-1 machineset, got:\n%s", out)
	}
}

// TestCmdScaleSelectorDryRunScalesNothing asserts --dry-run's entire
// point for bulk scale: the selector still resolves against the live
// list, but no PATCH request is ever sent.
func TestCmdScaleSelectorDryRunScalesNothing(t *testing.T) {
	s := &scaleSelectorTestServer{machineSets: []model.MachineSet{
		labeledMachineSet("web-1", map[string]string{"tier": "web"}),
	}}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc := &kube.Client{BaseURL: srv.URL, HTTP: srv.Client()}

	out := captureStdout(t, func() {
		cmdScale(context.Background(), kc, []string{"machineset", "--selector", "tier=web", "--replicas", "3", "--dry-run"})
	})
	if len(s.scaled) != 0 {
		t.Fatalf("--dry-run must not scale anything, got scaled=%v", s.scaled)
	}
	if !strings.Contains(out, "machineset/web-1 (dry-run, not scaled)") {
		t.Errorf("expected a dry-run preview line, got:\n%s", out)
	}
}

// TestCmdScaleSelectorNoMatchesScalesNothing covers a selector that
// matches no existing MachineSet: no PATCH calls, and a plain "nothing to
// scale" message rather than silence or an error.
func TestCmdScaleSelectorNoMatchesScalesNothing(t *testing.T) {
	s := &scaleSelectorTestServer{machineSets: []model.MachineSet{
		labeledMachineSet("db-1", map[string]string{"tier": "db"}),
	}}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc := &kube.Client{BaseURL: srv.URL, HTTP: srv.Client()}

	out := captureStdout(t, func() {
		cmdScale(context.Background(), kc, []string{"machineset", "--selector", "tier=web", "--replicas", "3"})
	})
	if len(s.scaled) != 0 {
		t.Fatalf("expected no scaling, got %v", s.scaled)
	}
	if !strings.Contains(out, "no machinesets matched selector; nothing to scale") {
		t.Errorf("expected a \"nothing to scale\" message, got:\n%s", out)
	}
}

// TestCmdScaleSelectorRespectsNamespaceFlag confirms --namespace (scale's
// own flag, parsed within cmdScaleSelector itself since scale never uses
// nsFlag's -n/--namespace stripping the other verbs do) still scopes the
// bulk listing/patching, exactly as it already does for the single-object
// scale path.
func TestCmdScaleSelectorRespectsNamespaceFlag(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/staging/machinesets" {
			called = true
			_ = json.NewEncoder(w).Encode(model.MachineSetList{})
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer srv.Close()
	kc := &kube.Client{BaseURL: srv.URL, HTTP: srv.Client()}

	captureStdout(t, func() {
		cmdScale(context.Background(), kc, []string{"machineset", "--selector", "tier=web", "--replicas", "3", "--namespace", "staging"})
	})
	if !called {
		t.Fatal("expected the bulk list to be scoped to the --namespace staging namespace")
	}
}

// TestCmdGetSelectorFiltersMachines is `kaironctl get`'s read-side
// counterpart to TestCmdDeleteSelectorDeletesOnlyMatchingMachines above:
// the same three-Machine fixture (two tier=web, one tier=db), but via
// `get machine --selector tier=web` this must only ever print rows for the
// two web Machines -- nothing is deleted here, deleteSelectorTestServer's
// GET handler is reused purely as a convenient in-memory Machine list.
func TestCmdGetSelectorFiltersMachines(t *testing.T) {
	s := &deleteSelectorTestServer{machines: []model.Machine{
		labeledMachine("web-2", map[string]string{"tier": "web"}),
		labeledMachine("db-1", map[string]string{"tier": "db"}),
		labeledMachine("web-1", map[string]string{"tier": "web"}),
	}}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc := &kube.Client{BaseURL: srv.URL, HTTP: srv.Client()}

	out := captureStdout(t, func() {
		cmdGet(context.Background(), kc, []string{"machine", "--selector", "tier=web"})
	})
	if !strings.Contains(out, "web-1") || !strings.Contains(out, "web-2") {
		t.Errorf("expected both web-1 and web-2 listed, got:\n%s", out)
	}
	if strings.Contains(out, "db-1") {
		t.Errorf("must not list the non-matching db-1 machine, got:\n%s", out)
	}
	if len(s.deleted) != 0 {
		t.Fatalf("get must never delete anything, got deleted=%v", s.deleted)
	}
}

// TestCmdGetSelectorNoMatchesPrintsHeaderOnly covers a selector matching no
// existing Machine: unlike bulk delete (which prints an explicit "nothing
// to delete"), get has always printed just its header row for an empty
// list (e.g. an empty namespace) -- a selector matching nothing must
// behave identically, not grow a special-cased message of its own.
func TestCmdGetSelectorNoMatchesPrintsHeaderOnly(t *testing.T) {
	s := &deleteSelectorTestServer{machines: []model.Machine{
		labeledMachine("db-1", map[string]string{"tier": "db"}),
	}}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc := &kube.Client{BaseURL: srv.URL, HTTP: srv.Client()}

	out := captureStdout(t, func() {
		cmdGet(context.Background(), kc, []string{"machine", "--selector", "tier=web"})
	})
	if strings.Contains(out, "db-1") {
		t.Errorf("must not list the non-matching db-1 machine, got:\n%s", out)
	}
	if got := strings.TrimRight(out, "\n"); got != "NAME\tNODE\tPHASE\tCPU\tMEMORY\tIP" {
		t.Errorf("expected only the header row, got:\n%q", out)
	}
}

// TestCmdGetWithoutSelectorStillListsEverything is the regression this
// whole feature must never break: `kaironctl get machine` with no
// --selector at all is by far the most common invocation, and must keep
// listing every Machine in the namespace exactly as it always has.
func TestCmdGetWithoutSelectorStillListsEverything(t *testing.T) {
	s := &deleteSelectorTestServer{machines: []model.Machine{
		labeledMachine("web-1", map[string]string{"tier": "web"}),
		labeledMachine("db-1", map[string]string{"tier": "db"}),
	}}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc := &kube.Client{BaseURL: srv.URL, HTTP: srv.Client()}

	out := captureStdout(t, func() {
		cmdGet(context.Background(), kc, []string{"machine"})
	})
	if !strings.Contains(out, "web-1") || !strings.Contains(out, "db-1") {
		t.Errorf("expected both machines listed with no --selector given, got:\n%s", out)
	}
}

// TestCmdGetSelectorWorksAcrossKinds exercises `get --selector` against a
// second kind (MachineSet), mirroring
// TestCmdDeleteSelectorWorksAcrossKinds's own cross-kind check for delete.
func TestCmdGetSelectorWorksAcrossKinds(t *testing.T) {
	s := &deleteSelectorTestServer{machineSets: []model.MachineSet{
		{Metadata: model.ObjectMeta{Name: "web", Namespace: "default", Labels: map[string]string{"tier": "web"}}},
		{Metadata: model.ObjectMeta{Name: "db", Namespace: "default", Labels: map[string]string{"tier": "db"}}},
	}}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	kc := &kube.Client{BaseURL: srv.URL, HTTP: srv.Client()}

	out := captureStdout(t, func() {
		cmdGet(context.Background(), kc, []string{"machineset", "--selector", "tier=web"})
	})
	if !strings.Contains(out, "web") {
		t.Errorf("expected the web machineset listed, got:\n%s", out)
	}
	if strings.Contains(out, "\ndb\t") || strings.HasPrefix(out, "db\t") {
		t.Errorf("must not list the non-matching db machineset, got:\n%s", out)
	}
}

// TestCmdGetSelectorRespectsNamespaceFlag confirms `-n`/`--namespace`
// still scopes the listing once --selector is also given, mirroring
// TestCmdDeleteSelectorRespectsNamespaceFlag for get.
func TestCmdGetSelectorRespectsNamespaceFlag(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/staging/machines" {
			called = true
			_ = json.NewEncoder(w).Encode(model.MachineList{})
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer srv.Close()
	kc := &kube.Client{BaseURL: srv.URL, HTTP: srv.Client()}

	captureStdout(t, func() {
		cmdGet(context.Background(), kc, []string{"-n", "staging", "machine", "--selector", "tier=web"})
	})
	if !called {
		t.Fatal("expected the listing to be scoped to the -n staging namespace")
	}
}
