// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/metrics"
	"github.com/zyvorai/kairon/internal/model"
	"github.com/zyvorai/kairon/internal/scheduler"
)

type Controller struct {
	Kube      *kube.Client
	Scheduler scheduler.Scheduler
	Log       *slog.Logger
	// Metrics, when set, observes every MachineMigration once per
	// reconcile tick. Optional -- nil-checked, same convention as
	// Agent.MigrationPeer.
	Metrics *metrics.Recorder
	// MaxConcurrentPerNode/MaxConcurrentCluster cap how many non-terminal
	// migrations may touch a single node / run cluster-wide at once. 0 (the
	// default) is unlimited -- today's unchanged behavior. A migration that
	// would exceed either limit is rejected into the existing Blocked phase
	// (see isActiveMigrationPhase/migrationLoad), the same mechanism used
	// for an unschedulable Machine or an ineligible target.
	MaxConcurrentPerNode int
	MaxConcurrentCluster int
}

// isActiveMigrationPhase reports whether a migration in this phase is
// currently consuming node resources -- true for everything except the
// not-yet-admitted "" / Pending and the terminal phases.
func isActiveMigrationPhase(phase string) bool {
	switch phase {
	case "Starting", "Running", "Cutover", "Adopting", "Stopping", "Restarting", "NeedsRecovery":
		return true
	}
	return false
}

// migrationLoad is a snapshot of how many active migrations currently touch
// each node / the cluster as a whole, computed once per Reconcile tick.
type migrationLoad struct {
	byNode map[string]int
	total  int
}

func newMigrationLoad(migrations []model.MachineMigration) *migrationLoad {
	l := &migrationLoad{byNode: map[string]int{}}
	for _, m := range migrations {
		if !isActiveMigrationPhase(m.Status.Phase) {
			continue
		}
		l.total++
		if m.Status.SourceNode != "" {
			l.byNode[m.Status.SourceNode]++
		}
		if m.Status.TargetNode != "" && m.Status.TargetNode != m.Status.SourceNode {
			l.byNode[m.Status.TargetNode]++
		}
	}
	return l
}

// admit records a newly-admitted migration against the load snapshot, so a
// burst of Pending migrations processed in the same tick (e.g. one
// `kaironctl evacuate` creating several at once) can't all pass a stale
// pre-tick count.
func (l *migrationLoad) admit(sourceNode, targetNode string) {
	l.total++
	if sourceNode != "" {
		l.byNode[sourceNode]++
	}
	if targetNode != "" && targetNode != sourceNode {
		l.byNode[targetNode]++
	}
}

func (c *Controller) Reconcile(ctx context.Context) error {
	machines, err := c.Kube.ListMachines(ctx)
	if err != nil {
		return err
	}
	nodes, err := c.Kube.ListNodes(ctx)
	if err != nil {
		return err
	}
	assigned := countAssigned(machines)
	machineIndex := indexMachines(machines)

	migrations, err := c.Kube.ListMachineMigrations(ctx)
	if err != nil && !kube.IsNotFound(err) {
		return err
	}
	if c.Metrics != nil {
		c.Metrics.ObserveMigrations(migrations)
	}
	load := newMigrationLoad(migrations)
	for _, migration := range migrations {
		if err := c.reconcileMigration(ctx, migration, machineIndex, nodes, assigned, load); err != nil {
			status := migration.Status
			status.Phase = "Failed"
			status.Message = err.Error()
			_ = c.Kube.PatchMachineMigrationStatus(ctx, migration.Namespace(), migration.Metadata.Name, status)
			c.Log.Error("migration reconcile failed", "namespace", migration.Namespace(), "migration", migration.Metadata.Name, "error", err)
		}
	}

	snapshots, err := c.Kube.ListMachineSnapshots(ctx)
	if err != nil && !kube.IsNotFound(err) {
		return err
	}
	for _, snapshot := range snapshots {
		if err := c.reconcileSnapshot(ctx, snapshot, machineIndex); err != nil {
			status := snapshot.Status
			status.Phase = "Failed"
			status.ReadyToUse = false
			status.Message = err.Error()
			_ = c.Kube.PatchMachineSnapshotStatus(ctx, snapshot.Namespace(), snapshot.Metadata.Name, status)
			c.Log.Error("snapshot reconcile failed", "namespace", snapshot.Namespace(), "snapshot", snapshot.Metadata.Name, "error", err)
		}
	}

	for _, m := range machines {
		if m.Metadata.DeletionTimestamp != nil || m.Spec.NodeName != "" || m.DesiredPowerState() == "Stopped" {
			continue
		}
		node, err := c.Scheduler.Choose(m, nodes, assigned)
		if err != nil {
			status := m.Status
			status.Phase = "Pending"
			status.Message = err.Error()
			_ = c.Kube.PatchMachineStatus(ctx, m.Namespace(), m.Metadata.Name, status)
			continue
		}
		if err := c.Kube.PatchMachine(ctx, m.Namespace(), m.Metadata.Name, map[string]any{"spec": map[string]any{"nodeName": node}}); err != nil {
			return err
		}
		assigned[node]++
		c.Log.Info("scheduled machine", "namespace", m.Namespace(), "machine", m.Metadata.Name, "node", node)
	}
	return nil
}

