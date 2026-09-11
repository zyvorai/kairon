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
