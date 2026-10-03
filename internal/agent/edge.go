// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/zyvorai/kairon/internal/ebpfedge"
	"github.com/zyvorai/kairon/internal/model"
)

// edgeRequested reports whether this Machine asked for the VM-edge eBPF
// contract. Empty Machines stay on today's path so a legacy FluxVM is not
// probed for endpoints it does not have.
func edgeRequested(m model.Machine) bool {
	n := m.Spec.Network
	if n.AntiSpoof || n.LearnIP || n.QoS != nil {
		return true
	}
	return strings.EqualFold(n.DataplaneMode, "ebpf")
}

func buildEdgeSpec(m model.Machine, guestIP string) ebpfedge.EdgeSpec {
	spec := ebpfedge.EdgeSpec{
		Namespace:    m.Namespace(),
		Machine:      m.Metadata.Name,
		AntiSpoof:    m.Spec.Network.AntiSpoof,
		LearnIP:      m.Spec.Network.LearnIP,
		AssignedMAC:  m.Spec.Network.MAC,
		AssignedIP:   guestIP,
		DefaultAllow: true,
	}
	if q := m.Spec.Network.QoS; q != nil {
		if q.IngressMbps != nil {
			spec.QoS.IngressMbps = *q.IngressMbps
		}
		if q.EgressMbps != nil {
			spec.QoS.EgressMbps = *q.EgressMbps
		}
		if q.IngressPps != nil {
			spec.QoS.IngressPps = *q.IngressPps
		}
		if q.EgressPps != nil {
			spec.QoS.EgressPps = *q.EgressPps
		}
	}
	return spec
}

// applyEdge posts the compiled document and returns the status fragment.
// A FluxVM that does not know the endpoint yet is a warning unless the
// Machine asked to fail closed.
func (a *Agent) applyEdge(ctx context.Context, m model.Machine, runtimeID, guestIP string) (*model.MachineEdgeStatus, error) {
	if !edgeRequested(m) || a.Flux == nil || runtimeID == "" {
		return nil, nil
	}
	compiled, err := ebpfedge.Compile(buildEdgeSpec(m, guestIP))
	if err != nil {
		return nil, fmt.Errorf("compile edge: %w", err)
	}
	status := &model.MachineEdgeStatus{
		Identity:  compiled.Identity,
		AntiSpoof: compiled.AntiSpoof,
	}
	if err := a.Flux.ApplyEdge(ctx, runtimeID, compiled); err != nil {
		if m.Spec.Network.DataplaneRequired {
			return nil, fmt.Errorf("dataplane required but edge apply failed: %w", err)
		}
		a.log().Warn("edge apply failed; continuing", "machine", m.Metadata.Name, "error", err)
	}
	return status, nil
}

func (a *Agent) log() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

// exportConntrackSnapshot reads the source map and stamps the stable
// identity so the destination restore check matches this Machine. A miss
// is empty, matching network snapshot export: live migration still proceeds.
func (a *Agent) exportConntrackSnapshot(ctx context.Context, m model.Machine, runtimeID string) json.RawMessage {
	if !edgeRequested(m) || a.Flux == nil || runtimeID == "" {
		return nil
	}
	snap, err := a.Flux.ExportConntrack(ctx, runtimeID)
	if err != nil {
		a.log().Warn("conntrack export failed; continuing without it", "machine", m.Metadata.Name, "error", err)
		return nil
	}
	snap.Identity = ebpfedge.StableIdentity(m.Namespace(), m.Metadata.Name)
	if snap.Generation == 0 {
		snap.Generation = uint64(time.Now().UTC().Unix())
	}
	if snap.ExportedAt.IsZero() {
		snap.ExportedAt = time.Now().UTC()
	}
	raw, err := ebpfedge.MarshalSnapshot(snap)
	if err != nil {
		a.log().Warn("conntrack encode failed", "machine", m.Metadata.Name, "error", err)
		return nil
	}
	return raw
}