func countAssigned(machines []model.Machine) map[string]int {
	assigned := map[string]int{}
	for _, m := range machines {
		if m.Spec.NodeName != "" && m.Metadata.DeletionTimestamp == nil && m.DesiredPowerState() == "Running" {
			assigned[m.Spec.NodeName]++
		}
	}
	return assigned
}

func indexMachines(machines []model.Machine) map[string]model.Machine {
	out := make(map[string]model.Machine, len(machines))
	for _, m := range machines {
		out[m.Namespace()+"/"+m.Metadata.Name] = m
	}
	return out
}

func (c *Controller) reconcileMigration(ctx context.Context, migration model.MachineMigration, machines map[string]model.Machine, nodes []model.Node, assigned map[string]int, load *migrationLoad) error {
	if migration.Status.Phase == "Succeeded" || migration.Status.Phase == "Failed" || migration.Status.Phase == "Blocked" || migration.Status.Phase == "NeedsRecovery" {
		return nil
	}
	if strings.TrimSpace(migration.Spec.MachineName) == "" {
		return fmt.Errorf("spec.machineName is required")
	}
	machine, ok := machines[migration.Namespace()+"/"+migration.Spec.MachineName]
	if !ok {
		return fmt.Errorf("machine %s/%s not found", migration.Namespace(), migration.Spec.MachineName)
	}
	if machine.Metadata.DeletionTimestamp != nil {
		return fmt.Errorf("machine is being deleted")
	}

	status := migration.Status
	phase := status.Phase
	if phase == "" || phase == "Pending" {
		if machine.Spec.NodeName == "" {
			return c.blockMigration(ctx, migration, "Machine has not been scheduled yet")
		}
		if c.MaxConcurrentCluster > 0 && load.total >= c.MaxConcurrentCluster {
			return c.blockMigration(ctx, migration, fmt.Sprintf("cluster migration concurrency limit reached (%d active, max %d)", load.total, c.MaxConcurrentCluster))
		}
		if c.MaxConcurrentPerNode > 0 && load.byNode[machine.Spec.NodeName] >= c.MaxConcurrentPerNode {
			return c.blockMigration(ctx, migration, fmt.Sprintf("source node %s has reached its concurrent migration limit (%d active, max %d)", machine.Spec.NodeName, load.byNode[machine.Spec.NodeName], c.MaxConcurrentPerNode))
		}
		target, err := c.migrationTarget(machine, migration.Spec.TargetNode, nodes, assigned)
		if err != nil {
			return c.blockMigration(ctx, migration, err.Error())
		}
		if c.MaxConcurrentPerNode > 0 && load.byNode[target] >= c.MaxConcurrentPerNode {
			return c.blockMigration(ctx, migration, fmt.Sprintf("target node %s has reached its concurrent migration limit (%d active, max %d)", target, load.byNode[target], c.MaxConcurrentPerNode))
		}
		strategy, err := effectiveStrategy(machine, migration)
		if err != nil {
			return c.blockMigration(ctx, migration, err.Error())
		}
		status.SourceNode = machine.Spec.NodeName
		status.TargetNode = target
		status.EffectiveStrategy = strategy
		status.Message = ""
		load.admit(status.SourceNode, status.TargetNode)
		if strategy == "live" {
			status.Phase = "Starting"
			status.Message = "source node agent will securely prepare the target before touching the source runtime"
			return c.Kube.PatchMachineMigrationStatus(ctx, migration.Namespace(), migration.Metadata.Name, status)
		}
		status.Phase = "Stopping"
		status.Message = "stopping source runtime for controlled cold evacuation"
		if err := c.Kube.PatchMachine(ctx, machine.Namespace(), machine.Metadata.Name, map[string]any{"spec": map[string]any{"powerState": "Stopped"}}); err != nil {
			return err
		}
		return c.Kube.PatchMachineMigrationStatus(ctx, migration.Namespace(), migration.Metadata.Name, status)
	}

	switch phase {
	case "Starting", "Running":
		// The source node agent owns peer preparation, transfer and commit.
		return nil
	case "Cutover":
		if status.EffectiveStrategy != "live" {
			return fmt.Errorf("cutover phase is only valid for live migration")
		}
		patch := map[string]any{
			"spec": map[string]any{"nodeName": status.TargetNode},
			"metadata": map[string]any{"annotations": map[string]any{
				model.AnnotationAdoptOnly:    "true",
				model.AnnotationMigrationRef: migration.Metadata.Name,
			}},
		}
		if err := c.Kube.PatchMachine(ctx, machine.Namespace(), machine.Metadata.Name, patch); err != nil {
			return err
		}
		status.Phase = "Adopting"
		status.Message = "source migration completed; target is in adopt-only mode"
		return c.Kube.PatchMachineMigrationStatus(ctx, migration.Namespace(), migration.Metadata.Name, status)
	case "Adopting":
		if machine.Spec.NodeName != status.TargetNode || machine.Status.NodeName != status.TargetNode || machine.Status.Phase != "Running" {
			return nil
		}
		patch := map[string]any{"metadata": map[string]any{"annotations": map[string]any{
			model.AnnotationAdoptOnly:    nil,
			model.AnnotationMigrationRef: nil,
		}}}
		if err := c.Kube.PatchMachine(ctx, machine.Namespace(), machine.Metadata.Name, patch); err != nil {
			return err
		}
		status.Phase = "Succeeded"
		status.Message = "live migration completed and target runtime adopted"
		return c.Kube.PatchMachineMigrationStatus(ctx, migration.Namespace(), migration.Metadata.Name, status)
	case "Stopping":
		if machine.Status.Phase != "Stopped" {
			return nil
		}
		if err := c.Kube.PatchMachine(ctx, machine.Namespace(), machine.Metadata.Name, map[string]any{"spec": map[string]any{"nodeName": status.TargetNode, "powerState": "Running"}}); err != nil {
			return err
		}
		status.Phase = "Restarting"
		status.Message = "source stopped; Machine reassigned to target"
		return c.Kube.PatchMachineMigrationStatus(ctx, migration.Namespace(), migration.Metadata.Name, status)
	case "Restarting":
		if machine.Spec.NodeName == status.TargetNode && machine.Status.NodeName == status.TargetNode && machine.Status.Phase == "Running" {
			status.Phase = "Succeeded"
			status.Message = "cold migration completed"
			return c.Kube.PatchMachineMigrationStatus(ctx, migration.Namespace(), migration.Metadata.Name, status)
		}
		return nil
	default:
		return fmt.Errorf("unknown migration status phase %q", phase)
	}
}

