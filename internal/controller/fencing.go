// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	"github.com/zyvorai/kairon/internal/model"
)

// detectUnreachableNodes keeps model.ConditionNodeUnreachable in sync with
// reality every reconcile tick: True when a Machine's spec.nodeName no
// longer names a Ready, present Kubernetes Node; False once it's Ready
// again. This is detection only -- see ConditionNodeUnreachable's own doc
// comment in internal/model/types.go for why nothing here automatically
// reschedules the Machine (doing so without confirming the node is
// actually dead, not just unreachable, risks running the same VM twice --
// exactly what NeedsRecovery exists to prevent for migrations). An
// operator who has confirmed out-of-band that the node is truly gone uses
// `kaironctl fence` to clear spec.nodeName and let normal scheduling pick
// it up on a different node -- see cmd/kaironctl/main.go's cmdFence.
//
// Only patches a Machine when its condition actually needs to change (a
// transition either way), not every tick -- avoids a status write storm
// for the overwhelmingly common "everything is fine" case. A patch
// failure is logged and skipped, same as every other best-effort status
// update in this file: a fencing signal one tick stale is far less
// harmful than letting one failed Machine stop the whole reconcile pass.
func (c *Controller) detectUnreachableNodes(ctx context.Context, machines []model.Machine, nodes []model.Node) {
	readyNodes := map[string]bool{}
	for _, n := range nodes {
		if nodeReady(n) {
			readyNodes[n.Metadata.Name] = true
		}
	}
	for _, m := range machines {
		if m.Metadata.DeletionTimestamp != nil || m.Spec.NodeName == "" {
			continue
		}
		unreachable := !readyNodes[m.Spec.NodeName]
		current, found := model.FindCondition(m.Status.Conditions, model.ConditionNodeUnreachable)
		currentlyTrue := found && current.Status == "True"
		if unreachable == currentlyTrue {
			continue
		}
		status, reason, message := "False", "NodeReady", fmt.Sprintf("node %q is Ready", m.Spec.NodeName)
		if unreachable {
			status = "True"
			reason = "NodeNotReadyOrMissing"
			message = fmt.Sprintf("node %q is not Ready or no longer exists in the cluster; this Machine will NOT be automatically rescheduled -- see \"kaironctl fence\" only once you've confirmed out-of-band that the node is truly gone, not just unreachable", m.Spec.NodeName)
		}
		newStatus := m.Status
		newStatus.Conditions = model.SetCondition(m.Status.Conditions, model.Condition{
			Type: model.ConditionNodeUnreachable, Status: status, Reason: reason, Message: message,
		})
		if err := c.Kube.PatchMachineStatus(ctx, m.Namespace(), m.Metadata.Name, newStatus); err != nil {
			c.Log.Error("machine status patch failed (node reachability)", "namespace", m.Namespace(), "machine", m.Metadata.Name, "error", err)
			continue
		}
		if unreachable {
			c.Log.Warn("machine's node is unreachable", "namespace", m.Namespace(), "machine", m.Metadata.Name, "node", m.Spec.NodeName)
		}
	}
}

// nodeReady deliberately duplicates internal/scheduler's own unexported
// ready() rather than importing it -- same "each consumer's own narrower
// copy" precedent this codebase already uses for isTerminalMigrationPhase,
// not a shared cross-package dependency worth taking for one boolean.
func nodeReady(n model.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	return false
}
