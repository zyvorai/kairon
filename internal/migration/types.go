// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	safeID   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
	safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,252}$`)
)

var ErrUnsupported = errors.New("live migration backend unsupported")

type Session struct {
	ID                string `json:"id"`
	Namespace         string `json:"namespace"`
	Machine           string `json:"machine"`
	SourceNode        string `json:"sourceNode"`
	TargetNode        string `json:"targetNode"`
	RuntimeID         string `json:"runtimeID,omitempty"`
	Phase             string `json:"phase"`
	Backend           string `json:"backend,omitempty"`
	Endpoint          string `json:"endpoint,omitempty"`
	TransferSupported bool   `json:"transferSupported"`
	Reason            string `json:"reason,omitempty"`
	TransferID        string `json:"transferID,omitempty"`
	// DataPlaneEncrypted mirrors PrepareResult.DataPlaneEncrypted -- set once
	// from the destination driver's Prepare response.
	DataPlaneEncrypted bool            `json:"dataPlaneEncrypted,omitempty"`
	NetworkSnapshot    json.RawMessage `json:"networkSnapshot,omitempty"`
	// DiskPath, MAC, VCPUs and MemoryMiB describe the source runtime's
	// current, live configuration -- not just the Machine's original spec
	// -- so a real hypervisor-level adapter has enough information to
	// provision a topology-matching receiver on the target (storage
	// contract v1: shared storage, no disk copy, so DiskPath must name the
	// exact file the target will also open; MAC must be identical on both
	// sides since the destination device config must match the source's
	// migrated device state exactly). Populated by the source node's
	// agent from its own FluxVM record, not from the Machine spec, since
	// FluxVM may have resolved defaults (e.g. an auto-generated MAC) the
	// original spec never pinned down. Additive/optional: a stub or
	// protocol-only adapter that doesn't need them simply ignores them.
	DiskPath  string `json:"diskPath,omitempty"`
	MAC       string `json:"mac,omitempty"`
	VCPUs     uint32 `json:"vcpus,omitempty"`
	MemoryMiB uint64 `json:"memoryMiB,omitempty"`
	// MigrationNetwork names a migration network configured on the
	// destination node's adapter, used to pick which address the transfer
	// binds/advertises instead of the adapter's default. Empty is fully
	// backward compatible -- the adapter falls back to today's behavior.
	MigrationNetwork string    `json:"migrationNetwork,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

func (s Session) Validate() error {
	if !safeID.MatchString(s.ID) {
		return fmt.Errorf("invalid id %q", s.ID)
	}
	for name, value := range map[string]string{
		"namespace": s.Namespace, "machine": s.Machine,
		"sourceNode": s.SourceNode, "targetNode": s.TargetNode,
	} {
		if !safeName.MatchString(value) {
			return fmt.Errorf("invalid %s %q", name, value)
		}
	}
	if s.SourceNode == s.TargetNode {
		return errors.New("sourceNode and targetNode must differ")
	}
	if s.MigrationNetwork != "" && !safeName.MatchString(s.MigrationNetwork) {
		return fmt.Errorf("invalid migrationNetwork %q", s.MigrationNetwork)
	}
	return nil
}

func (s Session) SameIdentity(other Session) bool {
	return s.ID == other.ID && s.Namespace == other.Namespace && s.Machine == other.Machine &&
		s.SourceNode == other.SourceNode && s.TargetNode == other.TargetNode && s.RuntimeID == other.RuntimeID
}

type PrepareRequest struct {
	Session Session `json:"session"`
}

type PrepareResult struct {
	TransferSupported bool   `json:"transferSupported"`
	Endpoint          string `json:"endpoint,omitempty"`
	Backend           string `json:"backend,omitempty"`
	Reason            string `json:"reason,omitempty"`
	// DataPlaneEncrypted reports whether the destination adapter has TLS
	// configured for the QEMU migration data stream (distinct from this
	// RPC's own always-on mTLS). See model.MachineMigrationStatus's field
	// of the same name for the full contract.
	DataPlaneEncrypted bool `json:"dataPlaneEncrypted,omitempty"`
}

type PrepareResponse struct {
	SessionID          string `json:"sessionID"`
	Phase              string `json:"phase"`
	TransferSupported  bool   `json:"transferSupported"`
	Endpoint           string `json:"endpoint,omitempty"`
	Backend            string `json:"backend,omitempty"`
	Reason             string `json:"reason,omitempty"`
	DataPlaneEncrypted bool   `json:"dataPlaneEncrypted,omitempty"`
}