func (c *Controller) blockMigration(ctx context.Context, migration model.MachineMigration, message string) error {
	status := migration.Status
	status.Phase = "Blocked"
	status.Message = message
	return c.Kube.PatchMachineMigrationStatus(ctx, migration.Namespace(), migration.Metadata.Name, status)
}

func effectiveStrategy(machine model.Machine, migration model.MachineMigration) (string, error) {
	strategy := strings.ToLower(strings.TrimSpace(migration.Spec.Strategy))
	if strategy == "" {
		strategy = "auto"
	}
	switch strategy {
	case "cold":
		return "cold", nil
	case "live":
		if !liveBackendEligible(machine.Spec.Runtime.Backend) {
			return "", fmt.Errorf("live migration requires qemu backend; Machine requests %q", machine.Spec.Runtime.Backend)
		}
		return "live", nil
	case "auto":
		// Until Kairon has cluster-wide backend capability discovery, auto stays
		// conservative and chooses the fully implemented cold path. Operators can
		// explicitly request live to use the v0.3 secure handshake.
		return "cold", nil
	default:
		return "", fmt.Errorf("unsupported migration strategy %q", migration.Spec.Strategy)
	}
}

func liveBackendEligible(backend string) bool {
	return backend == "" || backend == "auto" || backend == "qemu"
}

