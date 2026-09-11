package controller

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/zyvorai/kairon/internal/health"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
	"github.com/zyvorai/kairon/internal/scheduler"
)

type Controller struct {
	Kube       *kube.Client
	Scheduler  scheduler.Scheduler
	Log        *slog.Logger
	FenceGrace time.Duration
	Metrics    *health.Metrics
}

func (c *Controller) fenceGrace() time.Duration {
	if c.FenceGrace <= 0 {
		return 60 * time.Second
	}
	return c.FenceGrace
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
	nodeByName := map[string]model.Node{}
	for _, n := range nodes {
		nodeByName[n.Metadata.Name] = n
	}
	if err := c.reconcileMDBs(ctx, machines); err != nil {
		c.Log.Error("mdb reconcile", "error", err)
	}
	if err := c.reconcileFencing(ctx, machines, nodeByName); err != nil {
		return err
	}
	// refresh after fencing may have cleared assignments
	machines, err = c.Kube.ListMachines(ctx)
	if err != nil {
		return err
	}
	assigned := map[string]int{}
	for _, m := range machines {
		if m.Spec.NodeName != "" && m.Metadata.DeletionTimestamp == nil && m.DesiredPowerState() == "Running" {
			assigned[m.Spec.NodeName]++
		}
	}
	now := time.Now().UTC()
	for _, m := range machines {
		if m.Metadata.DeletionTimestamp != nil || m.Spec.NodeName != "" || m.DesiredPowerState() == "Stopped" {
			continue
		}
		node, err := c.Scheduler.Choose(m, nodes, assigned)
		if err != nil {
			status := m.Status
			status.Phase = "Pending"
			status.Message = err.Error()
			status.ObservedGeneration = m.Metadata.Generation
			status.Conditions = model.SetCondition(status.Conditions, model.ConditionScheduled, "False", "FailedScheduling", err.Error(), now)
			status.Conditions = model.SetCondition(status.Conditions, model.ConditionReady, "False", "Unscheduled", err.Error(), now)
			_ = c.Kube.PatchMachineStatus(ctx, m.Namespace(), m.Metadata.Name, status)
			_ = c.Kube.Eventf(ctx, m, "Warning", "FailedScheduling", err.Error(), "kairon-controller")
			continue
		}
		if err := c.Kube.PatchMachine(ctx, m.Namespace(), m.Metadata.Name, map[string]any{"spec": map[string]any{"nodeName": node}}); err != nil {
			return err
		}
		status := m.Status
		status.Phase = "Pending"
		status.NodeName = node
		status.Message = fmt.Sprintf("assigned to %s", node)
		status.ObservedGeneration = m.Metadata.Generation
		status.Conditions = model.SetCondition(status.Conditions, model.ConditionScheduled, "True", "Scheduled", node, now)
		status.Conditions = model.SetCondition(status.Conditions, model.ConditionReady, "False", "WaitingForRuntime", "", now)
		status.Conditions = model.SetCondition(status.Conditions, model.ConditionNodeHealthy, "True", "NodeReady", node, now)
		_ = c.Kube.PatchMachineStatus(ctx, m.Namespace(), m.Metadata.Name, status)
		_ = c.Kube.Eventf(ctx, m, "Normal", "Scheduled", fmt.Sprintf("Successfully assigned to %s", node), "kairon-controller")
		assigned[node]++
		c.Log.Info("scheduled machine", "namespace", m.Namespace(), "machine", m.Metadata.Name, "node", node)
		if c.Metrics != nil {
			c.Metrics.MachinesScheduled.Add(1)
		}
	}
	return nil
}

func nodeReady(n model.Node) bool {
	if n.Spec.Unschedulable {
		return false
	}
	for _, c := range n.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	return false
}

