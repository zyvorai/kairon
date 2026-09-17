// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package csinode

import (
	"context"
	"os"
	"path/filepath"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DriverName identifies this CSI driver -- the value a statically
// provisioned PersistentVolume's spec.csi.driver must match exactly, and
// what this reports back via GetPluginInfo/NodeGetInfo. Domain-shaped per
// the CSI spec's own naming convention.
const DriverName = "csi.kairon.zyvor.dev"

// stagingDirPerm/publishDirPerm: kairon-csi-node runs privileged (it has
// to, to log in to iSCSI targets and mount block devices), so these
// directories are created 0750 rather than world-readable -- there is no
// legitimate reason for anything other than root (this plugin) and the
// group it runs as to read a Machine's raw disk image directory.
const stagingDirPerm = 0o750

// NodeServer implements the CSI Node service for Kairon's first-cut,
// iSCSI-only network-block driver -- see docs/guides/machine-storage-csi.md
// for the full design, including why this breaks Go-stdlib-only (a real
// iSCSI initiator and filesystem tooling aren't hand-rolled) and its
// current, real limits (iSCSI only, mount-type volumes only -- no raw
// block mode, no volume health reporting).
//
// A PersistentVolume this driver serves can come from either of two
// places now: an admin still can create one statically (spec.csi.
// volumeAttributes carrying the iSCSI portal/IQN/LUN by hand -- the
// original, still fully supported "pre-provisioned" path), or a
// StorageClass naming this driver as its provisioner can request one
// dynamically via ControllerServer (controller.go), served by the
// separate kairon-csi-controller binary/Deployment -- see its own doc
// comment for what that first cut does and doesn't cover. Either way, the
// Node service's own job is unchanged: log in to whatever portal/IQN/LUN
// the resulting PersistentVolume's volumeAttributes name and mount it.
// Kairon's own Machine boot-disk consumption path (internal/agent) also
// never goes through kubelet's Pod volume machinery at all -- kairon-node
// dials this same node plugin's Unix socket directly as a CSI client, the
// same way it already reads a hostPath/local PersistentVolume's path
// directly without any Pod involved (see internal/agent/storage.go). The
// upstream csi-node-driver-registrar sidecar this ships with still
// registers the plugin with kubelet, so a real Kubernetes Pod (unrelated
// to Kairon) with a matching PVC/PV can also use it normally.
type NodeServer struct {
	csi.UnimplementedNodeServer
	NodeID string
	Runner CommandRunner
	iscsi  *iscsiClient

	// isMountPoint/mountFn/unmountFn default to the real, OS-touching
	// implementations (IsMountPoint, mount, unmount) via NewNodeServer --
	// overridden in tests so this type's actual request-handling logic
	// (validation, idempotency checks, call sequencing, error
	// propagation) is fully unit-testable without a real filesystem,
	// real mount privileges, or a real iSCSI target. See node_test.go and
	// CommandRunner's own doc comment for why the real OS-level behavior
	// these stand in for can only be verified by running the built
	// kairon-csi-node image for real.
	isMountPoint func(path string) (bool, error)
	mountFn      func(source, target, fsType string, flags uintptr, data string) error
	unmountFn    func(target string) error
	statfsFn     func(path string) (VolumeStats, error)
}

// NewNodeServer builds a NodeServer against the real CommandRunner and
// real OS mount/mountinfo calls. nodeID should uniquely identify this
// Kubernetes Node (its name is the natural choice -- see
// cmd/kairon-csi-node/main.go).
func NewNodeServer(nodeID string, runner CommandRunner) *NodeServer {
	return &NodeServer{
		NodeID: nodeID, Runner: runner, iscsi: &iscsiClient{run: runner},
		isMountPoint: IsMountPoint, mountFn: mount, unmountFn: unmount, statfsFn: statfs,
	}
}

func (s *NodeServer) NodeGetInfo(_ context.Context, _ *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{NodeId: s.NodeID}, nil
}

func (s *NodeServer) NodeGetCapabilities(_ context.Context, _ *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: []*csi.NodeServiceCapability{
			{Type: &csi.NodeServiceCapability_Rpc{Rpc: &csi.NodeServiceCapability_RPC{
				Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
			}}},
			{Type: &csi.NodeServiceCapability_Rpc{Rpc: &csi.NodeServiceCapability_RPC{
				Type: csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
			}}},
			{Type: &csi.NodeServiceCapability_Rpc{Rpc: &csi.NodeServiceCapability_RPC{
				Type: csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
			}}},
		},
	}, nil
}

