// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/zyvorai/kairon/internal/csinode"
	"github.com/zyvorai/kairon/internal/model"
)

// csiVolumeStatus is what resolveCSIVolume computes and reconcileMachine
// persists into MachineStatus's Volume* fields -- see that type's own
// doc comment for why kairon-node has to track this itself.
type csiVolumeStatus struct {
	StagingPath string
	PublishPath string
	VolumeID    string
	// Driver is empty for Kairon's own driver (csinode.DriverName) -- see
	// MachineStatus.VolumeDriver's own doc comment -- or a third-party
	// driver name (see resolveThirdPartyCSIVolume).
	Driver string
}

// csiNodeClient lazily dials CSISocketPath and caches the connection on
// the Agent -- resolveCSIVolume runs on every reconcile tick for every
// CSI-backed Machine, and while redialing a local Unix socket is cheap,
// there's no reason to pay even that cost every ~3s.
func (a *Agent) csiNodeClient() (csi.NodeClient, error) {
	if a.CSISocketPath == "" {
		return nil, fmt.Errorf("this node has no CSI socket configured (-csi-socket / $KAIRON_CSI_SOCKET) -- a network-block (CSI-backed) PersistentVolume cannot be used as a Machine boot disk without kairon-csi-node deployed; see docs/guides/machine-storage-csi.md")
	}
	if a.csiConn == nil {
		conn, err := grpc.NewClient("unix://"+a.CSISocketPath, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return nil, fmt.Errorf("dial kairon-csi-node at %s: %w", a.CSISocketPath, err)
		}
		a.csiConn = conn
	}
	return csi.NewNodeClient(a.csiConn), nil
}

// resolveCSIVolume stages and publishes a CSI-backed PersistentVolume via
// internal/csinode's Node service, dialed directly over its local Unix
// socket -- kairon-node acts as its own CSI client here rather than
// routing through kubelet's Pod volume machinery, since a Machine has no
// backing Pod for kubelet to trigger that machinery against at all; see
// internal/csinode.NodeServer's own doc comment. Idempotent at two
// levels: this skips the gRPC calls entirely once status already records
// the result (mirroring reconcileHotplug's own "don't redo it every
// tick" pattern for AppliedVCPUs/AppliedMemoryMiB), and
// NodeStageVolume/NodePublishVolume are themselves safe to call again
// even if this level of caching were bypassed.
//
// A PV naming Kairon's own driver (csinode.DriverName) resolves here; one
// naming any other driver present in Agent.ThirdPartyCSIDrivers resolves
// via resolveThirdPartyCSIVolume instead (see its own doc comment for the
// real, narrower scope that path has); any other driver name is still
// refused outright, exactly as before third-party support existed.
func (a *Agent) resolveCSIVolume(ctx context.Context, m model.Machine, pv model.PersistentVolume) (string, csiVolumeStatus, error) {
	src := pv.Spec.CSI
	if src.Driver != csinode.DriverName {
		if _, ok := a.ThirdPartyCSIDrivers[src.Driver]; ok {
			return a.resolveThirdPartyCSIVolume(ctx, m, pv)
		}
		return "", csiVolumeStatus{}, fmt.Errorf("PersistentVolume %s names CSI driver %q -- only Kairon's own driver (%q) or a driver listed in this node's third-party allowlist can be used as a Machine boot disk", pv.Metadata.Name, src.Driver, csinode.DriverName)
	}
	if a.CSIStagingDir == "" || a.CSIPublishDir == "" {
		return "", csiVolumeStatus{}, fmt.Errorf("this node has no CSI staging/publish directory configured -- see docs/guides/machine-storage-csi.md")
	}

	status := m.Status
	if status.VolumeStagingPath != "" && status.VolumePublishPath != "" && status.VolumeHandle == src.VolumeHandle {
		return filepath.Join(status.VolumePublishPath, bootDiskFileName), csiVolumeStatus{
			StagingPath: status.VolumeStagingPath, PublishPath: status.VolumePublishPath, VolumeID: status.VolumeHandle,
		}, nil
	}

	client, err := a.csiNodeClient()
	if err != nil {
		return "", csiVolumeStatus{}, err
	}

	staging := filepath.Join(a.CSIStagingDir, pv.Metadata.Name)
	publish := filepath.Join(a.CSIPublishDir, pv.Metadata.Name)
	volumeCapability := &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: src.FSType}},
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
	}

	if _, err := client.NodeStageVolume(ctx, &csi.NodeStageVolumeRequest{
		VolumeId:          src.VolumeHandle,
		StagingTargetPath: staging,
		VolumeCapability:  volumeCapability,
		VolumeContext:     src.VolumeAttributes,
		// No Secrets: kairon-node's own direct-CSI-client path doesn't
		// resolve a nodeStageSecretRef the way kubelet normally would --
		// see docs/guides/machine-storage-csi.md for why (avoiding a
		// cluster-wide "read any Secret" RBAC grant for this path). CHAP
		// auth is only available when this driver is used via a real
		// Kubernetes Pod instead.
	}); err != nil {
		return "", csiVolumeStatus{}, fmt.Errorf("NodeStageVolume for PersistentVolume %s: %w", pv.Metadata.Name, err)
	}
	if _, err := client.NodePublishVolume(ctx, &csi.NodePublishVolumeRequest{
		VolumeId:          src.VolumeHandle,
		StagingTargetPath: staging,
		TargetPath:        publish,
		VolumeCapability:  volumeCapability,
		Readonly:          src.ReadOnly,
	}); err != nil {
		return "", csiVolumeStatus{}, fmt.Errorf("NodePublishVolume for PersistentVolume %s: %w", pv.Metadata.Name, err)
	}

	return filepath.Join(publish, bootDiskFileName), csiVolumeStatus{StagingPath: staging, PublishPath: publish, VolumeID: src.VolumeHandle}, nil
}

// teardownCSIVolume unpublishes and unstages a Machine's CSI-backed
// volume, if it ever resolved one -- called from cleanup before a
// Machine's finalizer is removed, the same "don't finish deleting until
// this succeeds" posture already applied to the FluxVM runtime delete
// call. A no-op (nil error) when the Machine never staged/published a
// CSI volume in the first place (the overwhelmingly common case: plain
// spec.image.path, or a hostPath/local-backed volume, neither of which
// this ever touches). Routes to teardownThirdPartyCSIVolume when
// status.volumeDriver names one -- see MachineStatus.VolumeDriver's own
// doc comment for why that's tracked in status rather than re-derived
// from the PV (which may already be gone by teardown time).
func (a *Agent) teardownCSIVolume(ctx context.Context, m model.Machine) error {
	if m.Status.VolumeStagingPath == "" && m.Status.VolumePublishPath == "" {
		return nil
	}
	if m.Status.VolumeDriver != "" {
		return a.teardownThirdPartyCSIVolume(ctx, m)
	}
	client, err := a.csiNodeClient()
	if err != nil {
		return err
	}
	if m.Status.VolumePublishPath != "" {
		if _, err := client.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{
			VolumeId: m.Status.VolumeHandle, TargetPath: m.Status.VolumePublishPath,
		}); err != nil {
			return fmt.Errorf("NodeUnpublishVolume: %w", err)
		}
	}
	if m.Status.VolumeStagingPath != "" {
		if _, err := client.NodeUnstageVolume(ctx, &csi.NodeUnstageVolumeRequest{
			VolumeId: m.Status.VolumeHandle, StagingTargetPath: m.Status.VolumeStagingPath,
		}); err != nil {
			return fmt.Errorf("NodeUnstageVolume: %w", err)
		}
	}
	return nil
}
