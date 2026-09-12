// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/zyvorai/kairon/internal/model"
)

func webMachine(name, node, phase string) model.Machine {
	return model.Machine{
		Metadata: model.ObjectMeta{Name: name, Namespace: "prod", Labels: map[string]string{"tier": "web"}},
		Spec:     model.MachineSpec{NodeName: node},
		Status:   model.MachineStatus{Phase: phase},
	}
}

func webBudget(name, minAvailable string) model.MachineDisruptionBudget {
	return model.MachineDisruptionBudget{
		Metadata: model.ObjectMeta{Name: name, Namespace: "prod"},
		Spec:     model.MachineDisruptionBudgetSpec{Selector: map[string]string{"tier": "web"}, MinAvailable: minAvailable},
	}
}

func TestAdmitDisruptionAllowsUntilBudgetExhausted(t *testing.T) {
	machines := []model.Machine{
		webMachine("web-1", "node-a", "Running"),
		webMachine("web-2", "node-a", "Running"),
		webMachine("web-3", "node-a", "Running"),
	}
	states, err := loadBudgetStates([]model.MachineDisruptionBudget{webBudget("web-pdb", "2")}, machines, nil)
	if err != nil {
		t.Fatalf("loadBudgetStates: %v", err)
	}
	// 3 healthy, minAvailable 2 -> exactly 1 disruption allowed.
	if reason := admitDisruption(states, machines[0]); reason != "" {
		t.Fatalf("first disruption should be admitted, got reason %q", reason)
	}
	if reason := admitDisruption(states, machines[1]); reason == "" {
		t.Fatal("second disruption should be blocked once the budget is spent")
	}
}

func TestAdmitDisruptionIgnoresMachinesAlreadyMidMigration(t *testing.T) {
	machines := []model.Machine{
		webMachine("web-1", "node-a", "Running"),
		webMachine("web-2", "node-a", "Running"),
	}
	migrations := []model.MachineMigration{
		{Metadata: model.ObjectMeta{Namespace: "prod"}, Spec: model.MachineMigrationSpec{MachineName: "web-1"}, Status: model.MachineMigrationStatus{Phase: "Starting"}},
	}
	// minAvailable 2, but web-1 is already mid-migration -> only web-2 counts
	// as healthy, so disrupting it too would go below minAvailable.
	states, err := loadBudgetStates([]model.MachineDisruptionBudget{webBudget("web-pdb", "2")}, machines, migrations)
	if err != nil {
		t.Fatalf("loadBudgetStates: %v", err)
	}
	if reason := admitDisruption(states, machines[1]); reason == "" {
		t.Fatal("expected the in-flight migration to count against currentHealthy, blocking this disruption")
	}
}

func TestAdmitDisruptionSpendsAllowanceAcrossEveryMatchingBudget(t *testing.T) {
	m := model.Machine{
		Metadata: model.ObjectMeta{Name: "both", Namespace: "prod", Labels: map[string]string{"tier": "web", "team": "payments"}},
		Status:   model.MachineStatus{Phase: "Running"},
	}
	tierBudget := model.MachineDisruptionBudget{
		Metadata: model.ObjectMeta{Name: "tier-pdb", Namespace: "prod"},
		Spec:     model.MachineDisruptionBudgetSpec{Selector: map[string]string{"tier": "web"}, MaxUnavailable: "1"},
	}
	teamBudget := model.MachineDisruptionBudget{
		Metadata: model.ObjectMeta{Name: "team-pdb", Namespace: "prod"},
		Spec:     model.MachineDisruptionBudgetSpec{Selector: map[string]string{"team": "payments"}, MaxUnavailable: "0"},
	}
	states, err := loadBudgetStates([]model.MachineDisruptionBudget{tierBudget, teamBudget}, []model.Machine{m}, nil)
	if err != nil {
		t.Fatalf("loadBudgetStates: %v", err)
	}
	// tierBudget alone would allow 1 disruption, but teamBudget (maxUnavailable
	// 0 out of the same 1 total machine) allows none -- the stricter budget wins.
	if reason := admitDisruption(states, m); reason == "" {
		t.Fatal("expected the stricter team budget to block this disruption")
	}
}

func TestAdmitDisruptionMachineNotMatchingAnyBudgetIsAlwaysAllowed(t *testing.T) {
	m := model.Machine{Metadata: model.ObjectMeta{Name: "unmanaged", Namespace: "prod"}, Status: model.MachineStatus{Phase: "Running"}}
	states, err := loadBudgetStates([]model.MachineDisruptionBudget{webBudget("web-pdb", "100")}, []model.Machine{m}, nil)
	if err != nil {
		t.Fatalf("loadBudgetStates: %v", err)
	}
	if reason := admitDisruption(states, m); reason != "" {
		t.Fatalf("expected no budget to apply, got reason %q", reason)
	}
}
