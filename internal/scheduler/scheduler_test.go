package scheduler

import (
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

func TestChooseRespectsTaintToleration(t *testing.T) {
	m := model.Machine{
		Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"},
		Spec: model.MachineSpec{Placement: model.PlacementSpec{
			Tolerations: []model.Toleration{{Key: "dedicated", Operator: "Equal", Value: "gpu", Effect: "NoSchedule"}},
		}},
	}
	n := node("gpu-1", true, true)
	n.Spec.Taints = []model.Taint{{Key: "dedicated", Value: "gpu", Effect: "NoSchedule"}}
	s := Scheduler{RequireCapableLabel: true}
	got, err := s.Choose(m, []model.Node{n}, map[string]int{})
	if err != nil || got != "gpu-1" {
		t.Fatalf("got %q err=%v", got, err)
	}
	_, err = s.Choose(model.Machine{Metadata: model.ObjectMeta{Name: "x"}}, []model.Node{n}, map[string]int{})
	if err == nil {
		t.Fatal("expected untolerated taint to fail")
	}
}

func TestChooseAffinity(t *testing.T) {
	m := model.Machine{
		Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"},
		Spec: model.MachineSpec{Placement: model.PlacementSpec{
			Affinity: &model.Affinity{NodeAffinity: &model.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &model.NodeSelector{
					NodeSelectorTerms: []model.NodeSelectorTerm{{
						MatchExpressions: []model.NodeSelectorRequirement{
							{Key: "zone", Operator: "In", Values: []string{"z1"}},
						},
					}},
				},
			}},
		}},
	}
	a := node("a", true, true)
	a.Metadata.Labels["zone"] = "z2"
	b := node("b", true, true)
	b.Metadata.Labels["zone"] = "z1"
	s := Scheduler{RequireCapableLabel: true}
	got, err := s.Choose(m, []model.Node{a, b}, map[string]int{})
	if err != nil || got != "b" {
		t.Fatalf("got %q err=%v", got, err)
	}
}
