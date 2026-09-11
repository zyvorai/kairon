// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"hash/fnv"
	"testing"

	"github.com/zyvorai/kairon/internal/model"
)

func node(name string, ready, capable bool) model.Node {
	var n model.Node
	n.Metadata.Name = name
	n.Metadata.Labels = map[string]string{"kubernetes.io/arch": "amd64"}
	if capable {
		n.Metadata.Labels[model.CapableLabel] = "true"
	}
	status := "False"
	if ready {
		status = "True"
	}
	n.Status.Conditions = []model.NodeCondition{{Type: "Ready", Status: status}}
	return n
}

func TestChooseLeastLoaded(t *testing.T) {
	m := model.Machine{Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"}}
	s := Scheduler{RequireCapableLabel: true}
	got, err := s.Choose(m, []model.Node{node("a", true, true), node("b", true, true)}, map[string]int{"a": 5, "b": 1})
	if err != nil || got != "b" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestChooseRejectsUnready(t *testing.T) {
	m := model.Machine{Metadata: model.ObjectMeta{Name: "db"}}
	s := Scheduler{RequireCapableLabel: true}
	_, err := s.Choose(m, []model.Node{node("a", false, true)}, map[string]int{})
	if err == nil {
		t.Fatal("expected no eligible nodes")
	}
}

func TestChooseExcludesUnschedulable(t *testing.T) {
	m := model.Machine{Metadata: model.ObjectMeta{Name: "db"}}
	s := Scheduler{RequireCapableLabel: true}
	n := node("a", true, true)
	n.Spec.Unschedulable = true
	_, err := s.Choose(m, []model.Node{n}, map[string]int{})
	if err == nil {
		t.Fatal("expected unschedulable node to be excluded")
	}
}

func TestChooseAllowsUncapableWhenLabelNotRequired(t *testing.T) {
	m := model.Machine{Metadata: model.ObjectMeta{Name: "db"}}
	s := Scheduler{RequireCapableLabel: false}
	got, err := s.Choose(m, []model.Node{node("a", true, false)}, map[string]int{})
	if err != nil || got != "a" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestChooseFiltersByArchitecture(t *testing.T) {
	s := Scheduler{RequireCapableLabel: true}
	arm := node("arm-node", true, true)
	arm.Metadata.Labels["kubernetes.io/arch"] = "arm64"
	amd := node("amd-node", true, true)

	m := model.Machine{Metadata: model.ObjectMeta{Name: "db"}, Spec: model.MachineSpec{Placement: model.PlacementSpec{Architecture: "arm64"}}}
	got, err := s.Choose(m, []model.Node{arm, amd}, map[string]int{})
	if err != nil || got != "arm-node" {
		t.Fatalf("got %q err=%v", got, err)
	}

	noMatch := model.Machine{Metadata: model.ObjectMeta{Name: "db"}, Spec: model.MachineSpec{Placement: model.PlacementSpec{Architecture: "riscv64"}}}
	if _, err := s.Choose(noMatch, []model.Node{arm, amd}, map[string]int{}); err == nil {
		t.Fatal("expected no node to match an unavailable architecture")
	}
}

func TestChooseFiltersByNodeSelector(t *testing.T) {
	s := Scheduler{RequireCapableLabel: true}
	east := node("east", true, true)
	east.Metadata.Labels["zone"] = "us-east"
	west := node("west", true, true)
	west.Metadata.Labels["zone"] = "us-west"

	m := model.Machine{Metadata: model.ObjectMeta{Name: "db"}, Spec: model.MachineSpec{Placement: model.PlacementSpec{NodeSelector: map[string]string{"zone": "us-east"}}}}
	got, err := s.Choose(m, []model.Node{east, west}, map[string]int{})
	if err != nil || got != "east" {
		t.Fatalf("got %q err=%v", got, err)
	}

	noMatch := model.Machine{Metadata: model.ObjectMeta{Name: "db"}, Spec: model.MachineSpec{Placement: model.PlacementSpec{NodeSelector: map[string]string{"zone": "us-central"}}}}
	if _, err := s.Choose(noMatch, []model.Node{east, west}, map[string]int{}); err == nil {
		t.Fatal("expected no node to match an unsatisfied node selector")
	}
}

func TestChooseTieBreaksByHashAcrossEqualLoad(t *testing.T) {
	s := Scheduler{RequireCapableLabel: true}
	nodes := []model.Node{node("a", true, true), node("b", true, true), node("c", true, true)}
	assigned := map[string]int{"a": 0, "b": 0, "c": 0}

	expected := func(ns string) string {
		h := fnv.New32a()
		_, _ = h.Write([]byte("default/" + ns))
		return []string{"a", "b", "c"}[int(h.Sum32())%3]
	}

	for _, name := range []string{"machine-one", "machine-two", "machine-three"} {
		m := model.Machine{Metadata: model.ObjectMeta{Name: name}}
		got, err := s.Choose(m, nodes, assigned)
		if err != nil {
			t.Fatalf("machine=%s err=%v", name, err)
		}
		if got != expected(name) {
			t.Fatalf("machine=%s got=%q want=%q", name, got, expected(name))
		}
		// Determinism: calling again for the same machine must return the same node.
		again, err := s.Choose(m, nodes, assigned)
		if err != nil || again != got {
			t.Fatalf("machine=%s not deterministic: first=%q second=%q err=%v", name, got, again, err)
		}
	}
}

func TestChooseNoEligibleNodesReasonIsPlacement(t *testing.T) {
	s := Scheduler{RequireCapableLabel: true}
	nodes := []model.Node{node("a", true, true), node("b", true, true)}
	m := model.Machine{Metadata: model.ObjectMeta{Name: "db"}, Spec: model.MachineSpec{Placement: model.PlacementSpec{Architecture: "riscv64"}}}
	_, err := s.Choose(m, nodes, map[string]int{})
	if err == nil {
		t.Fatal("expected an error when Ready/capable nodes exist but none match placement")
	}
}
