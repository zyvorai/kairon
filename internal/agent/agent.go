package agent

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/zyvorai/kairon/internal/fluxvm"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

type Agent struct {
	NodeName       string
	Kube           *kube.Client
	Flux           *fluxvm.Client
	DefaultBackend string
	ImageRoot      string
	Log            *slog.Logger
}

func (a *Agent) Reconcile(ctx context.Context) error {
	machines, err := a.Kube.ListMachines(ctx)
	if err != nil {
		return err
	}
	for _, m := range machines {
		if m.Spec.NodeName != a.NodeName {
			continue
		}
		if err := a.reconcileMachine(ctx, m); err != nil {
			a.Log.Error("machine reconcile failed", "namespace", m.Namespace(), "machine", m.Metadata.Name, "error", err)
			status := m.Status
			status.Phase = "Error"
			status.NodeName = a.NodeName
			status.Message = err.Error()
			status.Conditions = []model.Condition{{Type: "Ready", Status: "False", Reason: "ReconcileFailed", Message: err.Error(), LastTransitionTime: time.Now().UTC()}}
			_ = a.Kube.PatchMachineStatus(ctx, m.Namespace(), m.Metadata.Name, status)
		}
	}
	return nil
}

func (a *Agent) reconcileMachine(ctx context.Context, m model.Machine) error {
	if m.Metadata.DeletionTimestamp != nil {
		return a.cleanup(ctx, m)
	}
	if !model.HasFinalizer(m, model.Finalizer) {
		finalizers := append(append([]string{}, m.Metadata.Finalizers...), model.Finalizer)
		if err := a.Kube.PatchMachine(ctx, m.Namespace(), m.Metadata.Name, map[string]any{"metadata": map[string]any{"finalizers": finalizers}}); err != nil {
			return err
		}
	}
	if m.DesiredPowerState() == "Stopped" {
		return a.ensureStopped(ctx, m)
	}
	if m.Spec.Image.Path == "" {
		return fmt.Errorf("spec.image.path is required")
	}
	if a.ImageRoot != "" {
		root, err := filepath.Abs(a.ImageRoot)
		if err != nil {
			return fmt.Errorf("resolve image root: %w", err)
		}
		img, err := filepath.Abs(m.Spec.Image.Path)
		if err != nil {
			return fmt.Errorf("resolve image path: %w", err)
		}
		rel, err := filepath.Rel(root, img)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("image path %q is outside allowed root %q", m.Spec.Image.Path, a.ImageRoot)
		}
	}

	rec, err := a.current(ctx, m)
	if err != nil {
		return err
	}
	if rec == nil {
		rec, err = a.Flux.Create(ctx, m, a.DefaultBackend)
		if err != nil {
			return err
		}
		a.Log.Info("created runtime", "machine", m.Metadata.Name, "runtimeID", rec.ID())
	}
	status := m.Status
	status.Phase = normalizePhase(rec.Status)
	status.NodeName = a.NodeName
	status.RuntimeID = rec.ID()
	status.GuestIP = rec.GuestIP
	status.Message = ""
	status.Conditions = []model.Condition{{Type: "Ready", Status: readyStatus(status.Phase), Reason: "FluxVMReconciled", LastTransitionTime: time.Now().UTC()}}
	return a.Kube.PatchMachineStatus(ctx, m.Namespace(), m.Metadata.Name, status)
}

func (a *Agent) current(ctx context.Context, m model.Machine) (*fluxvm.Record, error) {
	if m.Status.RuntimeID != "" {
		if rec, err := a.Flux.Get(ctx, m.Status.RuntimeID); err == nil {
			return rec, nil
		}
	}
	return a.Flux.LookupByName(ctx, m.RuntimeName())
}

func (a *Agent) ensureStopped(ctx context.Context, m model.Machine) error {
	rec, err := a.current(ctx, m)
	if err != nil {
		return err
	}
	if rec != nil && rec.ID() != "" {
		if err := a.Flux.Delete(ctx, rec.ID()); err != nil {
			return err
		}
	}
	status := m.Status
	status.Phase = "Stopped"
	status.NodeName = a.NodeName
	status.RuntimeID = ""
	status.GuestIP = ""
	status.Message = ""
	status.Conditions = []model.Condition{{Type: "Ready", Status: "False", Reason: "PoweredOff", LastTransitionTime: time.Now().UTC()}}
	return a.Kube.PatchMachineStatus(ctx, m.Namespace(), m.Metadata.Name, status)
}

func (a *Agent) cleanup(ctx context.Context, m model.Machine) error {
	if m.Status.RuntimeID != "" {
		if err := a.Flux.Delete(ctx, m.Status.RuntimeID); err != nil {
			return err
		}
	}
	var finals []string
	for _, f := range m.Metadata.Finalizers {
		if f != model.Finalizer {
			finals = append(finals, f)
		}
	}
	return a.Kube.PatchMachine(ctx, m.Namespace(), m.Metadata.Name, map[string]any{"metadata": map[string]any{"finalizers": finals}})
}

func normalizePhase(s string) string {
	s = strings.ToLower(s)
	switch s {
	case "running", "started", "ready":
		return "Running"
	case "paused":
		return "Paused"
	case "stopped", "exited":
		return "Stopped"
	case "creating", "starting", "pending":
		return "Starting"
	default:
		if s == "" {
			return "Running"
		}
		return "Unknown"
	}
}
func readyStatus(phase string) string {
	if phase == "Running" {
		return "True"
	}
	return "False"
}

func (a *Agent) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := a.Reconcile(ctx); err != nil {
			a.Log.Error("reconcile failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