// NodeGetVolumeStats reports volume_id's disk usage/capacity by
// statfs(2)-ing volume_path directly (see statfs's own doc comment) --
// kubelet calls this on every CSI volume it manages, to serve
// `kubectl describe pod`'s "Used"/du-free capacity display and
// ephemeral-storage-based eviction/metrics without kubelet itself needing
// to know this driver's on-disk layout. volume_path is whatever the CO
// passed to NodeStageVolume/NodePublishVolume -- either a staging or a
// publish path is valid per the CSI spec, and IsMountPoint's mountinfo
// scan recognizes both (a bind mount is its own distinct mountinfo
// entry). Deliberately fails closed rather than fabricating a number: a
// path that isn't currently mounted gets NotFound, and a real statfs
// error gets Internal -- callers make real capacity decisions off this,
// so a wrong answer is worse than a clear error.
func (s *NodeServer) NodeGetVolumeStats(_ context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume_id is required")
	}
	path := req.GetVolumePath()
	if path == "" {
		return nil, status.Error(codes.InvalidArgument, "volume_path is required")
	}
	mounted, err := s.isMountPoint(path)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "check volume path: %v", err)
	}
	if !mounted {
		return nil, status.Errorf(codes.NotFound, "volume path %s is not a mounted volume", path)
	}
	stats, err := s.statfsFn(path)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "statfs %s: %v", path, err)
	}
	return &csi.NodeGetVolumeStatsResponse{
		Usage: []*csi.VolumeUsage{
			{Unit: csi.VolumeUsage_BYTES, Total: stats.TotalBytes, Used: stats.UsedBytes, Available: stats.AvailableBytes},
			{Unit: csi.VolumeUsage_INODES, Total: stats.TotalInodes, Used: stats.UsedInodes, Available: stats.AvailableInodes},
		},
	}, nil
}

// NodeStageVolume logs in to the requested iSCSI target/LUN, formats it
// if it has no existing filesystem, and mounts it at staging_target_path
// -- idempotent, per the CSI spec's own requirement (see IsMountPoint).
func (s *NodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume_id is required")
	}
	if req.GetStagingTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "staging_target_path is required")
	}
	mountCap := req.GetVolumeCapability().GetMount()
	if mountCap == nil {
		return nil, status.Error(codes.InvalidArgument, "only mount-type volumes are supported (no raw block mode) in this first cut")
	}
	cfg, err := parseISCSIConfig(req.GetVolumeContext(), req.GetSecrets())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if mountCap.GetFsType() != "" {
		cfg.FSType = mountCap.GetFsType()
	}

	already, err := s.isMountPoint(req.GetStagingTargetPath())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "check staging path: %v", err)
	}
	if already {
		return &csi.NodeStageVolumeResponse{}, nil
	}

	device, err := s.iscsi.login(ctx, cfg)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "iscsi login for volume %s: %v", req.GetVolumeId(), err)
	}
	if err := ensureFormatted(ctx, s.Runner, device, cfg.FSType); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := os.MkdirAll(req.GetStagingTargetPath(), stagingDirPerm); err != nil {
		return nil, status.Errorf(codes.Internal, "create staging directory: %v", err)
	}
	if err := s.mountFn(device, req.GetStagingTargetPath(), cfg.FSType, 0, ""); err != nil {
		return nil, status.Errorf(codes.Internal, "mount %s at %s: %v", device, req.GetStagingTargetPath(), err)
	}
	return &csi.NodeStageVolumeResponse{}, nil
}

