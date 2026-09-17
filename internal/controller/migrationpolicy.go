// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

// MigrationPolicyState tracks one MigrationPolicy's current active-migration
// count against its own MaxConcurrent cap -- mirrors BudgetState
// (disruption.go) exactly, same "recomputed from scratch every Reconcile
// tick, never persisted" shape.
type MigrationPolicyState struct {
	policy model.MigrationPolicy
	active int
}

// LoadMigrationPolicyStates computes each policy's current active-migration
// count from the machines/migrations this tick already listed -- no
// separate I/O, mirrors LoadBudgetStates.
func LoadMigrationPolicyStates(policies []model.MigrationPolicy, machines []model.Machine, migrations []model.MachineMigration) []*MigrationPolicyState {
	machineByKey := map[string]model.Machine{}
	for _, m := range machines {
		machineByKey[m.Namespace()+"/"+m.Metadata.Name] = m
	}
	states := make([]*MigrationPolicyState, 0, len(policies))
	for _, p := range policies {
		active := 0
		for _, mig := range migrations {
			if mig.Namespace() != p.Namespace() || !isActiveMigrationPhase(mig.Status.Phase) {
				continue
			}
			m, ok := machineByKey[mig.Namespace()+"/"+mig.Spec.MachineName]
			if !ok || !model.LabelsMatch(m.Metadata.Labels, p.Spec.Selector) {
				continue
			}
			active++
		}
		states = append(states, &MigrationPolicyState{policy: p, active: active})
	}
	return states
}

// AdmitMigrationPolicy returns a non-empty blocker if starting a
// migration for machine would push some matching MigrationPolicy's
// MaxConcurrent over its cap, otherwise it spends one slot from every
// policy machine matches and returns "" -- mirrors AdmitDisruption
// exactly.
func AdmitMigrationPolicy(states []*MigrationPolicyState, machine model.Machine) string {
	var applicable []*MigrationPolicyState
	for _, st := range states {
		if machine.Namespace() != st.policy.Namespace() || !model.LabelsMatch(machine.Metadata.Labels, st.policy.Spec.Selector) {
			continue
		}
		if st.policy.Spec.MaxConcurrent > 0 && st.active >= st.policy.Spec.MaxConcurrent {
			return "MigrationPolicy " + st.policy.Namespace() + "/" + st.policy.Metadata.Name + ": maxConcurrent reached"
		}
		applicable = append(applicable, st)
	}
	for _, st := range applicable {
		st.active++
	}
	return ""
}

// BandwidthMbpsFromPolicies returns the first matching MigrationPolicy's
// BandwidthMbps (list order, not otherwise sorted -- see
// MigrationPolicySpec.BandwidthMbps's own doc comment for why this is
// deliberately "first match," not merged), or 0 if none match or none set
// one. Exported (not just called from controller.go's reconcile loop
// anymore) so kaironctl's `describe migrationpolicy` preview can compute
// the identical "what bandwidth would this machine actually get" answer
// without duplicating this decision -- same reasoning as AdmitMigrationPolicy
// already being exported for kaironctl's evacuate preview.
func BandwidthMbpsFromPolicies(states []*MigrationPolicyState, machine model.Machine) uint64 {
	for _, st := range states {
		if machine.Namespace() != st.policy.Namespace() || !model.LabelsMatch(machine.Metadata.Labels, st.policy.Spec.Selector) {
			continue
		}
		if st.policy.Spec.BandwidthMbps > 0 {
			return st.policy.Spec.BandwidthMbps
		}
	}
	return 0
}

// loadMigrationPolicyStates lists every MigrationPolicy and computes its
// current state -- called once per Reconcile tick, before the migration
// admission loop, so AdmitMigrationPolicy/BandwidthMbpsFromPolicies see
// (and spend against) the same states every reconcileMigration call this
// tick shares. Returns nil (not an error) if the CRD isn't installed --
// same tolerant-of-absence convention every other optional CRD kind in
// this reconcile loop already follows.
func (c *Controller) loadMigrationPolicyStates(ctx context.Context, machines []model.Machine, migrations []model.MachineMigration) []*MigrationPolicyState {
	policies, err := c.Kube.ListMigrationPolicies(ctx)
	if err != nil {
		if !kube.IsNotFound(err) {
			c.Log.Error("list migration policies failed", "error", err)
		}
		return nil
	}
	return LoadMigrationPolicyStates(policies, machines, migrations)
}

// patchMigrationPolicyStatuses persists each policy's active-migration
// count as of the end of this tick's migration admission loop -- purely
// observational, mirrors reconcileDisruptionBudgetsStatus's own status-only
// patch pass.
func (c *Controller) patchMigrationPolicyStatuses(ctx context.Context, states []*MigrationPolicyState) {
	for _, st := range states {
		status := model.MigrationPolicyStatus{ActiveMigrations: st.active}
		if err := c.Kube.PatchMigrationPolicyStatus(ctx, st.policy.Namespace(), st.policy.Metadata.Name, status); err != nil {
			c.Log.Error("migration policy status patch failed", "namespace", st.policy.Namespace(), "policy", st.policy.Metadata.Name, "error", err)
		}
	}
}
