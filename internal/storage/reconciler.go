package storage

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

// Reconciler binds MachineImage and VirtualDisk status (PVC→hostPath / local path).
type Reconciler struct {
	Kube *kube.Client
	Log  *slog.Logger
}

func (r *Reconciler) Reconcile(ctx context.Context) error {
	if err := r.reconcileImages(ctx); err != nil {
		return err
	}
	return r.reconcileDisks(ctx)
}

func (r *Reconciler) reconcileImages(ctx context.Context) error {
	images, err := r.Kube.ListMachineImages(ctx)
	if err != nil {
		return err
	}
	for _, img := range images {
		status := img.Status
		status.ObservedGeneration = img.Metadata.Generation
		switch {
		case img.Spec.Source.Path != "":
			status.Phase = "Ready"
			status.ResolvedPath = img.Spec.Source.Path
			status.Message = ""
		case img.Spec.Source.OCI != "":
			// OCI pull/distribution lands with GuestKit; declare digest expectation only.
			status.Phase = "Pending"
			status.ResolvedPath = ""
			status.Message = fmt.Sprintf("OCI source %q declared; stage image to a host path and set source.path (digest %s)", img.Spec.Source.OCI, img.Spec.Digest)
		default:
			status.Phase = "Failed"
			status.Message = "spec.source.path or spec.source.oci is required"
		}
		if err := r.Kube.PatchMachineImageStatus(ctx, img.Namespace(), img.Metadata.Name, status); err != nil {
			r.Log.Error("patch MachineImage status", "name", img.Metadata.Name, "error", err)
		}
	}
	return nil
}

func (r *Reconciler) reconcileDisks(ctx context.Context) error {
	disks, err := r.Kube.ListVirtualDisks(ctx)
	if err != nil {
		return err
	}
	for _, d := range disks {
		status := d.Status
		status.ObservedGeneration = d.Metadata.Generation
		path, vol, msg, phase := resolveDisk(ctx, r.Kube, d)
		status.Path = path
		status.VolumeName = vol
		status.Message = msg
		status.Phase = phase
		if err := r.Kube.PatchVirtualDiskStatus(ctx, d.Namespace(), d.Metadata.Name, status); err != nil {
			r.Log.Error("patch VirtualDisk status", "name", d.Metadata.Name, "error", err)
		}
	}
	return nil
}

func resolveDisk(ctx context.Context, kc *kube.Client, d model.VirtualDisk) (path, volumeName, message, phase string) {
	if d.Spec.CloneFromDisk != "" || d.Spec.CloneFromSnapshot != "" {
		srcName := d.Spec.CloneFromDisk
		if srcName == "" {
			return "", "", "cloneFromSnapshot requires a VirtualDisk cloneFromDisk target path after snapshot export", "Pending"
		}
		src, err := kc.GetVirtualDisk(ctx, d.Namespace(), srcName)
		if err != nil {
			return "", "", fmt.Sprintf("clone source: %v", err), "Pending"
		}
		if src.Status.Phase != "Bound" || src.Status.Path == "" {
			return "", "", fmt.Sprintf("clone source %q not Bound yet", srcName), "Pending"
		}
		// Clone copies the source path reference; bit-copy is GuestKit/CSI work.
		return src.Status.Path, src.Status.VolumeName, fmt.Sprintf("cloned reference from %s", srcName), "Bound"
	}
	if d.Spec.Source.HostPath != "" {
		return d.Spec.Source.HostPath, "", "", "Bound"
	}
	if d.Spec.Source.PersistentVolumeClaim == nil || d.Spec.Source.PersistentVolumeClaim.ClaimName == "" {
		return "", "", "spec.source.hostPath or persistentVolumeClaim is required", "Failed"
	}
	pvc, err := kc.GetPVC(ctx, d.Namespace(), d.Spec.Source.PersistentVolumeClaim.ClaimName)
	if err != nil {
		return "", "", fmt.Sprintf("get PVC: %v", err), "Pending"
	}
	if pvc.Spec.VolumeName == "" {
		return "", "", "PVC not bound to a volume yet", "Pending"
	}
	pv, err := kc.GetPV(ctx, pvc.Spec.VolumeName)
	if err != nil {
		return "", pvc.Spec.VolumeName, fmt.Sprintf("get PV: %v", err), "Pending"
	}
	if pv.Spec.HostPath != nil && pv.Spec.HostPath.Path != "" {
		return pv.Spec.HostPath.Path, pvc.Spec.VolumeName, "", "Bound"
	}
	if pv.Spec.Local != nil && pv.Spec.Local.Path != "" {
		return pv.Spec.Local.Path, pvc.Spec.VolumeName, "", "Bound"
	}
	if pv.Spec.CSI != nil {
		return "", pvc.Spec.VolumeName, "CSI PV bound; mount path publishing requires node CSI integration (pending)", "Pending"
	}
	return "", pvc.Spec.VolumeName, "PV has no hostPath/local path for FluxVM", "Failed"
}

func (r *Reconciler) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := r.Reconcile(ctx); err != nil {
			r.Log.Error("storage reconcile failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func NormalizeStorage(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "", "default":
		return "default"
	case "lvm-thin", "nbd", "ceph-rbd":
		return s
	default:
		return s
	}
}
