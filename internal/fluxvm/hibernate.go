// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package fluxvm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// Snapshot saves an in-place VM-state checkpoint -- QEMU's real `savevm`,
// or a Cloud Hypervisor snapshot directory -- capturing RAM, CPU, and
// device state exactly as they are right now. Distinct from CSI's
// disk-content-only CreateSnapshot/DeleteSnapshot (internal/csinode):
// this is a full hypervisor-level checkpoint of a Machine's *running*
// state, not a point-in-time copy of its backing disk file. Requires the
// VM to already be Running or Paused (FluxVM's own error otherwise); the
// VM keeps running/stays paused throughout -- this never stops or
// restarts anything. Firecracker doesn't support this at all -- FluxVM's
// own clear error ("snapshot not supported for backend Firecracker")
// surfaces unmodified if attempted.
func (c *Client) Snapshot(ctx context.Context, id, tag string) error {
	_, err := c.do(ctx, http.MethodPost, "/v1/vms/"+url.PathEscape(id)+"/snapshot", map[string]any{"tag": tag})
	return err
}

// stop powers a VM off via FluxVM's own POST /v1/vms/{id}/stop, keeping
// its record and disk intact -- a completely different operation from
// Delete (which tears the runtime down entirely; the same one Kairon's
// own spec.powerState: Stopped already uses). Deliberately unexported:
// this project doesn't expose a general-purpose "power off but keep the
// record" capability of its own yet (a real, separate, bigger piece of
// work than this file's own scope -- see ROADMAP.md), so this only ever
// exists as RestoreSnapshot's own internal orchestration step and
// internal/agent's reconcile self-healing branch, never a standalone
// route a caller can reach directly.
func (c *Client) stop(ctx context.Context, id string) (*Record, error) {
	data, err := c.do(ctx, http.MethodPost, "/v1/vms/"+url.PathEscape(id)+"/stop", nil)
	if err != nil {
		return nil, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("decode stop response: %w", err)
	}
	return &rec, nil
}

// Start resumes a VM FluxVM itself has stopped (via stop above -- never
// one Kairon deleted, which has no record left to start) from its
// existing disk, unchanged -- FluxVM's own POST /v1/vms/{id}/start.
// Exported for internal/agent's reconcile self-healing branch (a Machine
// whose FluxVM record reports Stopped while spec.powerState still wants
// Running/Paused -- the recovery path if a RestoreSnapshot call's own
// process crashes between its stop and start-from-snapshot steps); not
// wired to any standalone route of its own, for the same reason stop
// above isn't.
func (c *Client) Start(ctx context.Context, id string) (*Record, error) {
	data, err := c.do(ctx, http.MethodPost, "/v1/vms/"+url.PathEscape(id)+"/start", nil)
	if err != nil {
		return nil, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("decode start response: %w", err)
	}
	return &rec, nil
}

// startFromSnapshot resumes a FluxVM-stopped VM from a specific
// Snapshot's saved state (QEMU -loadvm, or Cloud Hypervisor's own
// snapshot restore) instead of its plain last-known-good disk state.
// FluxVM's own start_impl silently no-ops -- ignoring tag entirely --
// when the VM is already Running, which is exactly why RestoreSnapshot
// below always stops first rather than calling this directly.
func (c *Client) startFromSnapshot(ctx context.Context, id, tag string) (*Record, error) {
	data, err := c.do(ctx, http.MethodPost, "/v1/vms/"+url.PathEscape(id)+"/start-from-snapshot", map[string]any{"tag": tag})
	if err != nil {
		return nil, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("decode start-from-snapshot response: %w", err)
	}
	return &rec, nil
}

// RestoreSnapshot restores a VM to exactly the state a prior Snapshot
// call captured -- RAM, CPU, and device state, not just disk content
// (CSI's own snapshot restore, internal/csinode, only ever covers that).
// Orchestrates FluxVM's own stop -> start-from-snapshot sequence itself,
// since start-from-snapshot alone silently ignores tag on an
// already-Running VM (see startFromSnapshot's own doc comment) --
// restoring in place genuinely requires stopping first. If the stop
// succeeds but the start-from-snapshot call fails (including this
// process crashing in between), the VM is left FluxVM-stopped with its
// disk/record intact -- internal/agent's reconcile loop notices the
// mismatch against spec.powerState and calls Start to bring it back
// (unrestored, from its plain last-known-good state, not the snapshot)
// as a safety net, not a second automatic restore attempt.
func (c *Client) RestoreSnapshot(ctx context.Context, id, tag string) (*Record, error) {
	if _, err := c.stop(ctx, id); err != nil {
		return nil, fmt.Errorf("stop before restore: %w", err)
	}
	rec, err := c.startFromSnapshot(ctx, id, tag)
	if err != nil {
		return nil, fmt.Errorf("start from snapshot %q (the VM is now stopped, not restored -- retry, or resolve otherwise): %w", tag, err)
	}
	return rec, nil
}
