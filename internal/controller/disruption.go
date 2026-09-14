// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"fmt"

	"github.com/zyvorai/kairon/internal/model"
)

// BudgetState tracks one MachineDisruptionBudget's remaining allowance for
// the duration of a single caller -- either one `kaironctl evacuate` run
// (cmd/kaironctl/main.go's cmdEvacuate) or one admission-webhook request
// (webhook.go's validateMachineMigration). Recomputed from scratch every
// time, never persisted: nothing reconciles this object on a timer (see
// docs/guides/machine-disruption-budgets.md). isTerminalMigrationPhase here
// deliberately duplicates rather than imports internal/uiapi's own
// isNonTerminalMigrationPhase -- same "each consumer's own narrower
// definition of terminal" precedent that helper's own comment establishes,
// not a shared cross-package dependency worth taking for one boolean.
type BudgetState struct {
	budget  model.MachineDisruptionBudget
	allowed int
}

func isTerminalMigrationPhase(phase string) bool {
	switch phase {
	case "Succeeded", "Failed", "Blocked", "":
		return true
	}
	return false
}

// LoadBudgetStates computes each budget's remaining allowance from the
// current Machines/MachineMigrations that already exist -- callers pass in
// their own freshly-listed data (this does no I/O itself), then pass the
// result to AdmitDisruption once per Machine they're about to disrupt.
func LoadBudgetStates(budgets []model.MachineDisruptionBudget, machines []model.Machine, migrations []model.MachineMigration) ([]*BudgetState, error) {
	inFlight := map[string]bool{}
	for _, mig := range migrations {
		if !isTerminalMigrationPhase(mig.Status.Phase) {
			inFlight[mig.Namespace()+"/"+mig.Spec.MachineName] = true
		}
	}
	states := make([]*BudgetState, 0, len(budgets))
	for _, b := range budgets {
		total, healthy := 0, 0
		for _, m := range machines {
			if !model.LabelsMatch(m.Metadata.Labels, b.Spec.Selector) {
				continue
			}
			total++
			if m.Status.Phase == "Running" && !inFlight[m.Namespace()+"/"+m.Metadata.Name] {
				healthy++
			}
		}
		desired, err := b.Spec.DesiredHealthy(total)
		if err != nil {
			return nil, fmt.Errorf("MachineDisruptionBudget %s/%s: %w", b.Namespace(), b.Metadata.Name, err)
		}
		allowed := healthy - desired
		if allowed < 0 {
			allowed = 0
		}
		states = append(states, &BudgetState{budget: b, allowed: allowed})
	}
	return states, nil
}

// AdmitDisruption returns a non-empty reason if disrupting machine would
// violate some budget it matches, otherwise it spends one allowance from
// every budget machine matches and returns "".
func AdmitDisruption(states []*BudgetState, machine model.Machine) string {
	var applicable []*BudgetState
	for _, st := range states {
		if model.LabelsMatch(machine.Metadata.Labels, st.budget.Spec.Selector) {
			if st.allowed <= 0 {
				return fmt.Sprintf("MachineDisruptionBudget %s/%s has 0 disruptions allowed", st.budget.Namespace(), st.budget.Metadata.Name)
			}
			applicable = append(applicable, st)
		}
	}
	for _, st := range applicable {
		st.allowed--
	}
	return ""
}