func (c *Controller) migrationTarget(machine model.Machine, requested string, nodes []model.Node, assigned map[string]int) (string, error) {
	var candidates []model.Node
	for _, n := range nodes {
		if n.Metadata.Name == machine.Spec.NodeName {
			continue
		}
		if requested != "" && n.Metadata.Name != requested {
			continue
		}
		candidates = append(candidates, n)
	}
	if requested != "" && len(candidates) == 0 {
		return "", fmt.Errorf("target node %q does not exist or is the current source node", requested)
	}
	target, err := c.Scheduler.Choose(machine, candidates, assigned)
	if err != nil {
		if requested != "" {
			return "", fmt.Errorf("target node %q is not eligible: %w", requested, err)
		}
		return "", fmt.Errorf("no migration target available: %w", err)
	}
	return target, nil
}

func (c *Controller) reconcileSnapshot(ctx context.Context, snapshot model.MachineSnapshot, machines map[string]model.Machine) error {
	if snapshot.Status.Phase == "Succeeded" || snapshot.Status.Phase == "Failed" {
		return nil
	}
	if strings.TrimSpace(snapshot.Spec.MachineName) == "" {
		return fmt.Errorf("spec.machineName is required")
	}
	machine, ok := machines[snapshot.Namespace()+"/"+snapshot.Spec.MachineName]
	if !ok {
		return fmt.Errorf("machine %s/%s not found", snapshot.Namespace(), snapshot.Spec.MachineName)
	}
	if len(machine.Spec.Volumes) == 0 {
		return fmt.Errorf("machine has no PVC-backed spec.volumes to snapshot")
	}
	refs := make([]model.VolumeSnapshotReference, 0, len(machine.Spec.Volumes))
	allReady := true
	for _, volume := range machine.Spec.Volumes {
		if strings.TrimSpace(volume.Name) == "" || strings.TrimSpace(volume.ClaimName) == "" {
			return fmt.Errorf("machine volume requires name and claimName")
		}
		name := snapshotVolumeName(snapshot.Metadata.Name, volume.Name)
		vs, err := c.Kube.GetVolumeSnapshot(ctx, snapshot.Namespace(), name)
		if err != nil {
			if !kube.IsNotFound(err) {
				return err
			}
			claim := volume.ClaimName
			vs = model.VolumeSnapshot{
				TypeMeta: model.TypeMeta{APIVersion: "snapshot.storage.k8s.io/v1", Kind: "VolumeSnapshot"},
				Metadata: model.ObjectMeta{Name: name, Namespace: snapshot.Namespace(), Labels: map[string]string{
					"kairon.zyvor.dev/machine":  machine.Metadata.Name,
					"kairon.zyvor.dev/snapshot": snapshot.Metadata.Name,
				}},
				Spec: model.VolumeSnapshotSpec{Source: model.VolumeSnapshotSource{PersistentVolumeClaimName: &claim}},
			}
			if snapshot.Spec.VolumeSnapshotClassName != "" {
				className := snapshot.Spec.VolumeSnapshotClassName
				vs.Spec.VolumeSnapshotClassName = &className
			}
			vs, err = c.Kube.CreateVolumeSnapshot(ctx, snapshot.Namespace(), vs)
			if err != nil {
				return err
			}
		}
		if vs.Status.Error != nil && vs.Status.Error.Message != nil && *vs.Status.Error.Message != "" {
			return fmt.Errorf("VolumeSnapshot %s failed: %s", name, *vs.Status.Error.Message)
		}
		ready := vs.Status.ReadyToUse != nil && *vs.Status.ReadyToUse
		if !ready {
			allReady = false
		}
		refs = append(refs, model.VolumeSnapshotReference{VolumeName: volume.Name, VolumeSnapshotName: name, ReadyToUse: ready})
	}
	status := snapshot.Status
	status.VolumeSnapshots = refs
	status.ReadyToUse = allReady
	if allReady {
		status.Phase = "Succeeded"
		status.Message = "all CSI VolumeSnapshots are ready to use"
	} else {
		status.Phase = "Pending"
		status.Message = "waiting for CSI VolumeSnapshots"
	}
	return c.Kube.PatchMachineSnapshotStatus(ctx, snapshot.Namespace(), snapshot.Metadata.Name, status)
}

var nonDNS = regexp.MustCompile(`[^a-z0-9-]+`)

func snapshotVolumeName(snapshot, volume string) string {
	name := strings.ToLower(snapshot + "-" + volume)
	name = nonDNS.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	if name == "" {
		return "kairon-snapshot"
	}
	return name
}

func (c *Controller) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := c.Reconcile(ctx); err != nil {
			c.Log.Error("reconcile failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
