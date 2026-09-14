// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"fmt"
	"hash/fnv"
	"sort"

	"github.com/zyvorai/kairon/internal/model"
)

// draPreferenceWeight is the fixed score bonus a candidate node gets when
// it's draPreferredNode -- a modest, fixed nudge (not configurable, unlike
// PreferredAffinity/PreferredAntiAffinity's operator-chosen weights) since
// a DRA topology hint is a "prefer this node if nothing else matters more"
// signal, not something an operator tunes per-Machine.
const draPreferenceWeight = 5

type Scheduler struct {
	RequireCapableLabel bool
}

// Choose picks the winning node among every node passing eligible (a hard
// filter: architecture/nodeSelector/required affinity-anti-affinity/
// Ready+capable, exactly as before) by scoring each eligible node and
// taking the highest score, breaking any remaining tie the same
// deterministic-hash way as always. draPreferredNode is an optional DRA
// topology-awareness hint (see internal/controller's draPreferredNode) --
// pass "" when there's no such hint (the overwhelmingly common case: a
// Machine with no deviceClaims, or a claim not yet allocated to a specific
// node).
//
// A Machine with no PreferredAffinity/PreferredAntiAffinity/
// TopologySpreadConstraints and no DRA hint schedules identically to
// before this scoring pass existed: score degenerates to exactly
// -assigned[node], so the highest-scoring set is exactly the least-loaded
// set, in the same node order, so the same hash tie-break lands on the
// same node.
func (s Scheduler) Choose(m model.Machine, nodes []model.Node, machines []model.Machine, assigned map[string]int, draPreferredNode string) (string, error) {
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

	best := make([]model.Node, 0, len(eligible))
	bestScore := 0
	for i, n := range eligible {
		sc := s.score(m, n, nodes, machines, assigned, draPreferredNode)
		switch {
		case i == 0 || sc > bestScore:
			bestScore = sc
			best = best[:0]
			best = append(best, n)
		case sc == bestScore:
			best = append(best, n)
		}
	}
	if len(best) == 1 {
		return best[0].Metadata.Name, nil
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(m.Namespace() + "/" + m.Metadata.Name))
	return best[int(h.Sum32())%len(best)].Metadata.Name, nil
}

// score combines: a load-balancing penalty (1 point per Machine already
// assigned to the node -- the entirety of the old "least-loaded"
// behavior), each satisfied PreferredAffinity/PreferredAntiAffinity term's
// signed Weight, a topology-spread penalty per TopologySpreadConstraint
// (see topologySpreadPenalty), and draPreferenceWeight if this node is the
// DRA-preferred one. All soft signals are additive with the load penalty
// on purpose -- a strong enough preference (a high Weight) can outweigh an
// otherwise-imbalanced load, the same tradeoff Kubernetes' own weighted
// scoring plugins make.
func (s Scheduler) score(m model.Machine, n model.Node, nodes []model.Node, machines []model.Machine, assigned map[string]int, draPreferredNode string) int {
	sc := -assigned[n.Metadata.Name]
	for _, wt := range m.Spec.Placement.PreferredAffinity {
		if termSatisfied(m, n, nodes, machines, wt.MachineAffinityTerm) {
			sc += int(wt.Weight)
		}
	}
	for _, wt := range m.Spec.Placement.PreferredAntiAffinity {
		if termSatisfied(m, n, nodes, machines, wt.MachineAffinityTerm) {
			sc -= int(wt.Weight)
		}
	}
	for _, c := range m.Spec.Placement.TopologySpreadConstraints {
		sc -= topologySpreadPenalty(m, n, nodes, machines, c)
	}
	if draPreferredNode != "" && n.Metadata.Name == draPreferredNode {
		sc += draPreferenceWeight
	}
	return sc
}

// topologySpreadPenalty counts how many other Machines matching
// c.LabelSelector already sit in n's c.TopologyKey domain -- higher means
// less spread, so score subtracts it. Contributes 0 (no opinion) when n
// doesn't carry the TopologyKey label at all, same "can't compare, so
// don't penalize" posture eligible's required-affinity check takes for a
// missing topology label.
func topologySpreadPenalty(m model.Machine, n model.Node, nodes []model.Node, machines []model.Machine, c model.TopologySpreadConstraint) int {
	candidateValue, ok := n.Metadata.Labels[c.TopologyKey]
	if !ok {
		return 0
	}
	count := 0
	for _, other := range machines {
		if other.Namespace() == m.Namespace() && other.Metadata.Name == m.Metadata.Name {
			continue
		}
		if other.Spec.NodeName == "" || !model.LabelsMatch(other.Metadata.Labels, c.LabelSelector) {
			continue
		}
		if otherValue, ok := nodeLabelValue(nodes, other.Spec.NodeName, c.TopologyKey); ok && otherValue == candidateValue {
			count++
		}
	}
	return count
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
// value for term.TopologyKey. Used as-is for Affinity/PreferredAffinity
// (must be true) and negated for AntiAffinity/PreferredAntiAffinity.
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
