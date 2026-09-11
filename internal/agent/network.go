// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zyvorai/kairon/internal/fluxvm"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

func (a *Agent) reconcileNetworkResources(ctx context.Context) error {
	groups, err := a.Kube.ListNetworkSecurityGroups(ctx)
	if err != nil {
		if kube.IsNotFound(err) {
			return nil
		}
		return err
	}
	for _, g := range groups {
		if err := a.reconcileSecurityGroup(ctx, g); err != nil {
			a.Log.Error("network security group reconcile failed", "namespace", g.Namespace(), "name", g.Metadata.Name, "error", err)
			status := g.Status
			status.Phase = "Error"
			status.Message = err.Error()
			_ = a.Kube.PatchNetworkSecurityGroupStatus(ctx, g.Namespace(), g.Metadata.Name, status)
		}
	}

	policies, err := a.Kube.ListMachineNetworkPolicies(ctx)
	if err != nil {
		if kube.IsNotFound(err) {
			return nil
		}
		return err
	}
	machines, err := a.Kube.ListMachines(ctx)
	if err != nil {
		return err
	}
	for _, p := range policies {
		if err := a.reconcileMachineNetworkPolicy(ctx, p, machines); err != nil {
			a.Log.Error("machine network policy reconcile failed", "namespace", p.Namespace(), "name", p.Metadata.Name, "error", err)
			status := p.Status
			status.Phase = "Error"
			status.Message = err.Error()
			_ = a.Kube.PatchMachineNetworkPolicyStatus(ctx, p.Namespace(), p.Metadata.Name, status)
		}
	}
	return nil
}

func (a *Agent) reconcileSecurityGroup(ctx context.Context, g model.NetworkSecurityGroup) error {
	if g.Metadata.DeletionTimestamp != nil {
		if model.HasFinalizerList(g.Metadata.Finalizers, model.FinalizerNetworkGroup) {
			_ = a.Flux.DeleteNetworkGroup(ctx, g.FluxGroupName())
			finals := model.RemoveFinalizer(g.Metadata.Finalizers, model.FinalizerNetworkGroup)
			return a.Kube.PatchNetworkSecurityGroup(ctx, g.Namespace(), g.Metadata.Name, map[string]any{"metadata": map[string]any{"finalizers": finals}})
		}
		return nil
	}
	if !model.HasFinalizerList(g.Metadata.Finalizers, model.FinalizerNetworkGroup) {
		finals := append(append([]string{}, g.Metadata.Finalizers...), model.FinalizerNetworkGroup)
		if err := a.Kube.PatchNetworkSecurityGroup(ctx, g.Namespace(), g.Metadata.Name, map[string]any{"metadata": map[string]any{"finalizers": finals}}); err != nil {
			return err
		}
	}
	out, err := a.Flux.UpsertNetworkGroup(ctx, fluxvm.SecurityGroup{
		Name:        g.FluxGroupName(),
		Labels:      g.Spec.Labels,
		Policy:      fluxvm.ToWirePolicy(g.Spec.Policy),
		Priority:    g.Spec.Priority,
		Description: g.Spec.Description,
	})
	if err != nil {
		return err
	}
	status := g.Status
	status.Phase = "Applied"
	status.Message = ""
	status.AppliedOn = a.NodeName
	if out != nil {
		status.Identity = out.Identity
	}
	return a.Kube.PatchNetworkSecurityGroupStatus(ctx, g.Namespace(), g.Metadata.Name, status)
}

