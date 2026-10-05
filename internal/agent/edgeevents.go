// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/zyvorai/kairon/internal/agentplane"
	"github.com/zyvorai/kairon/internal/model"
)

// edgeEventWindow suppresses an identical Warning for the same Machine,
// so a steady beacon or deny loop does not write one Event per tick.
const edgeEventWindow = 10 * time.Minute

const edgeFlowLimit = 256

type edgeEventDedup struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// allow reports whether key may emit now, and records it when it may.
func (d *edgeEventDedup) allow(key string, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.last == nil {
		d.last = map[string]time.Time{}
	}
	if t, ok := d.last[key]; ok && now.Sub(t) < edgeEventWindow {
		return false
	}
	for k, t := range d.last {
		if now.Sub(t) >= edgeEventWindow {
			delete(d.last, k)
		}
	}
	d.last[key] = now
	return true
}

// edgeFlows reads FluxVM's recent flows for one runtime. FluxVM returns
// untyped JSON, so the common field spellings are accepted.
func (a *Agent) edgeFlows(ctx context.Context, runtimeID string) []agentplane.Flow {
	raw, err := a.Flux.GetVMNetworkFlows(ctx, runtimeID, edgeFlowLimit)
	if err != nil {
		return nil
	}
	return parseEdgeFlows(raw)
}

func parseEdgeFlows(raw json.RawMessage) []agentplane.Flow {
	var body struct {
		Items []struct {
			DstIP       string `json:"dstIP"`
			DstIPSnake  string `json:"dst_ip"`
			SNI         string `json:"sni"`
			DNS         string `json:"dns"`
			QName       string `json:"qname"`
			Bytes       int    `json:"bytes"`
			IntervalSec int    `json:"intervalSec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil
	}
	out := make([]agentplane.Flow, 0, len(body.Items))
	for _, it := range body.Items {
		f := agentplane.Flow{DstIP: it.DstIP, SNI: it.SNI, DNS: it.DNS, Bytes: it.Bytes, IntervalSec: it.IntervalSec}
		if f.DstIP == "" {
			f.DstIP = it.DstIPSnake
		}
		if f.DNS == "" {
			f.DNS = it.QName
		}
		out = append(out, f)
	}
	return out
}

// emitEdgeEvents turns this tick's drops and flows into Warning Events on
// the Machine. Best effort: a failed write is logged and never blocks the
// network reconcile.
func (a *Agent) emitEdgeEvents(ctx context.Context, m model.Machine, drops []agentplane.Drop, flows []agentplane.Flow) {
	if a.Kube == nil {
		return
	}
	events := agentplane.EventsFromDrops(m.Metadata.Name, drops)
	events = append(events, agentplane.EventsFromFindings(m.Metadata.Name, agentplane.Detect(flows, drops))...)
	now := time.Now()
	for _, ev := range events {
		key := m.Namespace() + "/" + m.Metadata.Name + "|" + ev.Reason + "|" + ev.Message
		if !a.edgeEvents.allow(key, now) {
			continue
		}
		if err := a.Kube.RecordEvent(ctx, m.Namespace(), "Machine", m.Metadata.Name, m.Metadata.UID, ev.Reason, ev.Message, ev.Type); err != nil {
			a.log().Warn("edge event record failed", "machine", m.Metadata.Name, "reason", ev.Reason, "error", err)
		}
	}
}
