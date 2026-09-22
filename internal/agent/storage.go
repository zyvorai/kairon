// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/zyvorai/kairon/internal/fluxvm"
	"github.com/zyvorai/kairon/internal/model"
)

// bootDiskFileName is the fixed filename Kairon looks for inside a
// PersistentVolume's directory when a Machine boots from spec.volumes
// instead of spec.image.path. There's no per-volume field for this today --
// one Machine, one boot disk, one well-known name -- matching the scope
// decision in docs/guides/machine-storage.md (PVC-backed disks v1: a single
// boot volume, Filesystem-mode).
const bootDiskFileName = "disk.img"

// resolveBootDiskPath returns the real disk path FluxVM should boot this
// Machine from, and (only for a CSI-backed volume) the staging/publish
// paths/volume ID reconcileMachine needs to persist into status -- see
// csiVolumeStatus. When spec.volumes is set, the first entry is resolved
// through its bound PersistentVolume; otherwise spec.image.path is used
// unchanged.
func (a *Agent) resolveBootDiskPath(ctx context.Context, m model.Machine) (string, csiVolumeStatus, error) {
	if len(m.Spec.Volumes) == 0 {
		return m.Spec.Image.Path, csiVolumeStatus{}, nil
	}
	vol := m.Spec.Volumes[0]
	if strings.TrimSpace(vol.ClaimName) == "" {
		return "", csiVolumeStatus{}, fmt.Errorf("spec.volumes[0] requires claimName")
	}
	pvc, err := a.Kube.GetPersistentVolumeClaim(ctx, m.Namespace(), vol.ClaimName)
	if err != nil {
		return "", csiVolumeStatus{}, fmt.Errorf("get PersistentVolumeClaim %s: %w", vol.ClaimName, err)
	}
	if pvc.Status.Phase != "Bound" || strings.TrimSpace(pvc.Spec.VolumeName) == "" {
		return "", csiVolumeStatus{}, fmt.Errorf("PersistentVolumeClaim %s is not Bound yet (phase=%q)", vol.ClaimName, pvc.Status.Phase)
	}
	pv, err := a.Kube.GetPersistentVolume(ctx, pvc.Spec.VolumeName)
	if err != nil {
		return "", csiVolumeStatus{}, fmt.Errorf("get PersistentVolume %s: %w", pvc.Spec.VolumeName, err)
	}
	if pv.Spec.VolumeMode != "" && pv.Spec.VolumeMode != "Filesystem" {
		return "", csiVolumeStatus{}, fmt.Errorf("volume %q (claim %s, PV %s): volumeMode %q is not supported as a Machine boot disk; only Filesystem-mode PersistentVolumes are", vol.Name, vol.ClaimName, pvc.Spec.VolumeName, pv.Spec.VolumeMode)
	}
	if pv.Spec.CSI != nil {
		path, volStatus, err := a.resolveCSIVolume(ctx, m, pv)
		if err != nil {
			return "", csiVolumeStatus{}, fmt.Errorf("volume %q (claim %s, PV %s): %w", vol.Name, vol.ClaimName, pvc.Spec.VolumeName, err)
		}
		return path, volStatus, nil
	}
	dir, err := hostDirForPV(pv)
	if err != nil {
		return "", csiVolumeStatus{}, fmt.Errorf("volume %q (claim %s, PV %s): %w", vol.Name, vol.ClaimName, pvc.Spec.VolumeName, err)
	}
	return filepath.Join(dir, bootDiskFileName), csiVolumeStatus{}, nil
}