// NodeExpandVolume grows volume_id's on-disk filesystem to fill its
// backing device, after ControllerExpandVolume has already grown the LIO
// backstore itself -- see rescan/growFilesystem's own doc comments for
// why both steps (rescan, then grow) are required and in that order.
// volume_path is either a staging or a publish path per the CSI spec;
// this driver's own staging path is what growFilesystem's own remount
// mechanism (xfs_growfs) needs regardless, since xfs can only grow via
// an already-mounted path.
func (s *NodeServer) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume_id is required")
	}
	if req.GetVolumePath() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume_path is required")
	}
	cfg, err := decodeVolumeID(req.GetVolumeId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.iscsi.rescan(ctx, cfg); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	device, err := filepath.EvalSymlinks(devicePathFunc(cfg))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve device for volume %s: %v", req.GetVolumeId(), err)
	}
	if err := growFilesystem(ctx, s.Runner, device, req.GetVolumePath()); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &csi.NodeExpandVolumeResponse{}, nil
}

// NodeUnstageVolume unmounts staging_target_path (if mounted) and logs
// out of the iSCSI target volume_id encodes -- see decodeVolumeID for why
// the target has to come from volume_id rather than a request field.
// Idempotent: safe to call against an already-unstaged volume.
func (s *NodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume_id is required")
	}
	if req.GetStagingTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "staging_target_path is required")
	}
	mounted, err := s.isMountPoint(req.GetStagingTargetPath())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "check staging path: %v", err)
	}
	if mounted {
		if err := s.unmountFn(req.GetStagingTargetPath()); err != nil {
			return nil, status.Errorf(codes.Internal, "unmount %s: %v", req.GetStagingTargetPath(), err)
		}
	}
	cfg, err := decodeVolumeID(req.GetVolumeId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.iscsi.logout(ctx, cfg); err != nil {
		return nil, status.Errorf(codes.Internal, "iscsi logout: %v", err)
	}
	return &csi.NodeUnstageVolumeResponse{}, nil
}

// NodePublishVolume bind-mounts the already-staged volume from
// staging_target_path to target_path -- the per-consumer mount CSI's
// two-phase stage/publish split exists for (multiple pods sharing one
// staged volume on the same node). Kairon itself only ever has one
// consumer per volume (one Machine), but implements the full two-phase
// flow anyway since that's what STAGE_UNSTAGE_VOLUME capability commits
// this plugin to for any CSI-compliant caller, not just kairon-node.
func (s *NodeServer) NodePublishVolume(_ context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume_id is required")
	}
	if req.GetStagingTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "staging_target_path is required (NodeStageVolume must run first)")
	}
	if req.GetTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "target_path is required")
	}
	already, err := s.isMountPoint(req.GetTargetPath())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "check target path: %v", err)
	}
	if already {
		return &csi.NodePublishVolumeResponse{}, nil
	}
	if err := os.MkdirAll(req.GetTargetPath(), stagingDirPerm); err != nil {
		return nil, status.Errorf(codes.Internal, "create target directory: %v", err)
	}
	if err := s.mountFn(req.GetStagingTargetPath(), req.GetTargetPath(), "", bindMountFlag, ""); err != nil {
		return nil, status.Errorf(codes.Internal, "bind mount %s at %s: %v", req.GetStagingTargetPath(), req.GetTargetPath(), err)
	}
	if req.GetReadonly() {
		// Linux ignores most flags (MS_RDONLY included) on the initial
		// bind-mount call -- a read-only bind mount needs a second,
		// explicit remount, not a single mount() call with both flags
		// set at once.
		if err := s.mountFn("", req.GetTargetPath(), "", bindMountFlag|bindRemountFlag|bindReadOnlyFlag, ""); err != nil {
			return nil, status.Errorf(codes.Internal, "remount %s read-only: %v", req.GetTargetPath(), err)
		}
	}
	return &csi.NodePublishVolumeResponse{}, nil
}

// NodeUnpublishVolume unmounts target_path -- idempotent, safe against an
// already-unpublished volume.
func (s *NodeServer) NodeUnpublishVolume(_ context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume_id is required")
	}
	if req.GetTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "target_path is required")
	}
	mounted, err := s.isMountPoint(req.GetTargetPath())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "check target path: %v", err)
	}
	if mounted {
		if err := s.unmountFn(req.GetTargetPath()); err != nil {
			return nil, status.Errorf(codes.Internal, "unmount %s: %v", req.GetTargetPath(), err)
		}
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}
