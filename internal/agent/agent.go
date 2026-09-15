// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

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

	"google.golang.org/grpc"

	"github.com/zyvorai/kairon/internal/fluxvm"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/metrics"
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
	// Metrics, when set, observes reconcile loop iteration duration/errors
	// (see Run) and, if Kube.Observe is wired to the same Recorder,
	// apiserver call health too. Optional, nil-checked, same convention as
	// MigrationPeer.
	Metrics *metrics.Recorder
	// CSISocketPath/CSIStagingDir/CSIPublishDir configure network-block
	// (CSI-backed) PersistentVolume support -- see internal/agent/csi.go
	// and internal/csinode. CSISocketPath empty (the default) means a
	// CSI-backed spec.volumes[0] is refused with a clear error rather
	// than silently failing; hostPath/local-backed volumes and plain
	// spec.image.path are entirely unaffected either way.
	CSISocketPath string
	CSIStagingDir string
	CSIPublishDir string
	// ImageCacheDir configures spec.image.source support -- see
	// internal/agent/imageimport.go. Empty (the default) means a Machine
	// setting spec.image.source is refused with a clear error rather than
	// silently failing; spec.image.path and spec.volumes are entirely
	// unaffected either way. Same fail-closed convention as
	// CSISocketPath above.
	ImageCacheDir string
	// csiConn caches the dialed connection to kairon-csi-node's local
	// Unix socket -- see csiNodeClient in csi.go. Safe unguarded for the
	// same reason guestIPCheckedAt below is: Reconcile only ever runs
	// single-goroutine, sequential.
	csiConn *grpc.ClientConn
	// guestIPCheckedAt tracks, per "namespace/name", the last time
	// projectNetworkStatus actually queried the guest agent for a Machine
	// that already has a resolved guestIP -- see guestAgentRecheckInterval
	// in network.go. Safe unguarded: Reconcile (and therefore every call
	// into projectNetworkStatus) runs machine-by-machine inside a single
	// goroutine, never concurrently (see Run's ticker loop below).
	guestIPCheckedAt map[string]time.Time
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
			if statusErr := a.Kube.PatchMachineStatus(ctx, m.Namespace(), m.Metadata.Name, status); statusErr != nil {
				a.Log.Error("machine status patch failed", "namespace", m.Namespace(), "machine", m.Metadata.Name, "error", statusErr)
			}
		}
	}
	if err := a.reconcileNetworkResources(ctx); err != nil {
		return err
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
			a.Log.Error("migration reconcile failed", "namespace", migration.Namespace(), "migration", migration.Metadata.Name, "error", err)
			if statusErr := a.Kube.PatchMachineMigrationStatus(ctx, migration.Namespace(), migration.Metadata.Name, status); statusErr != nil {
				a.Log.Error("migration status patch failed", "namespace", migration.Namespace(), "migration", migration.Metadata.Name, "error", statusErr)
			}
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
	if m.Spec.Image.Source != nil {
		cachedPath, err := a.resolveImageSource(ctx, m)
		if err != nil {
			return err
		}
		m.Spec.Image.Path = cachedPath
	}
	bootDisk, volStatus, err := a.resolveBootDiskPath(ctx, m)
	if err != nil {
		return err
	}
	if bootDisk == "" {
		return fmt.Errorf("spec.image.path or spec.volumes[0] is required")
	}
	if len(m.Spec.Volumes) == 0 && m.Spec.Image.Source == nil {
		// Only fence plain spec.image.path against ImageRoot -- a
		// PVC-resolved path already went through a stronger gate (the PVC
		// had to exist and be Bound, not just be a string any Machine
		// author could type in), and a Source-resolved path is
		// kairon-node's own cache directory, not something a Machine
		// author supplied, so the same node-local directory allowlist
		// doesn't apply to either.
		if err := a.validateImagePath(m); err != nil {
			return err
		}
	}
	m.Spec.Image.Path = bootDisk

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
	freshlyCreated := rec == nil
	if freshlyCreated {
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
	status.VolumeStagingPath = volStatus.StagingPath
	status.VolumePublishPath = volStatus.PublishPath
	status.VolumeHandle = volStatus.VolumeID
	appliedVCPUs, appliedMemoryMiB, hotplugErr := a.reconcileHotplug(ctx, m, rec, freshlyCreated)
	status.AppliedVCPUs = appliedVCPUs
	status.AppliedMemoryMiB = appliedMemoryMiB
	if hotplugErr != nil {
		a.Log.Error("hotplug reconcile failed", "namespace", m.Namespace(), "machine", m.Metadata.Name, "error", hotplugErr)
		status.Message = hotplugErr.Error()
	}
	if err := a.projectNetworkStatus(ctx, m, rec, &status); err != nil {
		return err
	}
	a.reconcileGuestQuiesce(ctx, m, rec)
	if err := a.reconcileServiceFabric(ctx, m, status.GuestIP); err != nil {
		return err
	}
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
	status.Network = nil
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
	// Same "don't finish deleting until this succeeds" posture as the
	// FluxVM runtime delete above -- a failed unpublish/unstage leaves
	// the finalizer in place (this Machine stays around, reconciled
	// again next tick) rather than silently leaking a mounted iSCSI
	// session. A no-op for the overwhelmingly common case: plain
	// spec.image.path, or a hostPath/local-backed volume.
	if err := a.teardownCSIVolume(ctx, m); err != nil {
		return fmt.Errorf("tear down CSI volume: %w", err)
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
		return nil, fmt.Errorf("machine requests DRA devices but KAIRON_VFIO_ALLOWLIST is empty; refusing unapproved VFIO passthrough")
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
	if phase == "NeedsRecovery" {
		return a.reconcileNeedsRecovery(ctx, item)
	}
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
		DiskPath:   rec.Disk,
		MAC:        rec.Request.Network.MAC,
		VCPUs:      rec.Request.VCPUs,
		MemoryMiB:  rec.Request.MemoryMiB,

		MigrationNetwork: item.Spec.MigrationNetwork,
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
		if err := a.Flux.NetworkMigrationQuiesce(ctx, rec.ID()); err != nil {
			a.Log.Warn("network migration quiesce failed; continuing", "runtimeID", rec.ID(), "error", err)
		} else if snap, err := a.Flux.NetworkMigrationExport(ctx, rec.ID()); err != nil {
			a.Log.Warn("network migration export failed; continuing without snapshot", "runtimeID", rec.ID(), "error", err)
		} else {
			session.NetworkSnapshot = snap
		}
		prepared, err := a.MigrationPeer.Prepare(ctx, targetURL, session)
		if err != nil {
			return fmt.Errorf("prepare target %s: %w", item.Status.TargetNode, err)
		}
		status.SessionID = prepared.SessionID
		status.RuntimeID = rec.ID()
		status.Backend = prepared.Backend
		status.TransferPhase = prepared.Phase
		status.DataPlaneEncrypted = prepared.DataPlaneEncrypted
		if !prepared.DataPlaneEncrypted {
			a.Log.Warn("live migration data-plane is not encrypted", "migration", item.Metadata.Name, "namespace", item.Namespace())
		}
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

// reconcileNeedsRecovery never auto-resolves a NeedsRecovery migration --
// every tick it only refreshes a live diagnosis of what's actually known
// (status.Recovery's diagnosis fields), and acts *only* when the operator
// has set spec.recovery with an AcknowledgedDiagnosis that matches the
// requested Action and a non-empty Reason. See docs/architecture.md and
// SECURITY.md: an ambiguous commit must never auto-resolve.
func (a *Agent) reconcileNeedsRecovery(ctx context.Context, item model.MachineMigration) error {
	status := item.Status
	if a.MigrationPeer == nil {
		return fmt.Errorf("secure migration control plane is not configured; cannot diagnose NeedsRecovery")
	}
	var targetURL string
	var err error
	if a.MigrationPeerURL != nil {
		targetURL, err = a.MigrationPeerURL(ctx, item.Status.TargetNode)
	} else {
		targetURL, err = a.targetControlURL(ctx, item.Status.TargetNode)
	}
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	recovery := status.Recovery
	if recovery == nil {
		recovery = &model.MachineMigrationRecoveryStatus{}
	}
	recovery.DiagnosedAt = &now
	if status.RuntimeID != "" {
		if rec, err := a.Flux.Get(ctx, status.RuntimeID); err != nil {
			a.Log.Warn("NeedsRecovery: source runtime lookup failed", "runtimeID", status.RuntimeID, "error", err)
			recovery.SourceRuntimeStatus = ""
		} else {
			recovery.SourceRuntimeStatus = rec.Status
		}
	}
	if status.SessionID != "" {
		diag, err := a.MigrationPeer.Diagnose(ctx, targetURL, status.SessionID)
		if err != nil {
			a.Log.Warn("NeedsRecovery: destination diagnosis failed", "sessionID", status.SessionID, "error", err)
		} else {
			recovery.DestinationSessionPhase = diag.SessionPhase
			recovery.DestinationRuntimeFound = diag.DestinationRuntimeFound
			recovery.DestinationRuntimeStatus = diag.DestinationRuntimeStatus
		}
	}
	status.Recovery = recovery

	action := item.Spec.Recovery
	if action == nil {
		// Parked, but the operator can now see live diagnosis via `kubectl
		// get machinemigration -o yaml` without triggering anything.
		return a.Kube.PatchMachineMigrationStatus(ctx, item.Namespace(), item.Metadata.Name, status)
	}
	if strings.TrimSpace(action.Reason) == "" {
		status.Message = "spec.recovery.reason is required -- refusing to act without the operator's attested evidence"
		return a.Kube.PatchMachineMigrationStatus(ctx, item.Namespace(), item.Metadata.Name, status)
	}
	diagnosisMatches := map[string]string{
		model.RecoveryActionConfirmDestinationCommitted:    model.RecoveryDiagnosisDestinationCommitted,
		model.RecoveryActionConfirmDestinationNotCommitted: model.RecoveryDiagnosisDestinationNotCommitted,
	}
	if want, ok := diagnosisMatches[action.Action]; ok && action.AcknowledgedDiagnosis != want {
		status.Message = fmt.Sprintf(
			"spec.recovery.acknowledgedDiagnosis (%s) does not match spec.recovery.action (%s); refusing to act -- update spec.recovery to match what you actually observed",
			action.AcknowledgedDiagnosis, action.Action,
		)
		return a.Kube.PatchMachineMigrationStatus(ctx, item.Namespace(), item.Metadata.Name, status)
	} else if !ok && action.Action != model.RecoveryActionForceAbort {
		status.Message = fmt.Sprintf("unknown spec.recovery.action %q", action.Action)
		return a.Kube.PatchMachineMigrationStatus(ctx, item.Namespace(), item.Metadata.Name, status)
	}

	appliedAt := time.Now().UTC()
	recovery.AppliedAction = action.Action
	recovery.AppliedReason = action.Reason
	recovery.AppliedAcknowledgedDiagnosis = action.AcknowledgedDiagnosis
	recovery.AppliedAt = &appliedAt

	switch action.Action {
	case model.RecoveryActionConfirmDestinationCommitted:
		// Idempotent: heals the case where the destination actually
		// committed but a prior network-resume step failed before the
		// session store could be updated to "Committed" (see
		// NetworkAwareDestination.Commit's own doc comment).
		if err := a.MigrationPeer.Commit(ctx, targetURL, status.SessionID); err != nil {
			a.Log.Warn("NeedsRecovery: ConfirmDestinationCommitted re-commit failed; proceeding anyway on operator's attestation", "sessionID", status.SessionID, "error", err)
		}
		if status.RuntimeID != "" {
			if err := a.Flux.Delete(ctx, status.RuntimeID); err != nil {
				a.Log.Warn("NeedsRecovery: best-effort source runtime delete failed", "runtimeID", status.RuntimeID, "error", err)
			}
		}
		status.Phase = "Cutover"
		status.Message = "recovery: operator confirmed destination committed; source runtime removed, handing off to normal cutover"
	case model.RecoveryActionConfirmDestinationNotCommitted:
		if err := a.MigrationPeer.Commit(ctx, targetURL, status.SessionID); err != nil {
			_ = a.MigrationPeer.Abort(ctx, targetURL, status.SessionID)
			status.Phase = "Failed"
			status.Message = "recovery: retried commit failed, confirming destination never committed: " + err.Error() + "; host-level inspection is required before reusing this Machine"
			break
		}
		if status.RuntimeID != "" {
			if err := a.Flux.Delete(ctx, status.RuntimeID); err != nil {
				a.Log.Warn("NeedsRecovery: best-effort source runtime delete failed", "runtimeID", status.RuntimeID, "error", err)
			}
		}
		status.Phase = "Cutover"
		status.Message = "recovery: retried commit succeeded; source runtime removed, handing off to normal cutover"
	case model.RecoveryActionForceAbort:
		// Deliberately never touches the source runtime here: ground truth
		// is unknown by definition, so destroying the only possibly-live
		// copy of the guest is out of bounds. A 409 from Abort is expected
		// and fine if the destination secretly did commit.
		if err := a.MigrationPeer.Abort(ctx, targetURL, status.SessionID); err != nil {
			a.Log.Warn("NeedsRecovery: ForceAbort peer abort failed", "sessionID", status.SessionID, "error", err)
		}
		status.Phase = "Failed"
		status.Message = "recovery: operator forced abort; source runtime was left untouched -- verify its state before reuse"
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
		start := time.Now()
		err := a.Reconcile(ctx)
		if a.Metrics != nil {
			a.Metrics.ObserveReconcile(time.Since(start), err)
		}
		if err != nil {
			a.Log.Error("reconcile failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
