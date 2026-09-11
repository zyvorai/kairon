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
