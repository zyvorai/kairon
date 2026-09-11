package admission

import "testing"

import "github.com/zyvorai/kairon/internal/model"

func TestValidateMachine(t *testing.T) {
	if got := ValidateMachine(model.Machine{Spec: model.MachineSpec{}}); got == "" {
		t.Fatal("expected denial without image")
	}
	m := model.Machine{Spec: model.MachineSpec{
		Image:     model.ImageSpec{Path: "/var/lib/fluxvm/images/a.qcow2", Digest: "sha256:abcd"},
		Resources: model.ResourceSpec{CPU: "1", Memory: "1Gi"},
		Storage:   "default",
	}}
	if got := ValidateMachine(m); got == "" {
		t.Fatal("expected short digest denial")
	}
	m.Spec.Image.Digest = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if got := ValidateMachine(m); got != "" {
		t.Fatalf("unexpected denial: %s", got)
	}
}