// resolveDataVolumes maps spec.volumes[1+] PVC host directories into FluxVM
// virtiofs SharedFolders. volumes[0] remains the boot disk (disk.img).
// FluxVM has no multi-block-disk create API; virtiofs is the supported
// first cut for additional volumes (QEMU only).
func (a *Agent) resolveDataVolumes(ctx context.Context, m model.Machine) ([]fluxvm.SharedFolder, error) {
	if len(m.Spec.Volumes) <= 1 {
		return nil, nil
	}
	out := make([]fluxvm.SharedFolder, 0, len(m.Spec.Volumes)-1)
	for i, vol := range m.Spec.Volumes[1:] {
		if strings.TrimSpace(vol.ClaimName) == "" {
			return nil, fmt.Errorf("spec.volumes[%d] requires claimName", i+1)
		}
		dir, err := a.resolveVolumeHostDir(ctx, m, vol, i+1)
		if err != nil {
			return nil, err
		}
		guest := strings.TrimSpace(vol.GuestPath)
		if guest == "" {
			name := strings.TrimSpace(vol.Name)
			if name == "" {
				name = fmt.Sprintf("vol%d", i+1)
			}
			guest = "/mnt/" + name
		}
		out = append(out, fluxvm.SharedFolder{
			HostPath:  dir,
			GuestPath: guest,
			ReadOnly:  vol.ReadOnly,
		})
	}
	return out, nil
}

// resolveVolumeHostDir returns the host directory for a PVC-backed volume
// (hostPath/local dir, or CSI publish path parent). Used for virtiofs shares.
func (a *Agent) resolveVolumeHostDir(ctx context.Context, m model.Machine, vol model.MachineVolume, idx int) (string, error) {
	pvc, err := a.Kube.GetPersistentVolumeClaim(ctx, m.Namespace(), vol.ClaimName)
	if err != nil {
		return "", fmt.Errorf("get PersistentVolumeClaim %s: %w", vol.ClaimName, err)
	}
	if pvc.Status.Phase != "Bound" || strings.TrimSpace(pvc.Spec.VolumeName) == "" {
		return "", fmt.Errorf("PersistentVolumeClaim %s is not Bound yet (phase=%q)", vol.ClaimName, pvc.Status.Phase)
	}
	pv, err := a.Kube.GetPersistentVolume(ctx, pvc.Spec.VolumeName)
	if err != nil {
		return "", fmt.Errorf("get PersistentVolume %s: %w", pvc.Spec.VolumeName, err)
	}
	if pv.Spec.VolumeMode != "" && pv.Spec.VolumeMode != "Filesystem" {
		return "", fmt.Errorf("volume %q (claim %s): volumeMode %q is not supported for data volumes; only Filesystem", vol.Name, vol.ClaimName, pv.Spec.VolumeMode)
	}
	if pv.Spec.CSI != nil {
		path, _, err := a.resolveCSIVolume(ctx, m, pv)
		if err != nil {
			return "", fmt.Errorf("volume %q (claim %s): %w", vol.Name, vol.ClaimName, err)
		}
		// CSI publish path is typically .../disk.img or a mount point; virtiofs
		// wants a directory — use the parent when path looks like a file.
		if strings.HasSuffix(path, bootDiskFileName) {
			return filepath.Dir(path), nil
		}
		return path, nil
	}
	dir, err := hostDirForPV(pv)
	if err != nil {
		return "", fmt.Errorf("volume %q (claim %s, index %d): %w", vol.Name, vol.ClaimName, idx, err)
	}
	return dir, nil
}

// hostDirForPV returns the real host directory a hostPath- or
// local-backed PV's disk image lives in -- both name a path already
// present on whatever node the PV was provisioned for, which is exactly
// what kairon-node can use directly, no attach/mount step needed. A
// CSI-backed PV goes through resolveCSIVolume instead (see
// resolveBootDiskPath), never through here.
func hostDirForPV(pv model.PersistentVolume) (string, error) {
	if pv.Spec.HostPath != nil && strings.TrimSpace(pv.Spec.HostPath.Path) != "" {
		return pv.Spec.HostPath.Path, nil
	}
	if pv.Spec.Local != nil && strings.TrimSpace(pv.Spec.Local.Path) != "" {
		return pv.Spec.Local.Path, nil
	}
	return "", fmt.Errorf("only hostPath- or local-backed PersistentVolumes can be used as a Machine boot disk here (a CSI-backed one is resolved separately, see resolveCSIVolume)")
}
