// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
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

// reconcileSecurityGroup drives a NetworkSecurityGroup's FluxVM-side
// state, including its own deletion. On delete, fails closed exactly
// like Agent.cleanup's own Machine-runtime-delete-then-finalizer-removal
// sequence: the finalizer is only removed once
// Flux.DeleteNetworkGroup actually succeeds (idempotently tolerating
// "already gone"), so a real delete failure (FluxVM node unreachable, a
// transient error) leaves the finalizer in place and the object gets
// reconciled -- and retried -- again next tick, rather than silently
// vanishing from Kubernetes while its FluxVM-side security group state
// leaks behind, untracked and unreachable through this object again.
func (a *Agent) reconcileSecurityGroup(ctx context.Context, g model.NetworkSecurityGroup) error {
	if g.Metadata.DeletionTimestamp != nil {
		if model.HasFinalizerList(g.Metadata.Finalizers, model.FinalizerNetworkGroup) {
			if err := a.Flux.DeleteNetworkGroup(ctx, g.FluxGroupName()); err != nil {
				return fmt.Errorf("delete FluxVM network group: %w", err)
			}
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
				// Fail closed on a real reset error -- matches
				// NetworkSecurityGroup's own delete-error handling.
				// Removing the finalizer here regardless would let this
				// object vanish from Kubernetes while a selected Machine's
				// VM keeps running under this policy's now-stale
				// restriction, with nothing left in Kubernetes to retry the
				// reset through. SetVMNetworkPolicy itself tolerates a 404
				// (the VM is already gone) as nothing-to-reset, so this
				// only ever blocks the finalizer's removal on a genuine
				// failure, retried automatically next tick.
				if err := a.Flux.SetVMNetworkPolicy(ctx, m.Status.RuntimeID, model.VmNetworkPolicy{DefaultAllow: true}); err != nil {
					return fmt.Errorf("reset network policy on %s/%s: %w", m.Namespace(), m.Metadata.Name, err)
				}
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
	allConfirmed := true
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
		// Read the policy back rather than trusting the POST alone --
		// FluxVM's own policy engine could in principle normalize or
		// reject part of what was sent without surfacing an HTTP error.
		// A read-back failure (e.g. an older FluxVM predating this GET
		// route) just leaves EffectiveSynced honestly false, the same
		// soft-fail posture the dataplane status projection above
		// already takes for a "legacy FluxVM" gap.
		got, err := a.Flux.GetVMNetworkPolicy(ctx, m.Status.RuntimeID)
		if err != nil || !reflect.DeepEqual(*got, p.Spec.Policy) {
			allConfirmed = false
		}
	}
	now := time.Now().UTC()
	status := p.Status
	status.Phase = "Applied"
	status.Message = ""
	status.ObservedMachines = applied
	status.LastAppliedTime = &now
	// EffectiveSynced is now a real confirmation (read the policy back
	// from every applied Machine and compare), not just "the POST
	// succeeded on at least one Machine" -- see GetVMNetworkPolicy's own
	// doc comment for why a read-back is the only real way to know.
	status.EffectiveSynced = applied > 0 && allConfirmed
	return a.Kube.PatchMachineNetworkPolicyStatus(ctx, p.Namespace(), p.Metadata.Name, status)
}

func policySelectsMachine(p model.MachineNetworkPolicy, m model.Machine) bool {
	if p.Spec.MachineName != "" {
		return p.Spec.MachineName == m.Metadata.Name
	}
	return model.LabelsMatch(m.Metadata.Labels, p.Spec.Selector)
}

// guestAgentRecheckInterval bounds how often projectNetworkStatus re-queries
// the guest agent once a guestIP has already been resolved at least once --
// unlike the initial resolution (retried every reconcile tick, since the
// guest agent may simply not have booted yet), re-verifying an
// already-known-good address doesn't need reconcile-tick granularity, and
// polling qemu-guest-agent over virtio-serial every few seconds forever for
// an address that essentially never changes would be needless overhead.
const guestAgentRecheckInterval = 5 * time.Minute

func (a *Agent) projectNetworkStatus(ctx context.Context, m model.Machine, rec *fluxvm.Record, status *model.MachineStatus) error {
	guestIP := rec.GuestIP
	var guestIPs []string
	if guestIP == "" {
		// Preserve whatever was already resolved on a prior tick -- m.Status
		// is this reconcile's untouched snapshot of the last-persisted
		// status, unlike *status, which the caller already overwrote with
		// this tick's (possibly empty) rec.GuestIP before calling here.
		guestIP = m.Status.GuestIP
		guestIPs = m.Status.GuestIPs
	}
	if rec.GuestIP == "" && m.Spec.GuestAgent.Enabled {
		// No DHCP lease to read (spec.network.mode: user in particular has
		// none at all) -- ask the real qemu-guest-agent instead, if the
		// Machine opted in. Retried every tick until the first successful
		// resolution (the guest agent may not have booted yet -- a failure
		// here is expected and transient early in a VM's life, not a
		// reconcile error); once resolved at least once, only re-verified
		// every guestAgentRecheckInterval, so a long-lived VM's address
		// change is eventually noticed without hammering QGA forever.
		key := m.Namespace() + "/" + m.Metadata.Name
		due := guestIP == ""
		if !due {
			last, checkedBefore := a.guestIPCheckedAt[key]
			due = !checkedBefore || time.Since(last) >= guestAgentRecheckInterval
		}
		if due {
			if resolved, err := a.Flux.QGANetworkInterfaces(ctx, rec.ID()); err != nil {
				a.Log.Debug("qga guest IP resolution failed, will retry next tick", "machine", m.Metadata.Name, "error", err)
			} else {
				if a.guestIPCheckedAt == nil {
					a.guestIPCheckedAt = map[string]time.Time{}
				}
				a.guestIPCheckedAt[key] = time.Now()
				if all := fluxvm.AllGuestIPs(resolved); len(all) > 0 {
					guestIPs = all
					// rec.Request.Network.MAC is FluxVM's own record of the
					// primary virtio-net NIC's assigned MAC -- distinguishes
					// it from an SR-IOV VFIO NIC (spec.deviceClaims), which
					// keeps its own real hardware MAC and could otherwise
					// nondeterministically win status.guestIP. See
					// BestGuestIPWithPrimary's own doc comment.
					guestIP = fluxvm.BestGuestIPWithPrimary(resolved, rec.Request.Network.MAC)
				}
			}
		}
	}
	status.GuestIP = guestIP
	status.GuestIPs = guestIPs
	netStatus := &model.MachineNetworkStatus{GuestIP: guestIP, GuestIPs: guestIPs, TapName: rec.TapName}
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

// reconcileServiceFabric merges guestIP into every VIP backend list
// m.Spec.ServiceFabric declares, then -- unlike before this pruning
// existed -- also removes any backend entry Kairon previously applied for
// this Machine (m.Status.AppliedServiceFabricMemberships, the prior
// tick's return value from here) that isn't part of the freshly-applied
// set below. That covers two real, guaranteed-to-happen cases neither
// deregisterServiceFabric's own delete/stop/halt callers ever see:
//   - a membership removed (or renamed/reported) from spec while the
//     Machine keeps running -- reconcileServiceFabric itself is the only
//     thing that ever added it, so it's the only thing that can know it's
//     gone;
//   - guestIP itself changing while the Machine keeps running -- a DHCP
//     re-lease, or a guest reboot landing on a new address. Before this,
//     a new guestIP was simply appended as an additional backend (no
//     existing entry matches its address), and the *old* address's
//     backend entry was never found again by anything to remove -- it
//     stayed registered, routing live VIP traffic at a now-dead address
//     forever, or -- worse, since guest addresses are commonly
//     DHCP-leased and get reused -- at a completely unrelated Machine
//     that later boots into that same address.
//
// Returns the freshly-applied set on success so the caller can persist it
// as the new ground truth in status. Fails closed like every other
// Service Fabric step in this file: a genuine error (upserting the new
// membership, or pruning a stale one) is returned before the caller ever
// commits a new applied set, so the previous one -- still accurate,
// since nothing here partially committed past the failure -- is retried
// again next tick.
func (a *Agent) reconcileServiceFabric(ctx context.Context, m model.Machine, guestIP string) ([]model.AppliedServiceFabricMembership, error) {
	previous := m.Status.AppliedServiceFabricMemberships
	var applied []model.AppliedServiceFabricMembership
	if guestIP != "" {
		for _, mem := range m.Spec.ServiceFabric.Services {
			name := strings.TrimSpace(mem.Name)
			if name == "" || mem.Port == 0 {
				return previous, fmt.Errorf("serviceFabric.services entries require name and port")
			}
			svc, err := a.Flux.GetNetworkService(ctx, name)
			if err != nil {
				return previous, fmt.Errorf("get service %s: %w", name, err)
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
				return previous, fmt.Errorf("upsert service %s membership: %w", name, err)
			}
			applied = append(applied, model.AppliedServiceFabricMembership{Name: name, Port: mem.Port, GuestIP: guestIP})
		}
	}

	stillApplied := make(map[string]struct{}, len(applied))
	for _, am := range applied {
		stillApplied[serviceFabricMembershipKey(am)] = struct{}{}
	}
	for _, prev := range previous {
		if _, ok := stillApplied[serviceFabricMembershipKey(prev)]; ok {
			continue
		}
		if err := a.removeServiceFabricBackend(ctx, prev.Name, prev.Port, prev.GuestIP); err != nil {
			return previous, fmt.Errorf("prune stale service fabric membership %s: %w", prev.Name, err)
		}
	}
	return applied, nil
}

func serviceFabricMembershipKey(m model.AppliedServiceFabricMembership) string {
	return m.Name + "\x00" + m.GuestIP + "\x00" + strconv.Itoa(int(m.Port))
}

// deregisterServiceFabric removes every backend entry Kairon has actually
// applied for this Machine (m.Status.AppliedServiceFabricMemberships) --
// the inverse of reconcileServiceFabric's upsert above. Called from
// cleanup (Machine deletion), ensureStopped (spec.powerState: Stopped) and
// ensureHalted (spec.powerState: Halted): the three places a Machine's
// guest is gone for good without reconcileServiceFabric itself ever
// running again to notice -- cleanup's Machine object is about to vanish
// from Kubernetes entirely, and ensureStopped/ensureHalted both clear
// status.guestIP, leaving nothing afterwards to reconcile membership
// against. Left alone, FluxVM's Service Fabric VIP keeps routing live
// traffic at an address that now answers with nothing (Stopped), or --
// worse, since guest addresses are commonly DHCP-leased and get reused --
// at a completely unrelated Machine that later boots into that same
// address and silently inherits this one's stale membership.
//
// Deliberately reads status.AppliedServiceFabricMemberships rather than
// m.Spec.ServiceFabric.Services (an earlier version of this function did,
// and only ever removed a backend still named in the *current* spec):
// status is the ground truth of what was actually registered, including
// the exact guestIP each entry was registered against, so a membership
// edited or removed from spec while the Machine kept running is torn
// down correctly here too, not just left for reconcileServiceFabric's own
// pruning to have already caught (which it always will have, for a
// Machine that stayed Running the whole time -- this function only ever
// runs for the delete/stop/halt transition itself).
//
// Fails closed like every other FluxVM-side cleanup step in this file
// (reconcileSecurityGroup's delete, reconcileMachineNetworkPolicy's
// reset): a genuine deregistration error is returned to the caller, so
// cleanup's finalizer removal / ensureStopped's status patch never
// completes and this is retried again next tick, rather than silently
// leaking the backend entry forever. A service that's already gone, or
// a backend already removed (a prior tick's retry, or one that was
// never registered because guestIP was empty for this Machine's whole
// life), is nothing left to do -- not an error.
func (a *Agent) deregisterServiceFabric(ctx context.Context, m model.Machine) error {
	for _, mem := range m.Status.AppliedServiceFabricMemberships {
		if err := a.removeServiceFabricBackend(ctx, mem.Name, mem.Port, mem.GuestIP); err != nil {
			return fmt.Errorf("deregister service %s membership: %w", mem.Name, err)
		}
	}
	return nil
}

// removeServiceFabricBackend removes the single (address, port) backend
// entry from the named FluxVM Service Fabric VIP, if both the service and
// that exact backend still exist -- the shared primitive both
// deregisterServiceFabric (delete/stop/halt) and reconcileServiceFabric's
// own stale-membership pruning (a live spec edit or guestIP change) use
// to actually retract a previously-applied membership.
func (a *Agent) removeServiceFabricBackend(ctx context.Context, name string, port uint16, address string) error {
	name = strings.TrimSpace(name)
	if name == "" || address == "" {
		return nil
	}
	svc, err := a.Flux.GetNetworkServiceIfExists(ctx, name)
	if err != nil {
		return fmt.Errorf("get service %s: %w", name, err)
	}
	if svc == nil {
		return nil
	}
	idx := -1
	for i := range svc.Backends {
		if svc.Backends[i].Address == address && svc.Backends[i].Port == port {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil
	}
	svc.Backends = append(svc.Backends[:idx], svc.Backends[idx+1:]...)
	if err := a.Flux.UpsertNetworkService(ctx, *svc); err != nil {
		return fmt.Errorf("upsert service %s: %w", name, err)
	}
	return nil
}
