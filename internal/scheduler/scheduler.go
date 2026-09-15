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
	var preSkew []model.Node
	for _, n := range nodes {
		if s.eligible(m, n, nodes, machines) {
			preSkew = append(preSkew, n)
		}
	}
	if len(preSkew) == 0 {
		return "", fmt.Errorf("no Ready Kairon-capable nodes match placement constraints")
	}
	eligible := s.filterMaxSkew(m, preSkew, nodes, machines)
	if len(eligible) == 0 {
		return "", fmt.Errorf("no node satisfies topologySpreadConstraints maxSkew (whenUnsatisfiable: DoNotSchedule)")
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

// filterMaxSkew drops any candidate node that would violate a
// WhenUnsatisfiable: DoNotSchedule constraint's MaxSkew if m were placed
// there. ScheduleAnyway constraints (the default, and every constraint
// before this field existed) are never enforced here -- only scored, via
// topologySpreadPenalty above, which runs unconditionally regardless of
// WhenUnsatisfiable. A Machine with no DoNotSchedule constraints returns
// candidates unchanged.
//
// MaxSkew's Go zero value (unset, via omitempty -- there's no way to
// distinguish "unset" from "explicitly 0" through this field alone) is
// treated as a real, meaningful 0 -- "domains must stay perfectly
// balanced" -- rather than silently disabling enforcement or defaulting
// to some other value. Real Kubernetes requires maxSkew to be a positive
// integer and rejects 0 at admission; Kairon has no such API-level
// validation layer to hook a rejection into, and defaulting to "no
// opinion" for a field an operator set WhenUnsatisfiable specifically to
// make meaningful would be the more surprising choice of the two --
// erring toward the stricter interpretation matches this project's
// general fail-closed posture elsewhere (RBAC, migration TLS, etc.).
func (s Scheduler) filterMaxSkew(m model.Machine, candidates []model.Node, nodes []model.Node, machines []model.Machine) []model.Node {
	var hard []model.TopologySpreadConstraint
	for _, c := range m.Spec.Placement.TopologySpreadConstraints {
		if c.WhenUnsatisfiable == model.WhenUnsatisfiableDoNotSchedule {
			hard = append(hard, c)
		}
	}
	if len(hard) == 0 {
		return candidates
	}
	// Domain counts are a property of the whole candidate set for a given
	// constraint, not any one node -- compute each once, rather than
	// recomputing it per candidate node below (minAfterPlacingOn still
	// needs the counts map itself per node, since which domain gets the
	// hypothetical +1 differs per candidate).
	counts := make([]map[string]int, len(hard))
	for i, c := range hard {
		counts[i] = topologyDomainCounts(m, c, candidates, nodes, machines)
	}

	out := make([]model.Node, 0, len(candidates))
	for _, n := range candidates {
		ok := true
		for i, c := range hard {
			v, has := n.Metadata.Labels[c.TopologyKey]
			if !has {
				continue // can't evaluate skew for this node -- no opinion, same posture topologySpreadPenalty/termSatisfied take for a missing topology label
			}
			newCount := counts[i][v] + 1
			if newCount-minAfterPlacingOn(counts[i], v) > int(c.MaxSkew) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, n)
		}
	}
	return out
}

// topologyDomainCounts returns, for constraint c evaluated against
// candidates (nodes already eligible on every other ground -- domains are
// only considered among these, matching real Kubernetes' own "candidate
// nodes matching the pod's node affinity" scope, not every node in the
// cluster), how many other Machines matching c.LabelSelector currently sit
// in each of c.TopologyKey's distinct values among candidates (0 for any
// domain with no matching Machines at all).
func topologyDomainCounts(m model.Machine, c model.TopologySpreadConstraint, candidates []model.Node, nodes []model.Node, machines []model.Machine) map[string]int {
	counts := map[string]int{}
	for _, cand := range candidates {
		if v, ok := cand.Metadata.Labels[c.TopologyKey]; ok {
			if _, seen := counts[v]; !seen {
				counts[v] = 0
			}
		}
	}
	for _, other := range machines {
		if other.Namespace() == m.Namespace() && other.Metadata.Name == m.Metadata.Name {
			continue
		}
		if other.Spec.NodeName == "" || !model.LabelsMatch(other.Metadata.Labels, c.LabelSelector) {
			continue
		}
		if v, ok := nodeLabelValue(nodes, other.Spec.NodeName, c.TopologyKey); ok {
			if _, tracked := counts[v]; tracked {
				counts[v]++
			}
		}
	}
	return counts
}

