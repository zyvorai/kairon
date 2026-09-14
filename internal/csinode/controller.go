// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package csinode

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// iqnPrefix names every target this driver dynamically provisions --
// deliberately a fixed, stable value (IQN naming convention embeds a
// registration date, which doesn't need to track "today"; it just needs
// to be syntactically valid and never change once volumes exist under
// it). A statically-provisioned PersistentVolume's own hand-written IQN
// (docs/guides/machine-storage-csi.md's example) is unrelated and can be
// anything -- this prefix only marks volumes *this* Controller created,
// so DeleteVolume can recover a dynamic volume's backstore name from its
// IQN (see nameFromIQN).
const iqnPrefix = "iqn.2026-01.dev.zyvor.kairon:"

// defaultVolumeSizeBytes is used only when a CreateVolumeRequest carries
// no CapacityRange at all -- real StorageClass-driven PVCs almost always
// set spec.resources.requests.storage, so this is a defensive fallback,
// not the expected common case. 1Gi matches this driver's own
// docs/guides/machine-storage-csi.md example LUN size.
const defaultVolumeSizeBytes = 1 << 30

// volumeNamePattern is what a CSI CreateVolumeRequest.Name is expected to
// look like in practice (Kubernetes' own external-provisioner generates
// "pvc-<uuid>") -- validated defensively since this name becomes both a
// filesystem path component (the backing file) and part of an iSCSI IQN,
// both of which have real character-set constraints this driver would
// rather reject up front than half-succeed against.
var volumeNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ControllerServer implements the CSI Controller service for Kairon's
// iSCSI backend -- dynamic provisioning, first cut. Served by the
// separate kairon-csi-controller binary/Deployment (internal/csinode's
// package doc comment explains why it shares this package with
// NodeServer rather than living apart). CreateVolume/DeleteVolume drive a
// real Linux LIO iSCSI target (lioClient) to create/destroy exactly the
// portal/IQN/LUN shape NodeServer's iscsiConfig already knows how to
// consume -- a dynamically provisioned volume is otherwise
// indistinguishable from a statically hand-written one once it reaches
// NodeStageVolume.
//
// Real, current limits (see docs/guides/machine-storage-csi.md): no CHAP
// on dynamically provisioned volumes (LIO "demo mode" -- see
// ensureDemoMode's own doc comment for why), no volume expansion, no
// snapshots through this path (MachineSnapshot's own CSI VolumeSnapshot
// flow is unrelated and unaffected), one backing file per volume on
// whichever single node runs kairon-csi-controller (no topology-aware
// placement across multiple storage nodes -- this first cut assumes one).
type ControllerServer struct {
	csi.UnimplementedControllerServer

	lio *lioClient

	// Portal is the host:port initiators dial -- normally this
	// Controller's own node's real, cluster-reachable IP (or a stable
	// VIP in front of it), never 0.0.0.0. Required; CreateVolume refuses
	// to provision anything without it (nothing else can tell a Machine
	// how to reach a volume this driver creates).
	Portal string

	// VolumeDir is the local directory each backing sparse file is
	// created under -- must exist and be writable, no default (an
	// operator-chosen path, not something this driver invents).
	VolumeDir string
}

// NewControllerServer builds a ControllerServer against the real
// CommandRunner. portal is required and validated as host:port up front
// (see the Portal field's doc comment) -- a malformed value would
// otherwise only surface on the first real CreateVolume call, the same
// "fail fast at startup, not on the first request" posture this project's
// other TLS/config validation already takes. volumeDir is created (0700
// -- only this Controller process needs it) if it doesn't already exist.
func NewControllerServer(runner CommandRunner, portal, volumeDir string) (*ControllerServer, error) {
	if portal == "" {
		return nil, fmt.Errorf("portal is required")
	}
	if _, _, err := splitPortal(portal); err != nil {
		return nil, err
	}
	if volumeDir == "" {
		return nil, fmt.Errorf("volumeDir is required")
	}
	if err := os.MkdirAll(volumeDir, 0o700); err != nil {
		return nil, fmt.Errorf("create volume directory %s: %w", volumeDir, err)
	}
	return &ControllerServer{lio: &lioClient{run: runner}, Portal: portal, VolumeDir: volumeDir}, nil
}