type DestinationDriver interface {
	Prepare(context.Context, Session) (PrepareResult, error)
	Commit(context.Context, Session) error
	Abort(context.Context, Session) error
}

// DiagnosisResult is a read-only snapshot of what's actually known about a
// migration stuck in NeedsRecovery -- both the destination's own durable
// session-store phase and, when a Flux client is available, live ground
// truth from the destination's FluxVM. SessionPhase alone is NOT proof of
// whether the destination actually committed: Commit() only advances the
// store to "Committed" after both the inner adapter commit AND network
// resume succeed, so a destination VM can be genuinely running while the
// store still says "Prepared" (see NetworkAwareDestination.Commit).
type DiagnosisResult struct {
	SessionPhase             string    `json:"sessionPhase"`
	DestinationRuntimeFound  bool      `json:"destinationRuntimeFound"`
	DestinationRuntimeStatus string    `json:"destinationRuntimeStatus,omitempty"`
	DestinationRuntimeID     string    `json:"destinationRuntimeID,omitempty"`
	DestinationGuestIP       string    `json:"destinationGuestIP,omitempty"`
	ObservedAt               time.Time `json:"observedAt"`
}

// Diagnosable is implemented by a DestinationDriver that can report live
// ground truth about a session, beyond its own durable phase -- kept
// separate from DestinationDriver (rather than added to it) so existing
// implementations and test fakes don't need changes. Callers should
// type-assert for it and degrade gracefully (report only the session-store
// phase) when a driver doesn't implement it.
type Diagnosable interface {
	Diagnose(ctx context.Context, session Session) (DiagnosisResult, error)
}

type UnsupportedDestinationDriver struct{ Reason string }

func (d UnsupportedDestinationDriver) Prepare(context.Context, Session) (PrepareResult, error) {
	reason := strings.TrimSpace(d.Reason)
	if reason == "" {
		reason = "no destination migration adapter is configured"
	}
	return PrepareResult{TransferSupported: false, Reason: reason}, nil
}
func (UnsupportedDestinationDriver) Commit(context.Context, Session) error { return ErrUnsupported }
func (UnsupportedDestinationDriver) Abort(context.Context, Session) error  { return nil }

type SourceOptions struct {
	Mode            string `json:"mode,omitempty"`
	BandwidthMbps   uint64 `json:"bandwidthMbps,omitempty"`
	MaxDowntimeMs   uint64 `json:"maxDowntimeMs,omitempty"`
	MultifdChannels uint8  `json:"multifdChannels,omitempty"`
}

type SourceRequest struct {
	Session   Session       `json:"session"`
	RuntimeID string        `json:"runtimeID"`
	Endpoint  string        `json:"endpoint"`
	Options   SourceOptions `json:"options,omitempty"`
}

type TransferStatus struct {
	TransferID     string `json:"transferID,omitempty"`
	Phase          string `json:"phase"`
	Message        string `json:"message,omitempty"`
	RAMTransferred uint64 `json:"ramTransferred,omitempty"`
	RAMRemaining   uint64 `json:"ramRemaining,omitempty"`
	RAMTotal       uint64 `json:"ramTotal,omitempty"`
	TotalTimeMs    uint64 `json:"totalTimeMs,omitempty"`
	DowntimeMs     uint64 `json:"downtimeMs,omitempty"`
}

type SourceDriver interface {
	Start(context.Context, SourceRequest) (TransferStatus, error)
	Status(context.Context, Session, string) (TransferStatus, error)
	Abort(context.Context, Session, string) error
}

type UnsupportedSourceDriver struct{ Reason string }

func (d UnsupportedSourceDriver) Start(context.Context, SourceRequest) (TransferStatus, error) {
	reason := strings.TrimSpace(d.Reason)
	if reason == "" {
		reason = "no source migration adapter is configured"
	}
	return TransferStatus{}, fmt.Errorf("%w: %s", ErrUnsupported, reason)
}
func (UnsupportedSourceDriver) Status(context.Context, Session, string) (TransferStatus, error) {
	return TransferStatus{}, ErrUnsupported
}
func (UnsupportedSourceDriver) Abort(context.Context, Session, string) error { return nil }
