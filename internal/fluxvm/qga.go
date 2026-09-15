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

// QGAFsfreezeFreeze calls FluxVM's real qemu-guest-agent
// guest-fsfreeze-freeze -- flushes and freezes every mounted, writable
// filesystem inside the guest, for an application-consistent disk
// snapshot instead of a merely crash-consistent one. Only meaningful for
// a VM created with spec.guestAgent.enabled -- FluxVM returns a clear
// error otherwise ("QGA not enabled for this VM"). Always pair with a
// later QGAFsfreezeThaw call -- see internal/controller/snapshot.go's own
// doc comment for why a caller must never leave a frozen guest
// unresolved. Returns the number of filesystems frozen.
func (c *Client) QGAFsfreezeFreeze(ctx context.Context, id string) (int64, error) {
	data, err := c.do(ctx, http.MethodPost, "/v1/vms/"+url.PathEscape(id)+"/qga/fsfreeze", nil)
	if err != nil {
		return 0, err
	}
	var out struct {
		Frozen int64 `json:"frozen"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return 0, fmt.Errorf("decode qga fsfreeze: %w", err)
	}
	return out.Frozen, nil
}

// QGAFsfreezeThaw calls FluxVM's real qemu-guest-agent guest-fsfreeze-thaw
// -- idempotent per QGA's own semantics (thawing an already-thawed guest
// succeeds as a no-op, not an error), which is exactly what makes this
// safe to retry unconditionally until it succeeds. Returns the number of
// filesystems thawed.
func (c *Client) QGAFsfreezeThaw(ctx context.Context, id string) (int64, error) {
	data, err := c.do(ctx, http.MethodPost, "/v1/vms/"+url.PathEscape(id)+"/qga/fsthaw", nil)
	if err != nil {
		return 0, err
	}
	var out struct {
		Thawed int64 `json:"thawed"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return 0, fmt.Errorf("decode qga fsthaw: %w", err)
	}
	return out.Thawed, nil
}
