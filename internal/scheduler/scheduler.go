package scheduler

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"github.com/zyvorai/kairon/internal/model"
)

type Scheduler struct {
	RequireCapableLabel bool
}

func (s Scheduler) Choose(m model.Machine, nodes []model.Node, assigned map[string]int) (string, error) {
	var eligible []model.Node
	for _, n := range nodes {
		if s.eligible(m, n) {
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

func (s Scheduler) eligible(m model.Machine, n model.Node) bool {
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
	if !tolerates(m.Spec.Placement.Tolerations, n.Spec.Taints) {
		return false
	}
	if !matchesAffinity(m.Spec.Placement.Affinity, n) {
		return false
	}
	return true
}

func ready(n model.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	return false
}

func tolerates(tolerations []model.Toleration, taints []model.Taint) bool {
	for _, taint := range taints {
		if taint.Effect != "" && taint.Effect != "NoSchedule" && taint.Effect != "NoExecute" {
			continue
		}
		if !tolerationMatches(tolerations, taint) {
			return false
		}
	}
	return true
}

func tolerationMatches(tolerations []model.Toleration, taint model.Taint) bool {
	for _, tol := range tolerations {
		if tol.Effect != "" && tol.Effect != taint.Effect {
			continue
		}
		op := strings.ToLower(tol.Operator)
		if op == "" {
			op = "equal"
		}
		switch op {
		case "exists":
			if tol.Key == "" || tol.Key == taint.Key {
				return true
			}
		case "equal":
			if tol.Key == taint.Key && tol.Value == taint.Value {
				return true
			}
		}
	}
	return false
}

func matchesAffinity(aff *model.Affinity, n model.Node) bool {
	if aff == nil || aff.NodeAffinity == nil || aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return true
	}
	terms := aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) == 0 {
		return true
	}
	for _, term := range terms {
		if matchTerm(term, n) {
			return true
		}
	}
	return false
}

func matchTerm(term model.NodeSelectorTerm, n model.Node) bool {
	if len(term.MatchExpressions) == 0 {
		return true
	}
	for _, req := range term.MatchExpressions {
		if !matchRequirement(req, n) {
			return false
		}
	}
	return true
}

func matchRequirement(req model.NodeSelectorRequirement, n model.Node) bool {
	val, ok := n.Metadata.Labels[req.Key]
	switch req.Operator {
	case "Exists":
		return ok
	case "DoesNotExist":
		return !ok
	case "In":
		if !ok {
			return false
		}
		for _, v := range req.Values {
			if v == val {
				return true
			}
		}
		return false
	case "NotIn":
		if !ok {
			return true
		}
		for _, v := range req.Values {
			if v == val {
				return false
			}
		}
		return true
	default:
		return false
	}
}
