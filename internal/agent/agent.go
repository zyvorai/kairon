package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/zyvorai/kairon/internal/fluxvm"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/migration"
	"github.com/zyvorai/kairon/internal/model"
)

var pciBDFPattern = regexp.MustCompile(`(?i)^(?:[0-9a-f]{4}:)?[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)

type Agent struct {
	NodeName         string
	Kube             *kube.Client
	Flux             *fluxvm.Client
	DefaultBackend   string
	ImageRoot        string
	VFIOAllowlist    map[string]struct{}
	MigrationPeer    *migration.Client
	SourceMigrator   migration.SourceDriver
	MigrationPort    int
	MigrationPeerURL func(context.Context, string) (string, error)
	Log              *slog.Logger
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

func (a *Agent) reconcileMigration(ctx context.Context, item model.MachineMigration) error {
	phase := item.Status.Phase
	if phase != "Starting" && phase != "Running" {
		return nil
	}
	if item.Status.EffectiveStrategy != "live" {
		return nil
	}
	machine, err := a.Kube.GetMachine(ctx, item.Namespace(), item.Spec.MachineName)
	if err != nil {
		return err
	}
	if machine.Spec.NodeName != a.NodeName {
		return fmt.Errorf("source Machine is assigned to %s, not %s", machine.Spec.NodeName, a.NodeName)
	}
	backend := machine.Spec.Runtime.Backend
	if backend != "" && backend != "auto" && backend != "qemu" {
		return fmt.Errorf("live migration currently requires qemu backend, Machine requests %s", backend)
	}
	if a.MigrationPeer == nil || a.SourceMigrator == nil {
		return a.blockLiveMigration(ctx, item, "secure migration control plane or source adapter is not configured; source runtime was left untouched")
	}

	rec, err := a.current(ctx, machine)
	if err != nil {
		return err
	}
	if rec == nil || rec.ID() == "" {
		return fmt.Errorf("source FluxVM runtime not found")
	}
	session := migration.Session{
		ID:         migrationSessionID(item),
		Namespace:  item.Namespace(),
		Machine:    item.Spec.MachineName,
		SourceNode: item.Status.SourceNode,
		TargetNode: item.Status.TargetNode,
		RuntimeID:  rec.ID(),
	}
	var targetURL string
	if a.MigrationPeerURL != nil {
		targetURL, err = a.MigrationPeerURL(ctx, item.Status.TargetNode)
	} else {
		targetURL, err = a.targetControlURL(ctx, item.Status.TargetNode)
	}
	if err != nil {
		return err
	}

	status := item.Status
	if phase == "Starting" {
		prepared, err := a.MigrationPeer.Prepare(ctx, targetURL, session)
		if err != nil {
			return fmt.Errorf("prepare target %s: %w", item.Status.TargetNode, err)
		}
		status.SessionID = prepared.SessionID
		status.RuntimeID = rec.ID()
		status.Backend = prepared.Backend
		status.TransferPhase = prepared.Phase
		if !prepared.TransferSupported {
			message := prepared.Reason
			if message == "" {
				message = "target node has no live migration backend"
			}
			return a.blockLiveMigrationWithStatus(ctx, item, status, message+"; source runtime was left untouched")
		}
		mode := item.Spec.Mode
		if mode == "" {
			mode = "pre-copy"
		}
		if mode != "pre-copy" && mode != "post-copy" {
			_ = a.MigrationPeer.Abort(ctx, targetURL, session.ID)
			return fmt.Errorf("unsupported migration mode %q", mode)
		}
		transfer, err := a.SourceMigrator.Start(ctx, migration.SourceRequest{
			Session:   session,
			RuntimeID: rec.ID(),
			Endpoint:  prepared.Endpoint,
			Options: migration.SourceOptions{
				Mode:            mode,
				BandwidthMbps:   item.Spec.BandwidthMbps,
				MaxDowntimeMs:   item.Spec.MaxDowntimeMs,
				MultifdChannels: item.Spec.MultifdChannels,
			},
		})
		if err != nil {
			_ = a.MigrationPeer.Abort(ctx, targetURL, session.ID)
			if errors.Is(err, migration.ErrUnsupported) {
				return a.blockLiveMigrationWithStatus(ctx, item, status, err.Error()+"; prepared target was aborted and source runtime was left untouched")
			}
			status.Phase = "Failed"
			status.TransferPhase = "start-failed"
			status.Message = "start source transfer failed; prepared target was aborted: " + err.Error()
			return a.Kube.PatchMachineMigrationStatus(ctx, item.Namespace(), item.Metadata.Name, status)
		}
		status.TransferID = transfer.TransferID
		return a.projectTransfer(ctx, item, status, session, targetURL, transfer)
	}

	if status.SessionID == "" || status.TransferID == "" {
		return fmt.Errorf("running migration is missing sessionID or transferID")
	}
	transfer, err := a.SourceMigrator.Status(ctx, session, status.TransferID)
	if err != nil {
		return fmt.Errorf("poll source transfer: %w", err)
	}
	return a.projectTransfer(ctx, item, status, session, targetURL, transfer)
}

func (a *Agent) projectTransfer(ctx context.Context, item model.MachineMigration, status model.MachineMigrationStatus, session migration.Session, targetURL string, transfer migration.TransferStatus) error {
	phase := strings.ToLower(strings.TrimSpace(transfer.Phase))
	status.TransferPhase = phase
	status.RAMTransferred = transfer.RAMTransferred
	status.RAMRemaining = transfer.RAMRemaining
	status.RAMTotal = transfer.RAMTotal
	status.TotalTimeMs = transfer.TotalTimeMs
	status.DowntimeMs = transfer.DowntimeMs
	if transfer.TransferID != "" {
		status.TransferID = transfer.TransferID
	}
	switch phase {
	case "completed", "transferred":
		if err := a.MigrationPeer.Commit(ctx, targetURL, session.ID); err != nil {
			status.Phase = "NeedsRecovery"
			status.Message = "source transfer completed but target commit failed; automatic cutover is stopped to avoid split brain: " + err.Error()
			return a.Kube.PatchMachineMigrationStatus(ctx, item.Namespace(), item.Metadata.Name, status)
		}
		status.Phase = "Cutover"
		status.Message = "source transfer completed and target committed; waiting for guarded target adoption"
	case "failed", "cancelled", "canceled", "aborted":
		_ = a.MigrationPeer.Abort(ctx, targetURL, session.ID)
		status.Phase = "Failed"
		status.Message = transfer.Message
		if status.Message == "" {
			status.Message = "source transfer " + phase + "; prepared target aborted"
		}
	default:
		status.Phase = "Running"
		status.Message = "live migration transfer in progress"
	}
	return a.Kube.PatchMachineMigrationStatus(ctx, item.Namespace(), item.Metadata.Name, status)
}

func (a *Agent) blockLiveMigration(ctx context.Context, item model.MachineMigration, message string) error {
	return a.blockLiveMigrationWithStatus(ctx, item, item.Status, message)
}

func (a *Agent) blockLiveMigrationWithStatus(ctx context.Context, item model.MachineMigration, status model.MachineMigrationStatus, message string) error {
	status.Phase = "Blocked"
	status.Message = message
	return a.Kube.PatchMachineMigrationStatus(ctx, item.Namespace(), item.Metadata.Name, status)
}

func migrationSessionID(item model.MachineMigration) string {
	seed := item.Metadata.UID
	if seed == "" {
		seed = item.Namespace() + "/" + item.Metadata.Name
	}
	sum := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("kmm-%x", sum[:16])
}

func (a *Agent) targetControlURL(ctx context.Context, targetNode string) (string, error) {
	if targetNode == "" {
		return "", fmt.Errorf("migration target node is empty")
	}
	nodes, err := a.Kube.ListNodes(ctx)
	if err != nil {
		return "", err
	}
	var address string
	for _, node := range nodes {
		if node.Metadata.Name != targetNode {
			continue
		}
		for _, candidate := range node.Status.Addresses {
			if candidate.Type == "InternalIP" && strings.TrimSpace(candidate.Address) != "" {
				address = strings.TrimSpace(candidate.Address)
				break
			}
		}
		break
	}
	if address == "" {
		return "", fmt.Errorf("target node %q has no InternalIP for the mTLS migration control plane", targetNode)
	}
	port := a.MigrationPort
	if port == 0 {
		port = 9443
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid migration port %d", port)
	}
	return "https://" + net.JoinHostPort(address, fmt.Sprintf("%d", port)), nil
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
