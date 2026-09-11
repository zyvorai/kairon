package controller

import (
	"context"
	"log/slog"
	"time"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/scheduler"
)

type Controller struct {
	Kube      *kube.Client
	Scheduler scheduler.Scheduler
	Log       *slog.Logger
}

func (c *Controller) Reconcile(ctx context.Context) error {
	machines, err := c.Kube.ListMachines(ctx)
	if err != nil {
		return err
	}
	nodes, err := c.Kube.ListNodes(ctx)
	if err != nil {
		return err
	}
	assigned := map[string]int{}
	for _, m := range machines {
		if m.Spec.NodeName != "" && m.Metadata.DeletionTimestamp == nil && m.DesiredPowerState() == "Running" {
			assigned[m.Spec.NodeName]++
		}
	}
	for _, m := range machines {
		if m.Metadata.DeletionTimestamp != nil || m.Spec.NodeName != "" || m.DesiredPowerState() == "Stopped" {
			continue
		}
		node, err := c.Scheduler.Choose(m, nodes, assigned)
		if err != nil {
			status := m.Status
			status.Phase = "Pending"
			status.Message = err.Error()
			_ = c.Kube.PatchMachineStatus(ctx, m.Namespace(), m.Metadata.Name, status)
			continue
		}
		if err := c.Kube.PatchMachine(ctx, m.Namespace(), m.Metadata.Name, map[string]any{"spec": map[string]any{"nodeName": node}}); err != nil {
			return err
		}
		assigned[node]++
		c.Log.Info("scheduled machine", "namespace", m.Namespace(), "machine", m.Metadata.Name, "node", node)
	}
	return nil
}

func (c *Controller) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := c.Reconcile(ctx); err != nil {
			c.Log.Error("reconcile failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