func (c *Controller) reconcileFencing(ctx context.Context, machines []model.Machine, nodes map[string]model.Node) error {
	now := time.Now().UTC()
	grace := c.fenceGrace()
	for _, m := range machines {
		if m.Metadata.DeletionTimestamp != nil || m.Spec.NodeName == "" {
			continue
		}
		// Voluntary evacuate
		if m.Metadata.Annotations[model.AnnotationEvacuate] == "true" {
			if !c.disruptionAllowed(ctx, m) {
				status := m.Status
				status.Message = "evacuation blocked by MachineDisruptionBudget"
				status.Conditions = model.SetCondition(status.Conditions, model.ConditionScheduled, "True", "EvacuateBlocked", status.Message, now)
				_ = c.Kube.PatchMachineStatus(ctx, m.Namespace(), m.Metadata.Name, status)
				_ = c.Kube.Eventf(ctx, m, "Warning", "EvacuateBlocked", status.Message, "kairon-controller")
				continue
			}
			if err := c.releaseMachine(ctx, m, "Evacuated", "voluntary evacuate annotation"); err != nil {
				return err
			}
			continue
		}
		n, ok := nodes[m.Spec.NodeName]
		healthy := ok && nodeReady(n)
		if healthy {
			ann := m.Metadata.Annotations
			if ann != nil && ann[model.AnnotationFenceSince] != "" {
				_ = c.Kube.PatchMachine(ctx, m.Namespace(), m.Metadata.Name, map[string]any{"metadata": map[string]any{"annotations": map[string]any{
					model.AnnotationFenceSince: nil,
				}}})
			}
			if model.ConditionStatus(m.Status.Conditions, model.ConditionNodeHealthy) != "True" {
				status := m.Status
				status.Conditions = model.SetCondition(status.Conditions, model.ConditionNodeHealthy, "True", "NodeReady", m.Spec.NodeName, now)
				_ = c.Kube.PatchMachineStatus(ctx, m.Namespace(), m.Metadata.Name, status)
			}
			continue
		}
		reason := "NodeNotReady"
		if !ok {
			reason = "NodeMissing"
		}
		status := m.Status
		status.Conditions = model.SetCondition(status.Conditions, model.ConditionNodeHealthy, "False", reason, m.Spec.NodeName, now)
		status.Phase = "Unknown"
		status.Message = fmt.Sprintf("node %s unhealthy (%s); fencing after %s", m.Spec.NodeName, reason, grace)
		_ = c.Kube.PatchMachineStatus(ctx, m.Namespace(), m.Metadata.Name, status)

		ann := m.Metadata.Annotations
		if ann == nil {
			ann = map[string]string{}
		}
		sinceStr := ann[model.AnnotationFenceSince]
		if sinceStr == "" {
			ann[model.AnnotationFenceSince] = strconv.FormatInt(now.Unix(), 10)
			_ = c.Kube.PatchMachine(ctx, m.Namespace(), m.Metadata.Name, map[string]any{"metadata": map[string]any{"annotations": ann}})
			_ = c.Kube.Eventf(ctx, m, "Warning", "NodeUnhealthy", status.Message, "kairon-controller")
			continue
		}
		sinceUnix, err := strconv.ParseInt(sinceStr, 10, 64)
		if err != nil {
			ann[model.AnnotationFenceSince] = strconv.FormatInt(now.Unix(), 10)
			_ = c.Kube.PatchMachine(ctx, m.Namespace(), m.Metadata.Name, map[string]any{"metadata": map[string]any{"annotations": ann}})
			continue
		}
		if now.Sub(time.Unix(sinceUnix, 0).UTC()) < grace {
			continue
		}
		if err := c.releaseMachine(ctx, m, "Fenced", fmt.Sprintf("fenced from unhealthy node %s", m.Spec.NodeName)); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) releaseMachine(ctx context.Context, m model.Machine, reason, message string) error {
	now := time.Now().UTC()
	patch := map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{
			model.AnnotationFenceSince: nil,
			model.AnnotationEvacuate:   nil,
		}},
		"spec": map[string]any{"nodeName": nil},
	}
	if err := c.Kube.PatchMachine(ctx, m.Namespace(), m.Metadata.Name, patch); err != nil {
		return err
	}
	status := m.Status
	status.Phase = "Pending"
	status.NodeName = ""
	status.RuntimeID = ""
	status.GuestIP = ""
	status.Message = message
	status.ObservedGeneration = m.Metadata.Generation
	status.Conditions = model.SetCondition(status.Conditions, model.ConditionScheduled, "False", reason, message, now)
	status.Conditions = model.SetCondition(status.Conditions, model.ConditionCreated, "False", reason, "", now)
	status.Conditions = model.SetCondition(status.Conditions, model.ConditionReady, "False", reason, message, now)
	status.Conditions = model.SetCondition(status.Conditions, model.ConditionNodeHealthy, "False", reason, message, now)
	_ = c.Kube.PatchMachineStatus(ctx, m.Namespace(), m.Metadata.Name, status)
	_ = c.Kube.Eventf(ctx, m, "Warning", reason, message, "kairon-controller")
	c.Log.Info("released machine placement", "machine", m.Metadata.Name, "reason", reason)
	if c.Metrics != nil && reason == "Fenced" {
		c.Metrics.MachinesFenced.Add(1)
	}
	return nil
}

func (c *Controller) reconcileMDBs(ctx context.Context, machines []model.Machine) error {
	mdbs, err := c.Kube.ListMachineDisruptionBudgets(ctx)
	if err != nil {
		return err
	}
	for _, mdb := range mdbs {
		healthy := 0
		desired := 0
		for _, m := range machines {
			if m.Namespace() != mdb.Namespace() {
				continue
			}
			if !model.LabelsMatch(mdb.Spec.Selector, m.Metadata.Labels) {
				continue
			}
			desired++
			if m.Status.Phase == "Running" && model.ConditionStatus(m.Status.Conditions, model.ConditionReady) == "True" {
				healthy++
			}
		}
		maxUn := mdb.Spec.MaxUnavailable
		if maxUn < 0 {
			maxUn = 0
		}
		allowed := maxUn - (desired - healthy)
		if allowed < 0 {
			allowed = 0
		}
		st := model.MachineDisruptionBudgetStatus{
			CurrentHealthy:     healthy,
			DesiredHealthy:     desired,
			DisruptionsAllowed: allowed,
			ObservedGeneration: mdb.Metadata.Generation,
		}
		_ = c.Kube.PatchMachineDisruptionBudgetStatus(ctx, mdb.Namespace(), mdb.Metadata.Name, st)
	}
	return nil
}

func (c *Controller) disruptionAllowed(ctx context.Context, m model.Machine) bool {
	mdbs, err := c.Kube.ListMachineDisruptionBudgets(ctx)
	if err != nil {
		return false
	}
	for _, mdb := range mdbs {
		if mdb.Namespace() != m.Namespace() {
			continue
		}
		if !model.LabelsMatch(mdb.Spec.Selector, m.Metadata.Labels) {
			continue
		}
		if mdb.Status.DisruptionsAllowed <= 0 {
			return false
		}
	}
	return true
}

func (c *Controller) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := c.Reconcile(ctx); err != nil {
			if c.Metrics != nil {
				c.Metrics.ReconcileErrors.Add(1)
			}
			c.Log.Error("reconcile failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
