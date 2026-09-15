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
	// CordonEvacuation, when Enabled, automatically migrates Machines off
	// a Node whose spec.unschedulable transitions to true -- see
	// internal/controller/cordon.go. Zero value (Enabled: false) is
	// today's unchanged behavior: cordoning a node does nothing to
	// already-running Machines.
	CordonEvacuation CordonEvacuation
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
	machines = c.resolveInstanceTypes(ctx, machines)
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
	policyStates := c.loadMigrationPolicyStates(ctx, machines, migrations)
	for _, migration := range migrations {
		if err := c.reconcileMigration(ctx, migration, machineIndex, machines, nodes, assigned, load, policyStates); err != nil {
			status := migration.Status
			status.Phase = "Failed"
			status.Message = err.Error()
			c.Log.Error("migration reconcile failed", "namespace", migration.Namespace(), "migration", migration.Metadata.Name, "error", err)
			if statusErr := c.Kube.PatchMachineMigrationStatus(ctx, migration.Namespace(), migration.Metadata.Name, status); statusErr != nil {
				c.Log.Error("migration status patch failed", "namespace", migration.Namespace(), "migration", migration.Metadata.Name, "error", statusErr)
			}
		}
	}
	c.patchMigrationPolicyStatuses(ctx, policyStates)

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
			c.Log.Error("snapshot reconcile failed", "namespace", snapshot.Namespace(), "snapshot", snapshot.Metadata.Name, "error", err)
			if statusErr := c.Kube.PatchMachineSnapshotStatus(ctx, snapshot.Namespace(), snapshot.Metadata.Name, status); statusErr != nil {
				c.Log.Error("snapshot status patch failed", "namespace", snapshot.Namespace(), "snapshot", snapshot.Metadata.Name, "error", statusErr)
			}
		}
	}

	restores, err := c.Kube.ListMachineSnapshotRestores(ctx)
	if err != nil && !kube.IsNotFound(err) {
		return err
	}
	for _, restore := range restores {
		if err := c.reconcileSnapshotRestore(ctx, restore); err != nil {
			status := restore.Status
			status.Phase = "Failed"
			status.Message = err.Error()
			c.Log.Error("snapshot restore reconcile failed", "namespace", restore.Namespace(), "restore", restore.Metadata.Name, "error", err)
			if statusErr := c.Kube.PatchMachineSnapshotRestoreStatus(ctx, restore.Namespace(), restore.Metadata.Name, status); statusErr != nil {
				c.Log.Error("snapshot restore status patch failed", "namespace", restore.Namespace(), "restore", restore.Metadata.Name, "error", statusErr)
			}
		}
	}

	machineSets, err := c.Kube.ListMachineSets(ctx)
	if err != nil && !kube.IsNotFound(err) {
		return err
	}
	c.reconcileMachineSets(ctx, machineSets, machines)

	quotas, err := c.Kube.ListMachineQuotas(ctx)
	if err != nil && !kube.IsNotFound(err) {
		return err
	}
	quotaTrackers, err := BuildQuotaTrackers(quotas, machines)
	if err != nil {
		return err
	}
	draHints, err := c.buildDRAHints(ctx, machines)
	if err != nil {
		return err
	}

	for _, m := range machines {
		// A never-yet-assigned Machine desired Stopped or Halted has
		// nothing to schedule -- ensureStopped/ensureHalted (internal/agent)
		// both no-op without an existing runtime anyway, so there is no
		// point choosing it a node here.
		desired := m.DesiredPowerState()
		if m.Metadata.DeletionTimestamp != nil || m.Spec.NodeName != "" || desired == "Stopped" || desired == "Halted" {
			continue
		}
		node, err := c.Scheduler.Choose(m, nodes, machines, assigned, draHints[m.Namespace()+"/"+m.Metadata.Name])
		if err != nil {
			status := m.Status
			status.Phase = "Pending"
			status.Message = err.Error()
			if statusErr := c.Kube.PatchMachineStatus(ctx, m.Namespace(), m.Metadata.Name, status); statusErr != nil {
				c.Log.Error("machine status patch failed", "namespace", m.Namespace(), "machine", m.Metadata.Name, "error", statusErr)
			}
			continue
		}
		// Quota is checked (and spent) only once a node is otherwise
		// eligible -- checking it earlier would falsely reserve capacity
		// for a Machine that turns out to have no eligible node anyway,
		// starving a later Machine in this same pass that could have fit.
		if blocker := admitQuota(quotaTrackers, m); blocker != "" {
			status := m.Status
			status.Phase = "Pending"
			status.Message = blocker
			if statusErr := c.Kube.PatchMachineStatus(ctx, m.Namespace(), m.Metadata.Name, status); statusErr != nil {
				c.Log.Error("machine status patch failed", "namespace", m.Namespace(), "machine", m.Metadata.Name, "error", statusErr)
			}
			continue
		}
		specPatch := map[string]any{"nodeName": node}
		if m.Spec.Resources.CPUPinning {
			// Allocated in the same scheduling pass Choose's own
			// eligibility check already used (the same machines/nodes
			// snapshot), so "which node" and "which cores" are decided
			// together and patched in one call below -- they can never
			// land separately. See scheduler.AllocateCPUSet's own doc
			// comment for why this doesn't attempt NUMA-aware selection.
			var chosenNode model.Node
			for _, n := range nodes {
				if n.Metadata.Name == node {
					chosenNode = n
					break
				}
			}
			cpuset, err := scheduler.AllocateCPUSet(m, chosenNode, machines)
			if err != nil {
				status := m.Status
				status.Phase = "Pending"
				status.Message = err.Error()
				if statusErr := c.Kube.PatchMachineStatus(ctx, m.Namespace(), m.Metadata.Name, status); statusErr != nil {
					c.Log.Error("machine status patch failed", "namespace", m.Namespace(), "machine", m.Metadata.Name, "error", statusErr)
				}
				continue
			}
			specPatch["resources"] = map[string]any{"allocatedCpuSet": cpuset}
		}
		if err := c.Kube.PatchMachine(ctx, m.Namespace(), m.Metadata.Name, map[string]any{"spec": specPatch}); err != nil {
			return err
		}
		assigned[node]++
		c.Log.Info("scheduled machine", "namespace", m.Namespace(), "machine", m.Metadata.Name, "node", node)
	}

	for _, list := range quotaTrackers {
		for _, t := range list {
			if statusErr := c.Kube.PatchMachineQuotaStatus(ctx, t.quota.Namespace(), t.quota.Metadata.Name, t.used); statusErr != nil {
				c.Log.Error("machine quota status patch failed", "namespace", t.quota.Namespace(), "quota", t.quota.Metadata.Name, "error", statusErr)
			}
		}
	}

	if err := c.reconcileDisruptionBudgetsStatus(ctx, machines, migrations); err != nil {
		c.Log.Error("machine disruption budget status computation failed", "error", err)
	}

	c.reconcileCordonEvacuation(ctx, machines, nodes, migrations)
	c.detectUnreachableNodes(ctx, machines, nodes)
	return nil
}