func (a *Agent) reconcileMachineNetworkPolicy(ctx context.Context, p model.MachineNetworkPolicy, machines []model.Machine) error {
	if p.Metadata.DeletionTimestamp != nil {
		if model.HasFinalizerList(p.Metadata.Finalizers, model.FinalizerNetworkPolicy) {
			for _, m := range machines {
				if m.Spec.NodeName != a.NodeName || m.Namespace() != p.Namespace() {
					continue
				}
				if !policySelectsMachine(p, m) {
					continue
				}
				if m.Status.RuntimeID == "" {
					continue
				}
				_ = a.Flux.SetVMNetworkPolicy(ctx, m.Status.RuntimeID, model.VmNetworkPolicy{DefaultAllow: true})
			}
			finals := model.RemoveFinalizer(p.Metadata.Finalizers, model.FinalizerNetworkPolicy)
			return a.Kube.PatchMachineNetworkPolicy(ctx, p.Namespace(), p.Metadata.Name, map[string]any{"metadata": map[string]any{"finalizers": finals}})
		}
		return nil
	}
	if !model.HasFinalizerList(p.Metadata.Finalizers, model.FinalizerNetworkPolicy) {
		finals := append(append([]string{}, p.Metadata.Finalizers...), model.FinalizerNetworkPolicy)
		if err := a.Kube.PatchMachineNetworkPolicy(ctx, p.Namespace(), p.Metadata.Name, map[string]any{"metadata": map[string]any{"finalizers": finals}}); err != nil {
			return err
		}
	}
	if len(p.Spec.CNP) > 0 {
		if err := a.Flux.ApplyCNP(ctx, p.Spec.CNP); err != nil {
			return fmt.Errorf("apply CNP: %w", err)
		}
	}
	applied := 0
	for _, m := range machines {
		if m.Spec.NodeName != a.NodeName || m.Namespace() != p.Namespace() {
			continue
		}
		if !policySelectsMachine(p, m) {
			continue
		}
		if m.Status.Phase != "Running" || m.Status.RuntimeID == "" {
			continue
		}
		if err := a.Flux.SetVMNetworkPolicy(ctx, m.Status.RuntimeID, p.Spec.Policy); err != nil {
			return fmt.Errorf("set policy on %s/%s: %w", m.Namespace(), m.Metadata.Name, err)
		}
		applied++
	}
	now := time.Now().UTC()
	status := p.Status
	status.Phase = "Applied"
	status.Message = ""
	status.ObservedMachines = applied
	status.LastAppliedTime = &now
	status.EffectiveSynced = applied > 0
	return a.Kube.PatchMachineNetworkPolicyStatus(ctx, p.Namespace(), p.Metadata.Name, status)
}

func policySelectsMachine(p model.MachineNetworkPolicy, m model.Machine) bool {
	if p.Spec.MachineName != "" {
		return p.Spec.MachineName == m.Metadata.Name
	}
	return model.LabelsMatch(m.Metadata.Labels, p.Spec.Selector)
}

func (a *Agent) projectNetworkStatus(ctx context.Context, m model.Machine, rec *fluxvm.Record, status *model.MachineStatus) error {
	guestIP := rec.GuestIP
	if guestIP == "" {
		guestIP = status.GuestIP
	}
	status.GuestIP = guestIP
	netStatus := &model.MachineNetworkStatus{GuestIP: guestIP, TapName: rec.TapName}
	dp, err := a.Flux.NetworkStatus(ctx, rec.ID())
	if err != nil {
		// Soft-fail when dataplane endpoints are unavailable (legacy FluxVM).
		if m.Spec.Network.DataplaneRequired {
			return fmt.Errorf("dataplane required but network status failed: %w", err)
		}
		status.Network = netStatus
		return nil
	}
	netStatus.Dataplane = &model.MachineDataplaneStatus{
		Attached:          dp.Attached,
		Mode:              dp.Mode,
		SchemaVersion:     dp.SchemaVersion,
		Identity:          dp.Identity,
		PolicyFingerprint: fluxvm.PolicyFingerprintString(dp.PolicyFingerprint),
		PolicySynced:      dp.PolicySynced,
		Interface:         dp.Interface,
	}
	status.Network = netStatus
	if m.Spec.Network.DataplaneRequired {
		mode := strings.ToLower(dp.Mode)
		if (mode == "ebpf" || mode == "cilium" || dp.Required) && !dp.Attached {
			return fmt.Errorf("dataplane required but attach unhealthy (mode=%s attached=%v)", dp.Mode, dp.Attached)
		}
	}
	return nil
}

func (a *Agent) reconcileServiceFabric(ctx context.Context, m model.Machine, guestIP string) error {
	if len(m.Spec.ServiceFabric.Services) == 0 || guestIP == "" {
		return nil
	}
	for _, mem := range m.Spec.ServiceFabric.Services {
		name := strings.TrimSpace(mem.Name)
		if name == "" || mem.Port == 0 {
			return fmt.Errorf("serviceFabric.services entries require name and port")
		}
		svc, err := a.Flux.GetNetworkService(ctx, name)
		if err != nil {
			return fmt.Errorf("get service %s: %w", name, err)
		}
		weight := mem.Weight
		if weight == 0 {
			weight = 1
		}
		enabled := true
		found := false
		for i := range svc.Backends {
			if svc.Backends[i].Address == guestIP && svc.Backends[i].Port == mem.Port {
				svc.Backends[i].Weight = weight
				svc.Backends[i].Enabled = &enabled
				svc.Backends[i].State = "ready"
				found = true
				break
			}
		}
		if !found {
			svc.Backends = append(svc.Backends, fluxvm.ServiceBackend{
				Address: guestIP,
				Port:    mem.Port,
				Weight:  weight,
				Enabled: &enabled,
				State:   "ready",
			})
		}
		if err := a.Flux.UpsertNetworkService(ctx, *svc); err != nil {
			return fmt.Errorf("upsert service %s membership: %w", name, err)
		}
	}
	return nil
}
