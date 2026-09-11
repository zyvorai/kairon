// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"context"
	"fmt"

	"github.com/zyvorai/kairon/internal/fluxvm"
)

// NetworkAwareDestination restores/resumes FluxVM network state around peer prepare/commit.
type NetworkAwareDestination struct {
	Inner DestinationDriver
	Flux  *fluxvm.Client
}

func (d NetworkAwareDestination) Prepare(ctx context.Context, session Session) (PrepareResult, error) {
	res, err := d.Inner.Prepare(ctx, session)
	if err != nil {
		return res, err
	}
	if !res.TransferSupported || d.Flux == nil || len(session.NetworkSnapshot) == 0 || session.RuntimeID == "" {
		return res, nil
	}
	if err := d.Flux.NetworkMigrationRestore(ctx, session.RuntimeID, session.NetworkSnapshot); err != nil {
		_ = d.Inner.Abort(ctx, session)
		return PrepareResult{}, fmt.Errorf("network migration restore: %w", err)
	}
	return res, nil
}

func (d NetworkAwareDestination) Commit(ctx context.Context, session Session) error {
	if err := d.Inner.Commit(ctx, session); err != nil {
		return err
	}
	if d.Flux == nil || len(session.NetworkSnapshot) == 0 || session.RuntimeID == "" {
		return nil
	}
	if err := d.Flux.NetworkMigrationResume(ctx, session.RuntimeID); err != nil {
		return fmt.Errorf("network migration resume: %w", err)
	}
	return nil
}

func (d NetworkAwareDestination) Abort(ctx context.Context, session Session) error {
	return d.Inner.Abort(ctx, session)
}