func (s *ControllerServer) ControllerGetCapabilities(_ context.Context, _ *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: []*csi.ControllerServiceCapability{
			{Type: &csi.ControllerServiceCapability_Rpc{Rpc: &csi.ControllerServiceCapability_RPC{
				Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
			}}},
			{Type: &csi.ControllerServiceCapability_Rpc{Rpc: &csi.ControllerServiceCapability_RPC{
				Type: csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
			}}},
		},
	}, nil
}

// chapCredentials is CreateVolume's own read of req.GetSecrets() -- it
// reuses the exact secretKeyUsername/secretKeyPassword keys iscsi.go's
// parseISCSIConfig already expects from a NodeStageVolume secret.
// Pointing a StorageClass's csi.storage.k8s.io/provisioner-secret-name
// and node-stage-secret-name parameters (see docs/guides/machine-storage-csi.md)
// at the same pre-created Secret is what makes the CHAP identity this
// Controller configures on the LIO target match what kubelet later hands
// NodeStageVolume to log in with.
type chapCredentials struct{ Username, Password string }

// chapCredentialsFromSecrets returns a zero-value chapCredentials (demo
// mode, no CHAP) when secrets carries neither key -- the default,
// unchanged behavior for a StorageClass with no provisioner secret.
func chapCredentialsFromSecrets(secrets map[string]string) (chapCredentials, error) {
	c := chapCredentials{Username: secrets[secretKeyUsername], Password: secrets[secretKeyPassword]}
	if (c.Username == "") != (c.Password == "") {
		return chapCredentials{}, fmt.Errorf("a provisioner secret must set both %q and %q, or neither", secretKeyUsername, secretKeyPassword)
	}
	return c, nil
}

// CreateVolume provisions a new iSCSI target/LUN backed by a fresh
// sparse file, and returns the volume_id/volume_context NodeServer's
// NodeStageVolume already knows how to consume. Idempotent per the CSI
// spec's own requirement for CreateVolume: a retry with the same Name
// (the external-provisioner sidecar's own idempotency token) reaches the
// same already-exists-tolerant lioClient calls and succeeds the same way.
func (s *ControllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	name := req.GetName()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if !volumeNamePattern.MatchString(name) {
		return nil, status.Errorf(codes.InvalidArgument, "name %q must match %s (Kubernetes' own external-provisioner-generated PVC names always do)", name, volumeNamePattern.String())
	}
	if len(req.GetVolumeCapabilities()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume_capabilities is required")
	}
	for _, cap := range req.GetVolumeCapabilities() {
		if cap.GetMount() == nil {
			return nil, status.Error(codes.InvalidArgument, "only mount-type volumes are supported (no raw block mode) in this first cut")
		}
	}

	sizeBytes := req.GetCapacityRange().GetRequiredBytes()
	if sizeBytes <= 0 {
		sizeBytes = defaultVolumeSizeBytes
	}

	iqn := iqnPrefix + name
	if err := s.lio.ensureBackstore(ctx, name, s.backingFilePath(name), sizeBytes); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := s.lio.ensureTarget(ctx, iqn); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := s.lio.ensureLUN(ctx, iqn, name); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	host, port, err := splitPortal(s.Portal)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := s.lio.ensurePortal(ctx, iqn, host, port); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	chap, err := chapCredentialsFromSecrets(req.GetSecrets())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.lio.ensureAuth(ctx, iqn, chap.Username, chap.Password); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	cfg := iscsiConfig{Portal: s.Portal, IQN: iqn, LUN: defaultLUN}
	return &csi.CreateVolumeResponse{Volume: &csi.Volume{
		VolumeId:      encodeVolumeID(cfg),
		CapacityBytes: sizeBytes,
		VolumeContext: map[string]string{
			volumeAttrPortal: s.Portal,
			volumeAttrIQN:    iqn,
			volumeAttrLUN:    defaultLUN,
		},
	}}, nil
}

