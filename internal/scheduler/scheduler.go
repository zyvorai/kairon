// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"fmt"
	"hash/fnv"
	"sort"

	"github.com/zyvorai/kairon/internal/model"
)

type Scheduler struct {
	RequireCapableLabel bool
}

func (s Scheduler) Choose(m model.Machine, nodes []model.Node, machines []model.Machine, assigned map[string]int) (string, error) {
	var eligible []model.Node
	for _, n := range nodes {
		if s.eligible(m, n, nodes, machines) {
			eligible = append(eligible, n)
		}
	}
	if len(eligible) == 0 {
		return "", fmt.Errorf("no Ready Kairon-capable nodes match placement constraints")
	}

	sort.Slice(eligible, func(i, j int) bool { return eligible[i].Metadata.Name < eligible[j].Metadata.Name })
	min := int(^uint(0) >> 1)
	for _, n := range eligible {
		if assigned[n.Metadata.Name] < min {
			min = assigned[n.Metadata.Name]
		}
	}
	var least []model.Node
	for _, n := range eligible {
		if assigned[n.Metadata.Name] == min {
			least = append(least, n)
		}
	}
	if len(least) == 1 {
		return least[0].Metadata.Name, nil
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(m.Namespace() + "/" + m.Metadata.Name))
	return least[int(h.Sum32())%len(least)].Metadata.Name, nil
}

func (s Scheduler) eligible(m model.Machine, n model.Node, nodes []model.Node, machines []model.Machine) bool {
	if n.Spec.Unschedulable || !ready(n) {
		return false
	}
	if s.RequireCapableLabel && n.Metadata.Labels[model.CapableLabel] != "true" {
		return false
	}
	if arch := m.Spec.Placement.Architecture; arch != "" && n.Metadata.Labels["kubernetes.io/arch"] != arch {
		return false
	}
	for k, v := range m.Spec.Placement.NodeSelector {
		if n.Metadata.Labels[k] != v {
			return false
		}
	}
	for _, term := range m.Spec.Placement.Affinity {
		if !termSatisfied(m, n, nodes, machines, term) {
			return false
		}
	}
	for _, term := range m.Spec.Placement.AntiAffinity {
		if termSatisfied(m, n, nodes, machines, term) {
			return false
		}
	}
	return true
}

// termSatisfied reports whether at least one other Machine matching
// term.LabelSelector currently sits on a node sharing the candidate node's
// value for term.TopologyKey. Used as-is for Affinity (must be true) and
// negated for AntiAffinity (must be false).
func termSatisfied(m model.Machine, n model.Node, nodes []model.Node, machines []model.Machine, term model.MachineAffinityTerm) bool {
	candidateValue, ok := n.Metadata.Labels[term.TopologyKey]
	if !ok {
		return false
	}
	for _, other := range machines {
		if other.Namespace() == m.Namespace() && other.Metadata.Name == m.Metadata.Name {
			continue
		}
		if other.Spec.NodeName == "" || !model.LabelsMatch(other.Metadata.Labels, term.LabelSelector) {
			continue
		}
		otherValue, ok := nodeLabelValue(nodes, other.Spec.NodeName, term.TopologyKey)
		if ok && otherValue == candidateValue {
			return true
		}
	}
	return false
}

func nodeLabelValue(nodes []model.Node, nodeName, key string) (string, bool) {
	for _, n := range nodes {
		if n.Metadata.Name == nodeName {
			v, ok := n.Metadata.Labels[key]
			return v, ok
		}
	}
	return "", false
}

func ready(n model.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	return false
}