// minAfterPlacingOn returns the minimum domain count across counts if one
// more Machine were hypothetically placed in domain target -- NOT simply
// the pre-placement global minimum applied uniformly to every candidate,
// which undercounts a domain that was itself the (unique) minimum: placing
// there raises its own count, so the minimum after placement may shift to
// whatever the next-smallest domain was. Mirrors real Kubernetes'
// topology-spread skew definition exactly (recomputed per candidate
// domain, not cached globally), the subtlety a first attempt at this got
// wrong.
func minAfterPlacingOn(counts map[string]int, target string) int {
	min := counts[target] + 1
	found := false
	for k, c := range counts {
		if k == target {
			continue
		}
		if !found || c < min {
			min = c
			found = true
		}
	}
	return min
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
	if m.Spec.Resources.CPUPinning {
		requested, err := model.ParseVCPUs(m.Spec.Resources.CPU)
		if err != nil {
			return false // an unparseable spec.resources.cpu is caught elsewhere with a real error; just not eligible here
		}
		if uint32(len(freePinnableCPUs(n, machines))) < requested {
			return false
		}
	}
	return true
}

// freePinnableCPUs returns a candidate node's PinnableCPUsLabel-asserted
// CPU numbers minus whatever every OTHER already-assigned Machine on that
// node has already exclusively claimed via its own
// spec.resources.allocatedCpuSet -- the real capacity check a
// CPUPinning-requesting Machine's eligibility depends on. A node with no
// (or an unparseable) PinnableCPUsLabel has zero pinnable CPUs -- fail
// closed, the same posture an empty KAIRON_VFIO_ALLOWLIST already has.
func freePinnableCPUs(n model.Node, machines []model.Machine) []uint32 {
	pinnable, err := model.ParseCPUList(n.Metadata.Labels[model.PinnableCPUsLabel])
	if err != nil || len(pinnable) == 0 {
		return nil
	}
	used := map[uint32]struct{}{}
	for _, other := range machines {
		if other.Spec.NodeName != n.Metadata.Name || other.Metadata.DeletionTimestamp != nil {
			continue
		}
		for _, cpu := range other.Spec.Resources.AllocatedCPUSet {
			used[cpu] = struct{}{}
		}
	}
	free := make([]uint32, 0, len(pinnable))
	for _, cpu := range pinnable {
		if _, ok := used[cpu]; !ok {
			free = append(free, cpu)
		}
	}
	return free
}

// AllocateCPUSet picks the exact host CPU numbers a CPUPinning Machine
// exclusively owns on the node Choose already picked -- called once,
// immediately after Choose returns, from the same scheduling pass (so the
// "which node" and "which cores" decisions are made from the same
// snapshot of machines and can be patched atomically by the caller).
// Deterministic first-fit over freePinnableCPUs's own ascending order --
// simple and reproducible, not attempting NUMA-locality-aware selection
// even when spec.resources.numaNode is also set (a real, documented
// first-cut limit: combining cpuPinning with numaNode doesn't
// cross-validate that the chosen cores actually sit in the requested NUMA
// node). Returns an error if the node no longer has enough free CPUs --
// possible if machines changed between eligible's own check and this call
// within the same reconcile pass, though ordinarily eligible already
// guarantees this succeeds.
func AllocateCPUSet(m model.Machine, node model.Node, machines []model.Machine) ([]uint32, error) {
	requested, err := model.ParseVCPUs(m.Spec.Resources.CPU)
	if err != nil {
		return nil, fmt.Errorf("spec.resources.cpu: %w", err)
	}
	free := freePinnableCPUs(node, machines)
	if uint32(len(free)) < requested {
		return nil, fmt.Errorf("node %q has only %d free pinnable CPU(s), %s requests %d", node.Metadata.Name, len(free), m.Metadata.Name, requested)
	}
	return free[:requested], nil
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