// DeleteVolume tears down the target and backstore CreateVolume created,
// and removes the backing file. Idempotent, including against a
// volume_id this driver never actually recognizes (the CSI spec requires
// DeleteVolume to succeed on an already-gone/unknown volume, not error).
func (s *ControllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume_id is required")
	}
	cfg, err := decodeVolumeID(req.GetVolumeId())
	if err != nil {
		return &csi.DeleteVolumeResponse{}, nil
	}
	name, ok := nameFromIQN(cfg.IQN)
	if !ok {
		// A volume_id this driver's own encoding produced, but for a
		// statically-provisioned (not dynamically created) target --
		// nothing this Controller created, so nothing for it to delete.
		return &csi.DeleteVolumeResponse{}, nil
	}
	if err := s.lio.deleteTarget(ctx, cfg.IQN); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := s.lio.deleteBackstore(ctx, name); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := os.Remove(s.backingFilePath(name)); err != nil && !os.IsNotExist(err) {
		return nil, status.Errorf(codes.Internal, "remove backing file for %s: %v", name, err)
	}
	return &csi.DeleteVolumeResponse{}, nil
}

// ControllerExpandVolume grows the LIO backstore CreateVolume created --
// idempotent per the CSI spec: a request whose required_bytes is already
// met by the current backing file's real size succeeds as a no-op rather
// than re-issuing a resize. Always reports NodeExpansionRequired: the
// on-disk filesystem still needs growing separately (NodeServer's own
// NodeExpandVolume) -- the kernel's SCSI layer caches a device's size at
// login and never re-reads it on its own. Never shrinks -- the CSI spec
// itself has no shrink verb.
func (s *ControllerServer) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume_id is required")
	}
	sizeBytes := req.GetCapacityRange().GetRequiredBytes()
	if sizeBytes <= 0 {
		return nil, status.Error(codes.InvalidArgument, "capacity_range.required_bytes must be set")
	}
	cfg, err := decodeVolumeID(req.GetVolumeId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	name, ok := nameFromIQN(cfg.IQN)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "volume %q was not dynamically provisioned by this Controller", req.GetVolumeId())
	}
	info, err := os.Stat(s.backingFilePath(name))
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "backing file for %s: %v", name, err)
	}
	if info.Size() >= sizeBytes {
		return &csi.ControllerExpandVolumeResponse{CapacityBytes: info.Size(), NodeExpansionRequired: true}, nil
	}
	if err := s.lio.resizeBackstore(ctx, name, sizeBytes); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &csi.ControllerExpandVolumeResponse{CapacityBytes: sizeBytes, NodeExpansionRequired: true}, nil
}

func (s *ControllerServer) backingFilePath(name string) string {
	return filepath.Join(s.VolumeDir, name+".img")
}

// nameFromIQN recovers CreateVolume's original name from an IQN this
// driver generated (iqnPrefix + name) -- ok is false for any IQN not
// carrying this exact prefix, which DeleteVolume treats as "nothing this
// Controller provisioned."
func nameFromIQN(iqn string) (name string, ok bool) {
	if !strings.HasPrefix(iqn, iqnPrefix) {
		return "", false
	}
	return strings.TrimPrefix(iqn, iqnPrefix), true
}

func splitPortal(portal string) (host, port string, err error) {
	host, port, err = net.SplitHostPort(portal)
	if err != nil {
		return "", "", fmt.Errorf("portal %q must be host:port: %w", portal, err)
	}
	return host, port, nil
}
