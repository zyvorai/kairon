// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/zyvorai/kairon/internal/model"
)

// bootDiskFileName is the fixed filename Kairon looks for inside a
// PersistentVolume's directory when a Machine boots from spec.volumes
// instead of spec.image.path. There's no per-volume field for this today --
// one Machine, one boot disk, one well-known name -- matching the scope
// decision in docs/guides/machine-storage.md (PVC-backed disks v1: a single
// boot volume, Filesystem-mode, hostPath/local only).
const bootDiskFileName = "disk.img"

// resolveBootDiskPath returns the real disk path FluxVM should boot this
// Machine from. When spec.volumes is set, the first entry is resolved
// through its bound PersistentVolume to a real host directory; otherwise
// spec.image.path is used unchanged (today's only supported path).
func (a *Agent) resolveBootDiskPath(ctx context.Context, m model.Machine) (string, error) {
	if len(m.Spec.Volumes) == 0 {
		return m.Spec.Image.Path, nil
	}
	vol := m.Spec.Volumes[0]
	if strings.TrimSpace(vol.ClaimName) == "" {
		return "", fmt.Errorf("spec.volumes[0] requires claimName")
	}
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
	dir, err := hostDirForPV(pv)
	if err != nil {
		return "", fmt.Errorf("volume %q (claim %s, PV %s): %w", vol.Name, vol.ClaimName, pvc.Spec.VolumeName, err)
	}
	return filepath.Join(dir, bootDiskFileName), nil
}

// hostDirForPV returns the real host directory a Filesystem-mode PV's disk
// image lives in. Only hostPath and local volume sources are supported: both
// name a path already present on whatever node the PV was provisioned for,
// which is exactly what an unattended kairon-node (no CSI node-plugin
// attach/mount step of its own) can use directly. Network-block CSI
// volumes need that attach/mount step done by something else first --
// refused with a clear error rather than silently guessing a path.
func hostDirForPV(pv model.PersistentVolume) (string, error) {
	if pv.Spec.VolumeMode != "" && pv.Spec.VolumeMode != "Filesystem" {
		return "", fmt.Errorf("volumeMode %q is not supported as a Machine boot disk yet; only Filesystem-mode PersistentVolumes backed by hostPath or local can be used", pv.Spec.VolumeMode)
	}
	if pv.Spec.HostPath != nil && strings.TrimSpace(pv.Spec.HostPath.Path) != "" {
		return pv.Spec.HostPath.Path, nil
	}
	if pv.Spec.Local != nil && strings.TrimSpace(pv.Spec.Local.Path) != "" {
		return pv.Spec.Local.Path, nil
	}
	return "", fmt.Errorf("only hostPath- or local-backed PersistentVolumes can be used as a Machine boot disk today")
}