func countAssigned(machines []model.Machine) map[string]int {
	assigned := map[string]int{}
	for _, m := range machines {
		// Running and Paused both keep a real FluxVM runtime (and its
		// resident RAM) alive on the node -- only they count here. A
		// Paused Machine's guest CPUs are suspended, but the process, and
		// the host memory backing it, are not. Stopped (full teardown)
		// and Halted (VMM process terminated, FluxVM's own record kept)
		// both genuinely free the capacity they held, so neither counts.
		desired := m.DesiredPowerState()
		if m.Spec.NodeName != "" && m.Metadata.DeletionTimestamp == nil && (desired == "Running" || desired == "Paused") {
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

func (c *Controller) reconcileMigration(ctx context.Context, migration model.MachineMigration, machines map[string]model.Machine, machineList []model.Machine, nodes []model.Node, assigned map[string]int, load *migrationLoad, policyStates []*MigrationPolicyState) error {
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
		if blocker := AdmitMigrationPolicy(policyStates, machine); blocker != "" {
			return c.blockMigration(ctx, migration, blocker)
		}
		strategy, err := effectiveStrategy(machine, migration)
		if err != nil {
			return c.blockMigration(ctx, migration, err.Error())
		}
		target, err := c.migrationTarget(machine, migration.Spec.TargetNode, strategy, nodes, machineList, assigned)
		if err != nil {
			return c.blockMigration(ctx, migration, err.Error())
		}
		if c.MaxConcurrentPerNode > 0 && load.byNode[target] >= c.MaxConcurrentPerNode {
			return c.blockMigration(ctx, migration, fmt.Sprintf("target node %s has reached its concurrent migration limit (%d active, max %d)", target, load.byNode[target], c.MaxConcurrentPerNode))
		}
		if migration.Spec.BandwidthMbps == 0 {
			if bw := bandwidthMbpsFromPolicies(policyStates, machine); bw > 0 {
				if err := c.Kube.PatchMachineMigration(ctx, migration.Namespace(), migration.Metadata.Name, map[string]any{"spec": map[string]any{"bandwidthMbps": bw}}); err != nil {
					c.Log.Error("migration policy bandwidth default patch failed", "namespace", migration.Namespace(), "migration", migration.Metadata.Name, "error", err)
				}
			}
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

func (c *Controller) migrationTarget(machine model.Machine, requested, strategy string, nodes []model.Node, machineList []model.Machine, assigned map[string]int) (string, error) {
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
	// No DRA hint here: a Machine with deviceClaims migrating to a
	// different node would leave its VFIO passthrough device behind on the
	// source node anyway (device claims aren't re-resolved by a
	// migration), so a DRA topology hint has nothing meaningful to nudge
	// toward for target selection specifically.
	target, err := c.Scheduler.Choose(machine, candidates, machineList, assigned, "")
	if err != nil {
		if requested != "" {
			return "", fmt.Errorf("target node %q is not eligible: %w", requested, err)
		}
		return "", fmt.Errorf("no migration target available: %w", err)
	}
	// Preflight against the chosen candidate only -- not a fallback search
	// through the rest of candidates if it fails. See migrationPreflight's
	// own doc comment for what this does and doesn't check.
	if sourceNode, ok := nodeByName(nodes, machine.Spec.NodeName); ok {
		if targetNode, ok := nodeByName(nodes, target); ok {
			if blocker := migrationPreflight(sourceNode, targetNode); blocker != "" {
				return "", fmt.Errorf("migration preflight failed: %s", blocker)
			}
			if blocker := deviceClaimsPreflight(machine, targetNode, strategy); blocker != "" {
				return "", fmt.Errorf("migration preflight failed: %s", blocker)
			}
		}
	}
	return target, nil
}

func nodeByName(nodes []model.Node, name string) (model.Node, bool) {
	for _, n := range nodes {
		if n.Metadata.Name == name {
			return n, true
		}
	}
	return model.Node{}, false
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
		start := time.Now()
		err := c.Reconcile(ctx)
		if c.Metrics != nil {
			c.Metrics.ObserveReconcile(time.Since(start), err)
		}
		if err != nil {
			c.Log.Error("reconcile failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
