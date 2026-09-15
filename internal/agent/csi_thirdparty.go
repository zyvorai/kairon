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

	"github.com/zyvorai/kairon/internal/model"
)

// thirdPartyCSIClient dials (and caches) a connection to a configured
// third-party CSI driver's own Unix socket -- kairon-node acting as a
// generic CSI client, the same "no Pod for kubelet to trigger this
// through" reasoning csiNodeClient already has for Kairon's own driver,
// just against an operator-named socket instead of a-fixed one. Returns a
// clear error for a driver name not present in
// Agent.ThirdPartyCSIDrivers -- fail-closed, the same posture an unlisted
// VFIO BDF already has against KAIRON_VFIO_ALLOWLIST.
func (a *Agent) thirdPartyCSIClient(driver string) (csi.NodeClient, error) {
	socket, ok := a.ThirdPartyCSIDrivers[driver]
	if !ok {
		return nil, fmt.Errorf("CSI driver %q is not in this node's third-party allowlist (-third-party-csi-drivers/$KAIRON_THIRD_PARTY_CSI_DRIVERS) -- refusing to dial an unauthorized driver socket; see docs/guides/machine-storage-thirdparty-csi.md", driver)
	}
	if conn, ok := a.thirdPartyCSIConns[driver]; ok {
		return csi.NewNodeClient(conn), nil
	}
	conn, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial third-party CSI driver %q at %s: %w", driver, socket, err)
	}
	if a.thirdPartyCSIConns == nil {
		a.thirdPartyCSIConns = map[string]*grpc.ClientConn{}
	}
	a.thirdPartyCSIConns[driver] = conn
	return csi.NewNodeClient(conn), nil
}

// resolveThirdPartyCSIVolume is resolveCSIVolume's counterpart for a
// third-party driver -- deliberately narrower than that function's own
// Kairon-driver path in two real, documented ways (see
// docs/guides/machine-storage-thirdparty-csi.md):
//
//  1. No secrets are ever resolved or sent (Secrets stays nil in both
//     NodeStageVolumeRequest and NodePublishVolumeRequest) -- matching
//     Kairon's own driver's existing posture exactly, and for the same
//     reason: resolving a real Secret reference would need cluster-wide
//     Secret-read RBAC this project has deliberately never granted
//     kairon-node. This limits first-cut compatibility to drivers that
//     don't require secret-based node auth (Ceph-CSI/RBD is the
//     validated reference case).
//  2. staging/publish paths are synthesized from the Machine's own
//     deterministic RuntimeName() rather than a kubelet-assigned,
//     Pod-UID-keyed path -- there is no Pod here for a Pod UID to come
//     from. Most CSI drivers treat these paths as opaque, but a driver
//     that specifically depends on kubelet's own path convention (some
//     drivers key internal state off it) may not behave correctly here;
//     a real, named compatibility risk, not a hidden one.
func (a *Agent) resolveThirdPartyCSIVolume(ctx context.Context, m model.Machine, pv model.PersistentVolume) (string, csiVolumeStatus, error) {
	src := pv.Spec.CSI
	client, err := a.thirdPartyCSIClient(src.Driver)
	if err != nil {
		return "", csiVolumeStatus{}, err
	}

	if m.Status.VolumeStagingPath != "" && m.Status.VolumePublishPath != "" && m.Status.VolumeHandle == src.VolumeHandle && m.Status.VolumeDriver == src.Driver {
		return filepath.Join(m.Status.VolumePublishPath, bootDiskFileName), csiVolumeStatus{
			StagingPath: m.Status.VolumeStagingPath, PublishPath: m.Status.VolumePublishPath, VolumeID: m.Status.VolumeHandle, Driver: src.Driver,
		}, nil
	}

	staging := filepath.Join(a.CSIStagingDir, "thirdparty", src.Driver, m.RuntimeName())
	publish := filepath.Join(a.CSIPublishDir, "thirdparty", src.Driver, m.RuntimeName())
	volumeCapability := &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: src.FSType}},
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
	}

	if _, err := client.NodeStageVolume(ctx, &csi.NodeStageVolumeRequest{
		VolumeId:          src.VolumeHandle,
		StagingTargetPath: staging,
		VolumeCapability:  volumeCapability,
		VolumeContext:     src.VolumeAttributes,
		// Deliberately no Secrets -- see this function's own doc comment.
	}); err != nil {
		return "", csiVolumeStatus{}, fmt.Errorf("NodeStageVolume for PersistentVolume %s (driver %s): %w", pv.Metadata.Name, src.Driver, err)
	}
	if _, err := client.NodePublishVolume(ctx, &csi.NodePublishVolumeRequest{
		VolumeId:          src.VolumeHandle,
		StagingTargetPath: staging,
		TargetPath:        publish,
		VolumeCapability:  volumeCapability,
		Readonly:          src.ReadOnly,
	}); err != nil {
		return "", csiVolumeStatus{}, fmt.Errorf("NodePublishVolume for PersistentVolume %s (driver %s): %w", pv.Metadata.Name, src.Driver, err)
	}

	return filepath.Join(publish, bootDiskFileName), csiVolumeStatus{StagingPath: staging, PublishPath: publish, VolumeID: src.VolumeHandle, Driver: src.Driver}, nil
}

// teardownThirdPartyCSIVolume is teardownCSIVolume's counterpart for a
// Machine whose status.volumeDriver names a third-party driver.
func (a *Agent) teardownThirdPartyCSIVolume(ctx context.Context, m model.Machine) error {
	client, err := a.thirdPartyCSIClient(m.Status.VolumeDriver)
	if err != nil {
		return err
	}
	if m.Status.VolumePublishPath != "" {
		if _, err := client.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{
			VolumeId: m.Status.VolumeHandle, TargetPath: m.Status.VolumePublishPath,
		}); err != nil {
			return fmt.Errorf("NodeUnpublishVolume (driver %s): %w", m.Status.VolumeDriver, err)
		}
	}
	if m.Status.VolumeStagingPath != "" {
		if _, err := client.NodeUnstageVolume(ctx, &csi.NodeUnstageVolumeRequest{
			VolumeId: m.Status.VolumeHandle, StagingTargetPath: m.Status.VolumeStagingPath,
		}); err != nil {
			return fmt.Errorf("NodeUnstageVolume (driver %s): %w", m.Status.VolumeDriver, err)
		}
	}
	return nil
}
