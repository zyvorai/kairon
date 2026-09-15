// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"strings"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

// reconcileSnapshotRestore restores one volume of a Succeeded
// MachineSnapshot into a brand-new PersistentVolumeClaim via the standard
// CSI spec.dataSource restore flow. See model.MachineSnapshotRestore for
// why this deliberately doesn't also create a Machine.
//
// The referenced MachineSnapshot (or its underlying VolumeSnapshot) simply
// not being ready *yet* is parked as Pending and retried next tick, not
// treated as an error -- the same "waiting on an external condition"
// posture reconcileVolumeSnapshots and finishRestoreFromPVCState already
// use elsewhere in this same feature. Before this, both cases returned a
// plain error, which the caller (Controller.Reconcile) turns into a
// permanent status.phase=Failed with no further retries -- so creating a
// MachineSnapshotRestore even slightly before its MachineSnapshot finished
// left it stuck Failed forever, even though the snapshot went on to
// succeed moments later. A genuinely missing/misspelled SnapshotName still
// fails outright (below) -- there's no way to tell "will exist later" from
// "will never exist", unlike a snapshot that already exists but is still
// in progress.
func (c *Controller) reconcileSnapshotRestore(ctx context.Context, restore model.MachineSnapshotRestore) error {
	if restore.Status.Phase == "Succeeded" || restore.Status.Phase == "Failed" {
		return nil
	}
	if strings.TrimSpace(restore.Spec.SnapshotName) == "" || strings.TrimSpace(restore.Spec.TargetClaimName) == "" {
		return fmt.Errorf("spec.snapshotName and spec.targetClaimName are required")
	}
	snapshot, err := c.Kube.GetMachineSnapshot(ctx, restore.Namespace(), restore.Spec.SnapshotName)
	if err != nil {
		return fmt.Errorf("get MachineSnapshot %s: %w", restore.Spec.SnapshotName, err)
	}
	if snapshot.Status.Phase != "Succeeded" || !snapshot.Status.ReadyToUse {
		return c.parkRestorePending(ctx, restore, fmt.Sprintf("waiting for MachineSnapshot %s to become ready to restore from (phase=%q)", restore.Spec.SnapshotName, snapshot.Status.Phase))
	}
	ref, err := selectSnapshotVolume(snapshot.Status.VolumeSnapshots, restore.Spec.VolumeName)
	if err != nil {
		return err
	}
	if !ref.ReadyToUse {
		return c.parkRestorePending(ctx, restore, fmt.Sprintf("waiting for VolumeSnapshot %s to become ready to use", ref.VolumeSnapshotName))
	}

	existing, err := c.Kube.GetPersistentVolumeClaim(ctx, restore.Namespace(), restore.Spec.TargetClaimName)
	if err != nil && !kube.IsNotFound(err) {
		return err
	}
	if err == nil && existing.Metadata.Name != "" {
		return c.finishRestoreFromPVCState(ctx, restore, existing)
	}

	size := restore.Spec.StorageSize
	if size == "" {
		vs, err := c.Kube.GetVolumeSnapshot(ctx, restore.Namespace(), ref.VolumeSnapshotName)
		if err != nil {
			return fmt.Errorf("get VolumeSnapshot %s: %w", ref.VolumeSnapshotName, err)
		}
		if vs.Status.RestoreSize == nil || strings.TrimSpace(*vs.Status.RestoreSize) == "" {
			return fmt.Errorf("VolumeSnapshot %s reports no restoreSize; set spec.storageSize explicitly", ref.VolumeSnapshotName)
		}
		size = *vs.Status.RestoreSize
	}

	pvc := model.PersistentVolumeClaim{
		TypeMeta: model.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
		Metadata: model.ObjectMeta{Name: restore.Spec.TargetClaimName, Namespace: restore.Namespace(), Labels: map[string]string{
			"kairon.zyvor.dev/restored-from-snapshot": snapshot.Metadata.Name,
		}},
		Spec: model.PersistentVolumeClaimSpec{
			AccessModes: []string{"ReadWriteOnce"},
			Resources:   &model.PersistentVolumeClaimResources{Requests: map[string]string{"storage": size}},
			DataSource:  &model.TypedLocalObjectReference{APIGroup: "snapshot.storage.k8s.io", Kind: "VolumeSnapshot", Name: ref.VolumeSnapshotName},
		},
	}
	if restore.Spec.StorageClassName != "" {
		pvc.Spec.StorageClassName = &restore.Spec.StorageClassName
	}
	created, err := c.Kube.CreatePersistentVolumeClaim(ctx, restore.Namespace(), pvc)
	if err != nil {
		return fmt.Errorf("create restored PersistentVolumeClaim %s: %w", restore.Spec.TargetClaimName, err)
	}
	return c.finishRestoreFromPVCState(ctx, restore, created)
}

// finishRestoreFromPVCState reports the restore as Succeeded once the
// target PVC is Bound. A freshly-created PVC under a WaitForFirstConsumer
// StorageClass stays Pending until something (a new Machine referencing
// it) is scheduled -- that's normal, not a failure, so an unbound PVC just
// leaves the restore Pending to be re-checked next reconcile tick, exactly
// like every other "waiting on an external condition" status in this
// project (e.g. MachineSnapshot itself waiting on its VolumeSnapshots).
func (c *Controller) finishRestoreFromPVCState(ctx context.Context, restore model.MachineSnapshotRestore, pvc model.PersistentVolumeClaim) error {
	status := restore.Status
	status.RestoredClaimName = pvc.Metadata.Name
	if pvc.Status.Phase == "Bound" {
		status.Phase = "Succeeded"
		status.Message = fmt.Sprintf("PersistentVolumeClaim %s is Bound and ready to use", pvc.Metadata.Name)
	} else {
		status.Phase = "Pending"
		status.Message = fmt.Sprintf("waiting for PersistentVolumeClaim %s to bind (phase=%q; normal under a WaitForFirstConsumer StorageClass until a Machine references it)", pvc.Metadata.Name, pvc.Status.Phase)
	}
	return c.Kube.PatchMachineSnapshotRestoreStatus(ctx, restore.Namespace(), restore.Metadata.Name, status)
}

// parkRestorePending records message as a Pending status and returns nil
// (not an error) -- so Controller.Reconcile's own per-item error handling
// never marks this restore Failed for what's actually a normal,
// automatically-retried wait, and never counts it against
// kairon_reconcile_item_errors_total{kind="snapshotrestore"} either.
func (c *Controller) parkRestorePending(ctx context.Context, restore model.MachineSnapshotRestore, message string) error {
	status := restore.Status
	status.Phase = "Pending"
	status.Message = message
	return c.Kube.PatchMachineSnapshotRestoreStatus(ctx, restore.Namespace(), restore.Metadata.Name, status)
}

func selectSnapshotVolume(refs []model.VolumeSnapshotReference, name string) (model.VolumeSnapshotReference, error) {
	if name == "" {
		if len(refs) == 1 {
			return refs[0], nil
		}
		return model.VolumeSnapshotReference{}, fmt.Errorf("spec.volumeName is required when the snapshot covers more than one volume")
	}
	for _, r := range refs {
		if r.VolumeName == name {
			return r, nil
		}
	}
	return model.VolumeSnapshotReference{}, fmt.Errorf("snapshot has no volume named %q", name)
}
