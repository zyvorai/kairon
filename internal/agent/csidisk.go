// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/container-storage-interface/spec/lib/go/csi"

	"github.com/zyvorai/kairon/internal/csinode"
	"github.com/zyvorai/kairon/internal/model"
)

// diskVolumePath is the path FluxVM attaches for a published disk volume.
func diskVolumePath(v model.DiskVolume) string {
	if v.Block {
		return v.PublishPath
	}
	return filepath.Join(v.PublishPath, bootDiskFileName)
}

// diskCSIClient returns the node client for driver: Kairon's own driver or
// one on this node's third-party allowlist.
func (a *Agent) diskCSIClient(driver string) (csi.NodeClient, error) {
	if driver == csinode.DriverName {
		return a.csiNodeClient()
	}
	if _, ok := a.ThirdPartyCSIDrivers[driver]; ok {
		return a.thirdPartyCSIClient(driver)
	}
	return nil, fmt.Errorf("CSI driver %q is neither Kairon's own (%q) nor on this node's third-party allowlist", driver, csinode.DriverName)
}

// publishDiskCSI stages and publishes the CSI-backed pv for spec.disks
// entry disk, under paths keyed by the Machine's runtime name and the disk
// name so one volume per disk can coexist with the boot volume.
func (a *Agent) publishDiskCSI(ctx context.Context, m model.Machine, disk string, pv model.PersistentVolume) (model.DiskVolume, error) {
	src := pv.Spec.CSI
	if a.CSIStagingDir == "" || a.CSIPublishDir == "" {
		return model.DiskVolume{}, fmt.Errorf("this node has no CSI staging/publish directory configured -- see docs/guides/machine-storage-csi.md")
	}
	block := pv.Spec.VolumeMode == "Block"
	own := src.Driver == csinode.DriverName
	if own && block {
		return model.DiskVolume{}, fmt.Errorf("PersistentVolume %s is Block-mode, which Kairon's own CSI driver does not serve", pv.Metadata.Name)
	}
	client, err := a.diskCSIClient(src.Driver)
	if err != nil {
		return model.DiskVolume{}, err
	}
	var publishContext, stageSecrets, publishSecrets map[string]string
	if own {
		if stageSecrets, err = a.resolveCSIChapSecrets(ctx, src); err != nil {
			return model.DiskVolume{}, err
		}
	} else {
		if publishContext, err = a.ensureCSIAttachment(ctx, pv); err != nil {
			return model.DiskVolume{}, err
		}
		if stageSecrets, err = a.resolveThirdPartyCSISecrets(ctx, "nodeStageSecretRef", src.NodeStageSecretRef); err != nil {
			return model.DiskVolume{}, err
		}
		if publishSecrets, err = a.resolveThirdPartyCSISecrets(ctx, "nodePublishSecretRef", src.NodePublishSecretRef); err != nil {
			return model.DiskVolume{}, err
		}
	}
	v := model.DiskVolume{
		Name:         disk,
		Node:         a.NodeName,
		Driver:       src.Driver,
		VolumeHandle: src.VolumeHandle,
		StagingPath:  filepath.Join(a.CSIStagingDir, "disks", src.Driver, m.RuntimeName(), disk),
		PublishPath:  filepath.Join(a.CSIPublishDir, "disks", src.Driver, m.RuntimeName(), disk),
		Block:        block,
	}
	capability := &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: src.FSType}},
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
	}
	if block {
		capability.AccessType = &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}
		if err := os.MkdirAll(filepath.Dir(v.PublishPath), 0o750); err != nil {
			return model.DiskVolume{}, fmt.Errorf("create CSI publish parent for PersistentVolume %s: %w", pv.Metadata.Name, err)
		}
	}
	if _, err := client.NodeStageVolume(ctx, &csi.NodeStageVolumeRequest{
		VolumeId:          src.VolumeHandle,
		PublishContext:    publishContext,
		StagingTargetPath: v.StagingPath,
		VolumeCapability:  capability,
		VolumeContext:     src.VolumeAttributes,
		Secrets:           stageSecrets,
	}); err != nil {
		return model.DiskVolume{}, fmt.Errorf("NodeStageVolume for PersistentVolume %s (driver %s): %w", pv.Metadata.Name, src.Driver, err)
	}
	if _, err := client.NodePublishVolume(ctx, &csi.NodePublishVolumeRequest{
		VolumeId:          src.VolumeHandle,
		PublishContext:    publishContext,
		StagingTargetPath: v.StagingPath,
		TargetPath:        v.PublishPath,
		VolumeCapability:  capability,
		VolumeContext:     src.VolumeAttributes,
		Readonly:          src.ReadOnly,
		Secrets:           publishSecrets,
	}); err != nil {
		return model.DiskVolume{}, fmt.Errorf("NodePublishVolume for PersistentVolume %s (driver %s): %w", pv.Metadata.Name, src.Driver, err)
	}
	return v, nil
}

// unpublishDiskCSI reverses publishDiskCSI, releasing the VolumeAttachment
// last for a third-party driver.
func (a *Agent) unpublishDiskCSI(ctx context.Context, v model.DiskVolume) error {
	client, err := a.diskCSIClient(v.Driver)
	if err != nil {
		return err
	}
	if _, err := client.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: v.VolumeHandle, TargetPath: v.PublishPath}); err != nil {
		return fmt.Errorf("NodeUnpublishVolume for disk %s (driver %s): %w", v.Name, v.Driver, err)
	}
	if _, err := client.NodeUnstageVolume(ctx, &csi.NodeUnstageVolumeRequest{VolumeId: v.VolumeHandle, StagingTargetPath: v.StagingPath}); err != nil {
		return fmt.Errorf("NodeUnstageVolume for disk %s (driver %s): %w", v.Name, v.Driver, err)
	}
	if v.Driver == csinode.DriverName {
		return nil
	}
	return a.releaseCSIAttachment(ctx, v.Driver, v.VolumeHandle)
}

// teardownDiskVolumes unpublishes every disk volume this node published
// for m. Entries recorded by another node are left for that node.
func (a *Agent) teardownDiskVolumes(ctx context.Context, m model.Machine) error {
	for _, v := range m.Status.DiskVolumes {
		if v.Node != a.NodeName {
			continue
		}
		if err := a.unpublishDiskCSI(ctx, v); err != nil {
			return err
		}
	}
	return nil
}
