package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zyvorai/kairon/internal/fluxvm"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

var pciBDFPattern = regexp.MustCompile(`(?i)^(?:[0-9a-f]{4}:)?[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)

type Agent struct {
	NodeName       string
	Kube           *kube.Client
	Flux           *fluxvm.Client
	DefaultBackend string
	ImageRoot      string
	VFIOAllowlist  map[string]struct{}
	Log            *slog.Logger
}

func (a *Agent) Reconcile(ctx context.Context) error {
	machines, err := a.Kube.ListMachines(ctx)
	if err != nil {
		return err
	}
	for _, m := range machines {
		if m.Spec.NodeName != a.NodeName {
			continue
		}
		if err := a.reconcileMachine(ctx, m); err != nil {
			a.Log.Error("machine reconcile failed", "namespace", m.Namespace(), "machine", m.Metadata.Name, "error", err)
			status := m.Status
			status.Phase = "Error"
			status.NodeName = a.NodeName
			status.Message = err.Error()
			status.Conditions = []model.Condition{{Type: "Ready", Status: "False", Reason: "ReconcileFailed", Message: err.Error(), LastTransitionTime: time.Now().UTC()}}
			_ = a.Kube.PatchMachineStatus(ctx, m.Namespace(), m.Metadata.Name, status)
		}
	}
	migrations, err := a.Kube.ListMachineMigrations(ctx)
	if err != nil {
		// This lets a v0.2 node binary coexist during a rolling CRD upgrade.
		if kube.IsNotFound(err) {
			return nil
		}
		return err
	}
	for _, migration := range migrations {
		if migration.Status.SourceNode != a.NodeName {
			continue
		}
		if err := a.reconcileMigration(ctx, migration); err != nil {
			status := migration.Status
			status.Phase = "Failed"
			status.Message = err.Error()
			_ = a.Kube.PatchMachineMigrationStatus(ctx, migration.Namespace(), migration.Metadata.Name, status)
			a.Log.Error("migration reconcile failed", "namespace", migration.Namespace(), "migration", migration.Metadata.Name, "error", err)
		}
	}
	return nil
}

func (a *Agent) reconcileMachine(ctx context.Context, m model.Machine) error {
	if m.Metadata.DeletionTimestamp != nil {
		return a.cleanup(ctx, m)
	}
	if !model.HasFinalizer(m, model.Finalizer) {
		finalizers := append(append([]string{}, m.Metadata.Finalizers...), model.Finalizer)
		if err := a.Kube.PatchMachine(ctx, m.Namespace(), m.Metadata.Name, map[string]any{"metadata": map[string]any{"finalizers": finalizers}}); err != nil {
			return err
		}
	}
	if m.DesiredPowerState() == "Stopped" {
		return a.ensureStopped(ctx, m)
	}
	if m.Spec.Image.Path == "" {
		return fmt.Errorf("spec.image.path is required")
	}
	if err := a.validateImagePath(m); err != nil {
		return err
	}

	rec, err := a.current(ctx, m)
	if err != nil {
		return err
	}
	if rec == nil && model.AnnotationTrue(m.Metadata, model.AnnotationAdoptOnly) {
		status := m.Status
		status.Phase = "Blocked"
		status.NodeName = a.NodeName
		status.Message = "adopt-only cutover guard: incoming FluxVM runtime was not found; refusing to create a duplicate VM"
		status.Conditions = []model.Condition{{Type: "Ready", Status: "False", Reason: "IncomingRuntimeMissing", Message: status.Message, LastTransitionTime: time.Now().UTC()}}
		return a.Kube.PatchMachineStatus(ctx, m.Namespace(), m.Metadata.Name, status)
	}
	if rec == nil {
		vfioDevices, err := a.resolveVFIODevices(ctx, m)
		if err != nil {
			return err
		}
		rec, err = a.Flux.CreateWithVFIO(ctx, m, a.DefaultBackend, vfioDevices)
		if err != nil {
			return err
		}
		a.Log.Info("created runtime", "machine", m.Metadata.Name, "runtimeID", rec.ID(), "vfioDevices", vfioDevices)
	}
	status := m.Status
	status.Phase = normalizePhase(rec.Status)
	status.NodeName = a.NodeName
	status.RuntimeID = rec.ID()
	status.GuestIP = rec.GuestIP
	status.Message = ""
	status.Conditions = []model.Condition{{Type: "Ready", Status: readyStatus(status.Phase), Reason: "FluxVMReconciled", LastTransitionTime: time.Now().UTC()}}
	return a.Kube.PatchMachineStatus(ctx, m.Namespace(), m.Metadata.Name, status)
}

func (a *Agent) validateImagePath(m model.Machine) error {
	if a.ImageRoot == "" {
		return nil
	}
	root, err := filepath.Abs(a.ImageRoot)
	if err != nil {
		return fmt.Errorf("resolve image root: %w", err)
	}
	img, err := filepath.Abs(m.Spec.Image.Path)
	if err != nil {
		return fmt.Errorf("resolve image path: %w", err)
	}
	rel, err := filepath.Rel(root, img)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("image path %q is outside allowed root %q", m.Spec.Image.Path, a.ImageRoot)
	}
	return nil
}

func (a *Agent) current(ctx context.Context, m model.Machine) (*fluxvm.Record, error) {
	if m.Status.RuntimeID != "" {
		if rec, err := a.Flux.Get(ctx, m.Status.RuntimeID); err == nil {
			return rec, nil
		}
	}
	return a.Flux.LookupByName(ctx, m.RuntimeName())
}

func (a *Agent) ensureStopped(ctx context.Context, m model.Machine) error {
	rec, err := a.current(ctx, m)
	if err != nil {
		return err
	}
	if rec != nil && rec.ID() != "" {
		if err := a.Flux.Delete(ctx, rec.ID()); err != nil {
			return err
		}
	}
	status := m.Status
	status.Phase = "Stopped"
	status.NodeName = a.NodeName
	status.RuntimeID = ""
	status.GuestIP = ""
	status.Message = ""
	status.Conditions = []model.Condition{{Type: "Ready", Status: "False", Reason: "PoweredOff", LastTransitionTime: time.Now().UTC()}}
	return a.Kube.PatchMachineStatus(ctx, m.Namespace(), m.Metadata.Name, status)
}

func (a *Agent) cleanup(ctx context.Context, m model.Machine) error {
	if m.Status.RuntimeID != "" {
		if err := a.Flux.Delete(ctx, m.Status.RuntimeID); err != nil {
			return err
		}
	}
	var finals []string
	for _, f := range m.Metadata.Finalizers {
		if f != model.Finalizer {
			finals = append(finals, f)
		}
	}
	return a.Kube.PatchMachine(ctx, m.Namespace(), m.Metadata.Name, map[string]any{"metadata": map[string]any{"finalizers": finals}})
}

func (a *Agent) resolveVFIODevices(ctx context.Context, m model.Machine) ([]string, error) {
	if len(m.Spec.DeviceClaims) == 0 {
		return nil, nil
	}
	if len(a.VFIOAllowlist) == 0 {
		return nil, fmt.Errorf("Machine requests DRA devices but KAIRON_VFIO_ALLOWLIST is empty; refusing unapproved VFIO passthrough")
	}
	seen := map[string]struct{}{}
	for _, ref := range m.Spec.DeviceClaims {
		if strings.TrimSpace(ref.Name) == "" {
			return nil, fmt.Errorf("deviceClaims contains an empty ResourceClaim name")
		}
		claim, err := a.Kube.GetResourceClaim(ctx, m.Namespace(), ref.Name)
		if err != nil {
			return nil, fmt.Errorf("get ResourceClaim %s: %w", ref.Name, err)
		}
		if claim.Status.Allocation == nil {
			return nil, fmt.Errorf("ResourceClaim %s has no DRA allocation yet", ref.Name)
		}
		var candidates []string
		if claim.Metadata.Annotations != nil {
			for _, raw := range strings.Split(claim.Metadata.Annotations[model.AnnotationVFIOBDF], ",") {
				if v := strings.TrimSpace(raw); v != "" {
					candidates = append(candidates, v)
				}
			}
		}
		if claim.Status.Allocation != nil {
			for _, r := range claim.Status.Allocation.Devices.Results {
				if pciBDFPattern.MatchString(strings.TrimSpace(r.Device)) {
					candidates = append(candidates, r.Device)
				}
			}
		}
		if len(candidates) == 0 {
			return nil, fmt.Errorf("ResourceClaim %s is allocated but exposes no PCI BDF; set %s or use a DRA driver whose device result is a BDF", ref.Name, model.AnnotationVFIOBDF)
		}
		for _, raw := range candidates {
			bdf, err := NormalizeBDF(raw)
			if err != nil {
				return nil, fmt.Errorf("ResourceClaim %s: %w", ref.Name, err)
			}
			if _, ok := a.VFIOAllowlist[bdf]; !ok {
				return nil, fmt.Errorf("VFIO device %s from ResourceClaim %s is not in this node's allowlist", bdf, ref.Name)
			}
			seen[bdf] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for bdf := range seen {
		out = append(out, bdf)
	}
	sort.Strings(out)
	return out, nil
}

func ParseVFIOAllowlist(raw string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		bdf, err := NormalizeBDF(item)
		if err != nil {
			return nil, err
		}
		out[bdf] = struct{}{}
	}
	return out, nil
}

func NormalizeBDF(raw string) (string, error) {
	bdf := strings.ToLower(strings.TrimSpace(raw))
	if !pciBDFPattern.MatchString(bdf) {
		return "", fmt.Errorf("invalid PCI BDF %q", raw)
	}
	if strings.Count(bdf, ":") == 1 {
		bdf = "0000:" + bdf
	}
	return bdf, nil
}

func ValidateMigrationDestination(raw string) error {
	if !strings.HasPrefix(raw, "tcp:") {
		return fmt.Errorf("live migration destination must use tcp:host:port")
	}
	addr := strings.TrimPrefix(raw, "tcp:")
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid live migration destination %q: %w", raw, err)
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("live migration destination host is empty")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("live migration destination has invalid TCP port %q", portText)
	}
	return nil
}

func (a *Agent) reconcileMigration(ctx context.Context, migration model.MachineMigration) error {
	phase := migration.Status.Phase
	if phase != "Starting" && phase != "Running" {
		return nil
	}
	if migration.Status.EffectiveStrategy != "live" {
		return nil
	}
	if err := ValidateMigrationDestination(migration.Spec.Destination); err != nil {
		return err
	}
	machine, err := a.Kube.GetMachine(ctx, migration.Namespace(), migration.Spec.MachineName)
	if err != nil {
		return err
	}
	if machine.Spec.NodeName != a.NodeName {
		return fmt.Errorf("source Machine is assigned to %s, not %s", machine.Spec.NodeName, a.NodeName)
	}
	backend := machine.Spec.Runtime.Backend
	if backend != "" && backend != "auto" && backend != "qemu" {
		return fmt.Errorf("FluxVM live migration currently requires qemu backend, Machine requests %s", backend)
	}

	status := migration.Status
	var fluxStatus fluxvm.MigrationStatus
	if phase == "Starting" {
		rec, err := a.current(ctx, machine)
		if err != nil {
			return err
		}
		if rec == nil || rec.ID() == "" {
			return fmt.Errorf("source FluxVM runtime not found")
		}
		mode := migration.Spec.Mode
		if mode == "" {
			mode = "pre-copy"
		}
		if mode != "pre-copy" && mode != "post-copy" {
			return fmt.Errorf("unsupported migration mode %q", mode)
		}
		fluxStatus, err = a.Flux.StartMigration(ctx, rec.ID(), fluxvm.MigrationStartRequest{
			Destination:     migration.Spec.Destination,
			Mode:            mode,
			BandwidthMbps:   migration.Spec.BandwidthMbps,
			MaxDowntimeMs:   migration.Spec.MaxDowntimeMs,
			MultifdChannels: migration.Spec.MultifdChannels,
		})
		if err != nil {
			return err
		}
		status.RuntimeID = rec.ID()
	} else {
		if status.RuntimeID == "" {
			return fmt.Errorf("Running migration has no runtimeID")
		}
		fluxStatus, err = a.Flux.MigrationStatus(ctx, status.RuntimeID)
		if err != nil {
			return err
		}
	}
	applyMigrationProgress(&status, fluxStatus)
	return a.Kube.PatchMachineMigrationStatus(ctx, migration.Namespace(), migration.Metadata.Name, status)
}

func applyMigrationProgress(status *model.MachineMigrationStatus, s fluxvm.MigrationStatus) {
	phase := strings.ToLower(s.Phase)
	if phase == "" {
		phase = strings.ToLower(s.Status)
	}
	status.FluxPhase = phase
	if s.RAMTransferred != nil {
		status.RAMTransferred = *s.RAMTransferred
	}
	if s.RAMRemaining != nil {
		status.RAMRemaining = *s.RAMRemaining
	}
	if s.RAMTotal != nil {
		status.RAMTotal = *s.RAMTotal
	}
	if s.TotalTimeMs != nil {
		status.TotalTimeMs = *s.TotalTimeMs
	}
	if s.DowntimeMs != nil {
		status.DowntimeMs = *s.DowntimeMs
	}
	switch phase {
	case "completed":
		status.Phase = "Cutover"
		status.Message = "FluxVM source migration completed; waiting for guarded target adoption"
	case "failed", "cancelled", "canceled":
		status.Phase = "Failed"
		status.Message = s.Error
		if status.Message == "" {
			status.Message = "FluxVM migration " + phase
		}
	default:
		status.Phase = "Running"
		status.Message = "FluxVM live migration in progress"
	}
}

func normalizePhase(s string) string {
	s = strings.ToLower(s)
	switch s {
	case "running", "started", "ready":
		return "Running"
	case "paused":
		return "Paused"
	case "stopped", "exited":
		return "Stopped"
	case "creating", "starting", "pending":
		return "Starting"
	default:
		if s == "" {
			return "Running"
		}
		return "Unknown"
	}
}
func readyStatus(phase string) string {
	if phase == "Running" {
		return "True"
	}
	return "False"
}

func (a *Agent) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := a.Reconcile(ctx); err != nil {
			a.Log.Error("reconcile failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
