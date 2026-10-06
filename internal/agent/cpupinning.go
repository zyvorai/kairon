// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"

	"github.com/zyvorai/kairon/internal/cpumanager"
	"github.com/zyvorai/kairon/internal/model"
)

// publishPinnableCPUs projects kairon.zyvor.dev/pinnable-cpus from sysfs
// and kubelet cpu_manager_state. An operator-set label is not replaced.
// A failed discovery clears only a label this agent published, so a node
// that can no longer prove its set fails closed for cpuPinning.
func (a *Agent) publishPinnableCPUs(ctx context.Context) {
	if a.Kube == nil || a.NodeName == "" {
		return
	}
	root := a.SysRoot
	if root == "" {
		root = "/sys"
	}
	state := a.CPUManagerState
	if state == "" {
		state = "/var/lib/kubelet/cpu_manager_state"
	}
	got, err := cpumanager.Discover(cpumanager.Input{OnlinePath: root + "/devices/system/cpu/online", StatePath: state, Reserved: a.ReservedCPUs})
	if err != nil {
		a.log().Warn("pinnable cpu discovery failed", "error", err)
		return
	}
	node, err := a.Kube.GetNode(ctx, a.NodeName)
	if err != nil {
		a.log().Debug("node read for pinnable cpus failed", "error", err)
		return
	}
	current := node.Metadata.Labels[model.PinnableCPUsLabel]
	source := node.Metadata.Annotations[cpumanager.AnnSource]
	owned := source == cpumanager.SourceDiscovered
	patch := cpumanager.LabelPatch(current, source, got.List, owned, got.Refused != "")
	if patch == nil {
		return
	}
	if err := a.Kube.PatchNode(ctx, a.NodeName, patch); err != nil {
		a.log().Warn("pinnable cpu label patch failed", "node", a.NodeName, "error", err)
		return
	}
	a.log().Info("pinnable cpu label updated", "node", a.NodeName, "cpus", got.List, "refused", got.Refused)
}
