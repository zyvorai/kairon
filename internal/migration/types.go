package migration

import (
	"context"
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
	ID                string    `json:"id"`
	Namespace         string    `json:"namespace"`
	Machine           string    `json:"machine"`
	SourceNode        string    `json:"sourceNode"`
	TargetNode        string    `json:"targetNode"`
	RuntimeID         string    `json:"runtimeID,omitempty"`
	Phase             string    `json:"phase"`
	Backend           string    `json:"backend,omitempty"`
	Endpoint          string    `json:"endpoint,omitempty"`
	TransferSupported bool      `json:"transferSupported"`
	Reason            string    `json:"reason,omitempty"`
	TransferID        string    `json:"transferID,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
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
}

type PrepareResponse struct {
	SessionID         string `json:"sessionID"`
	Phase             string `json:"phase"`
	TransferSupported bool   `json:"transferSupported"`
	Endpoint          string `json:"endpoint,omitempty"`
	Backend           string `json:"backend,omitempty"`
	Reason            string `json:"reason,omitempty"`
}

type DestinationDriver interface {
	Prepare(context.Context, Session) (PrepareResult, error)
	Commit(context.Context, Session) error
	Abort(context.Context, Session) error
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
