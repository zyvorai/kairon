// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kaironctl

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
	"github.com/zyvorai/kairon/internal/nodeliveness"
)

func cmdFence(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl fence MACHINE --reason REASON"))
	}
	name := args[0]
	fs := flag.NewFlagSet("fence", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	reason := fs.String("reason", "", "your out-of-band evidence the node is truly gone, not just unreachable (required)")
	livenessLeaseNamespace := fs.String("liveness-lease-namespace", "", "namespace holding kairon-node's own liveness Leases (see node.livenessLease.enabled); when set, fence cross-checks the fenced node's own Lease against ConditionNodeUnreachable and refuses if kairon-node itself still looks alive there -- empty (the default) skips this extra check entirely, exactly kaironctl fence's behavior before it existed")
	forceIgnoreLiveness := fs.Bool("force-ignore-liveness", false, "proceed even if the liveness lease cross-check above would refuse")
	_ = fs.Parse(args[1:])
	if *reason == "" {
		fatal(fmt.Errorf("--reason is required: state the out-of-band evidence you have that the node is truly gone"))
	}

	m, err := kc.GetMachine(ctx, *ns, name)
	if err != nil {
		fatal(fmt.Errorf("get machine %s/%s: %w", *ns, name, err))
	}
	cond, found := findMachineCondition(m.Status.Conditions, model.ConditionNodeUnreachable)
	if !found || cond.Status != "True" {
		fatal(fmt.Errorf("machine %s/%s does not currently have %s=True -- nothing to fence (its node looks Ready to kairon-controller)", *ns, name, model.ConditionNodeUnreachable))
	}
	fencedNode := m.Spec.NodeName
	// ConditionNodeUnreachable is purely a kubelet Node-Ready-derived
	// signal (internal/controller/fencing.go) -- kubelet can flap
	// NotReady (a brief network blip, an apiserver hiccup) while
	// kairon-node's own reconcile loop keeps running fine on that node.
	// When an operator has opted a node into node.livenessLease.enabled,
	// cross-check kairon-node's own independent liveness signal before
	// trusting Node Ready alone -- see internal/nodeliveness's own doc
	// comment for the full reasoning. Skipped entirely (falls back to
	// today's exact behavior) when --liveness-lease-namespace isn't set,
	// or when no Lease is found for this node (liveness leases disabled
	// on it, or it never came up) -- this is an additional safety gate on
	// top of the existing check, never a replacement for it.
	if *livenessLeaseNamespace != "" {
		lease, err := kc.GetLease(ctx, *livenessLeaseNamespace, nodeliveness.LeaseName(fencedNode))
		switch {
		case kube.IsNotFound(err):
			// No liveness signal recorded for this node -- proceed exactly
			// as before this check existed.
		case err != nil:
			fmt.Fprintf(os.Stderr, "warning: could not read kairon-node's liveness lease for node %q (%v) -- proceeding without this extra check\n", fencedNode, err)
		default:
			if nodeliveness.IsFresh(lease, 0) && !*forceIgnoreLiveness {
				fatal(fmt.Errorf("refusing to fence: kairon-node on node %q renewed its own liveness lease recently, despite %s=True -- kairon-node's reconcile loop may still be alive and actively managing this Machine, and fencing now risks abandoning a still-live VM instead of a truly dead one; pass --force-ignore-liveness if you are certain this is safe", fencedNode, model.ConditionNodeUnreachable))
			}
		}
	}
	okf("fencing machine/%s off node %q (kairon-controller's last-observed reason: %s)", name, fencedNode, cond.Message)

	if err := kc.PatchMachine(ctx, *ns, name, map[string]any{"spec": map[string]any{"nodeName": ""}}); err != nil {
		fatal(fmt.Errorf("clear spec.nodeName on %s/%s: %w", *ns, name, err))
	}
	status := m.Status
	status.Phase = ""
	status.NodeName = ""
	status.RuntimeID = ""
	status.GuestIP = ""
	status.GuestIPs = nil
	status.Network = nil
	status.AppliedVCPUs = 0
	status.AppliedMemoryMiB = 0
	status.Conditions = setMachineCondition(status.Conditions, model.Condition{
		Type: model.ConditionFenced, Status: "True", Reason: "OperatorAttested",
		Message: fmt.Sprintf("fenced off node %q by an operator: %s", fencedNode, *reason), LastTransitionTime: time.Now().UTC(),
	})
	if err := kc.PatchMachineStatus(ctx, *ns, name, status); err != nil {
		fatal(fmt.Errorf("clear runtime status on %s/%s: %w", *ns, name, err))
	}
	okf("machine/%s: spec.nodeName cleared; will be rescheduled onto a different node on kairon-controller's next reconcile tick", name)
}

func findMachineCondition(conditions []model.Condition, condType string) (model.Condition, bool) {
	for _, c := range conditions {
		if c.Type == condType {
			return c, true
		}
	}
	return model.Condition{}, false
}

func setMachineCondition(conditions []model.Condition, cond model.Condition) []model.Condition {
	out := make([]model.Condition, 0, len(conditions)+1)
	replaced := false
	for _, c := range conditions {
		if c.Type == cond.Type {
			out = append(out, cond)
			replaced = true
			continue
		}
		out = append(out, c)
	}
	if !replaced {
		out = append(out, cond)
	}
	return out
}
