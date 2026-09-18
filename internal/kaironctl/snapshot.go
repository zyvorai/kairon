// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kaironctl

import (
	"context"
	"flag"
	"fmt"
	"regexp"
	"time"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

func cmdSnapshot(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl snapshot MACHINE [--name NAME] [--class CSI_CLASS]"))
	}
	machine := args[0]
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	name := fs.String("name", "", "MachineSnapshot name")
	class := fs.String("class", "", "VolumeSnapshotClass name")
	_ = fs.Parse(args[1:])
	if *name == "" {
		*name = resourceName(machine + "-" + time.Now().UTC().Format("20060102-150405"))
	}
	snapshot := model.MachineSnapshot{
		TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachineSnapshot},
		Metadata: model.ObjectMeta{Name: *name, Namespace: *ns},
		Spec:     model.MachineSnapshotSpec{MachineName: machine, VolumeSnapshotClassName: *class},
	}
	out, err := kc.CreateMachineSnapshot(ctx, *ns, snapshot)
	if err != nil {
		fatal(err)
	}
	okf("machinesnapshot/%s created", out.Metadata.Name)
}

func cmdRestore(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl restore SNAPSHOT --target-claim NAME [--volume NAME] [flags]"))
	}
	snapshot := args[0]
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	name := fs.String("name", "", "MachineSnapshotRestore name")
	volume := fs.String("volume", "", "which snapshotted volume to restore; required when the snapshot covers more than one")
	targetClaim := fs.String("target-claim", "", "name for the new PersistentVolumeClaim (required)")
	storageClass := fs.String("storage-class", "", "StorageClass for the new PVC; empty uses the cluster default")
	storageSize := fs.String("storage-size", "", "size for the new PVC; empty defaults to the VolumeSnapshot's own reported restoreSize")
	_ = fs.Parse(args[1:])
	if *targetClaim == "" {
		fatal(fmt.Errorf("--target-claim is required"))
	}
	if *name == "" {
		*name = resourceName(snapshot + "-restore-" + time.Now().UTC().Format("20060102-150405"))
	}
	restore := model.MachineSnapshotRestore{
		TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachineSnapshotRestore},
		Metadata: model.ObjectMeta{Name: *name, Namespace: *ns},
		Spec: model.MachineSnapshotRestoreSpec{
			SnapshotName:     snapshot,
			VolumeName:       *volume,
			TargetClaimName:  *targetClaim,
			StorageClassName: *storageClass,
			StorageSize:      *storageSize,
		},
	}
	out, err := kc.CreateMachineSnapshotRestore(ctx, *ns, restore)
	if err != nil {
		fatal(err)
	}
	okf("machinesnapshotrestore/%s created", out.Metadata.Name)
	fmt.Printf("once Succeeded, point a new Machine's spec.volumes[0].claimName at %q\n", *targetClaim)
}

var invalidResourceName = regexp.MustCompile(`[^a-z0-9-]+`)
