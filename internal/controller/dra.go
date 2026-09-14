// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

// buildDRAHints resolves, for every Machine with spec.deviceClaims, which
// node (if any) internal/scheduler should prefer -- a best-effort DRA
// topology-awareness signal, not an authoritative placement decision:
// kairon-controller has no real role in DRA allocation itself (that's the
// cluster's own DRA driver/scheduler-plugin machinery, entirely outside
// this project), it only reads back an allocation that already happened
// and nudges the Machine toward whichever node hosts it. Returns a
// "namespace/name" -> node name map; a Machine absent from it (including
// every Machine with no deviceClaims at all) gets no hint --
// draPreferredNode == "" is the overwhelmingly common path into
// scheduler.Choose and adds no scheduling behavior at all.
//
// Both ResourceClaim/ResourceSlice API calls are skipped entirely -- not
// even attempted -- unless at least one Machine actually has deviceClaims
// set, so a cluster that simply doesn't have the DRA API installed/enabled
// never sees an error from this.
func (c *Controller) buildDRAHints(ctx context.Context, machines []model.Machine) (map[string]string, error) {
	anyDeviceClaims := false
	for _, m := range machines {
		if len(m.Spec.DeviceClaims) > 0 {
			anyDeviceClaims = true
			break
		}
	}
	if !anyDeviceClaims {
		return nil, nil
	}

	slices, err := c.Kube.ListResourceSlices(ctx)
	if err != nil && !kube.IsNotFound(err) {
		return nil, err
	}
	// "driver/poolName" -> the node hosting it. Only a node-local pool
	// (spec.nodeName set) contributes a hint -- a network-attached pool
	// (no single node "owns" it) contributes none, same as an
	// unresolvable claim.
	poolNode := map[string]string{}
	for _, sl := range slices {
		if sl.Spec.NodeName == "" {
			continue
		}
		poolNode[sl.Spec.Driver+"/"+sl.Spec.Pool.Name] = sl.Spec.NodeName
	}

	claims, err := c.Kube.ListResourceClaims(ctx)
	if err != nil && !kube.IsNotFound(err) {
		return nil, err
	}
	// "namespace/claimName" -> node hint, from the claim's own allocation.
	claimNode := map[string]string{}
	for _, cl := range claims {
		if cl.Status.Allocation == nil {
			continue
		}
		for _, res := range cl.Status.Allocation.Devices.Results {
			if node, ok := poolNode[res.Driver+"/"+res.Pool]; ok {
				claimNode[cl.Metadata.Namespace+"/"+cl.Metadata.Name] = node
				break // first cut: one hint per claim, first resolvable device wins
			}
		}
	}

	hints := map[string]string{}
	for _, m := range machines {
		for _, ref := range m.Spec.DeviceClaims {
			if node, ok := claimNode[m.Namespace()+"/"+ref.Name]; ok {
				hints[m.Namespace()+"/"+m.Metadata.Name] = node
				break // first cut: one hint per Machine, first resolvable claim wins
			}
		}
	}
	return hints, nil
}
