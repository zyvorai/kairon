// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kaironctl

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/zyvorai/kairon/internal/kaironctl/style"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

const backupUsage = `usage:
  kaironctl backup create MACHINE [--name NAME] [--quiesce auto|required|never] [--atlas] [--atlas-bucket ID] [--atlas-volume NAME]... [--keep N] [--max-age-seconds N]
  kaironctl backup list
  kaironctl backup restore BACKUP [--machine NAME] [--storage-class NAME] [--name NAME]
  kaironctl backup delete NAME`

func cmdBackup(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("%s", backupUsage))
	}
	fs := flag.NewFlagSet("backup "+args[0], flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	switch args[0] {
	case "create":
		if len(args) < 2 {
			fatal(fmt.Errorf("%s", backupUsage))
		}
		name := fs.String("name", "", "MachineBackup name (default MACHINE-<utc>)")
		quiesce := fs.String("quiesce", "", "guest filesystem freeze: auto (default), required or never")
		useAtlas := fs.Bool("atlas", false, "also back up the Machine's Atlas volumes to S3")
		bucket := fs.String("atlas-bucket", "", "Atlas bucket id (implies --atlas; default: the controller's --atlas-backup-bucket)")
		var volumes stringSliceFlag
		fs.Var(&volumes, "atlas-volume", "back up only this Atlas volume (repeatable; implies --atlas)")
		keep := fs.Int64("keep", 0, "Atlas keeps at most this many backups per volume")
		maxAge := fs.Int64("max-age-seconds", 0, "Atlas prunes backups of the volume older than this")
		_ = fs.Parse(args[2:])
		var atlasSpec *model.MachineBackupAtlas
		if *useAtlas || *bucket != "" || len(volumes) > 0 || *keep > 0 || *maxAge > 0 {
			atlasSpec = &model.MachineBackupAtlas{BucketID: *bucket, VolumeNames: volumes, Keep: *keep, MaxAgeSeconds: *maxAge}
		}
		out, err := createBackup(ctx, kc, *ns, args[1], *name, *quiesce, atlasSpec)
		if err != nil {
			fatal(err)
		}
		okf("machinebackup/%s created", out.Metadata.Name)
	case "list":
		_ = fs.Parse(args[1:])
		items, err := kc.ListMachineBackupsNamespace(ctx, *ns)
		if err != nil {
			fatal(err)
		}
		printBackups(items)
	case "restore":
		if len(args) < 2 {
			fatal(fmt.Errorf("%s", backupUsage))
		}
		name := fs.String("name", "", "MachineBackupRestore name (default BACKUP-restore-<utc>)")
		machine := fs.String("machine", "", "restore the disks into this Machine (default: the backed-up Machine); it must be halted")
		storageClass := fs.String("storage-class", "", "StorageClass for restored Atlas volumes")
		_ = fs.Parse(args[2:])
		out, err := createBackupRestore(ctx, kc, *ns, args[1], *name, *machine, *storageClass)
		if err != nil {
			fatal(err)
		}
		okf("machinebackuprestore/%s created", out.Metadata.Name)
		fmt.Println("the disk half waits until the Machine is halted: kaironctl halt MACHINE")
	case "delete":
		if len(args) < 2 {
			fatal(fmt.Errorf("%s", backupUsage))
		}
		_ = fs.Parse(args[2:])
		if err := kc.DeleteMachineBackup(ctx, *ns, args[1]); err != nil {
			fatal(err)
		}
		okf("machinebackup/%s deleted", args[1])
	default:
		fatal(fmt.Errorf("unknown backup action %q\n%s", args[0], backupUsage))
	}
}

func printBackups(items []model.MachineBackup) {
	fmt.Printf("NAME\tMACHINE\tNODE\tPHASE\tSIZE\tQUIESCED\tMESSAGE\n")
	for _, b := range items {
		size, quiesced := "-", "-"
		if b.Status.Disk != nil && b.Status.Disk.Phase == model.BackupSucceeded {
			size = fmt.Sprintf("%dMiB", b.Status.Disk.SizeBytes>>20)
			quiesced = fmt.Sprint(b.Status.Disk.Quiesced)
		}
		fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\t%s\n", b.Metadata.Name, b.Spec.MachineName, dash(b.Status.NodeName), style.Phase(os.Stdout, b.Status.Phase), size, quiesced, dash(b.Status.Message))
	}
}

func createBackup(ctx context.Context, kc *kube.Client, ns, machine, name, quiesce string, atlasSpec *model.MachineBackupAtlas) (model.MachineBackup, error) {
	if !model.ValidBackupQuiesce(quiesce) {
		return model.MachineBackup{}, fmt.Errorf("quiesce must be auto, required or never")
	}
	if name == "" {
		name = resourceName(machine + "-" + time.Now().UTC().Format("20060102-150405"))
	}
	return kc.CreateMachineBackup(ctx, ns, model.MachineBackup{
		TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachineBackup},
		Metadata: model.ObjectMeta{Name: name, Namespace: ns},
		Spec:     model.MachineBackupSpec{MachineName: machine, Quiesce: quiesce, Atlas: atlasSpec},
	})
}

func createBackupRestore(ctx context.Context, kc *kube.Client, ns, backup, name, machine, storageClass string) (model.MachineBackupRestore, error) {
	if name == "" {
		name = resourceName(backup + "-restore-" + time.Now().UTC().Format("20060102-150405"))
	}
	return kc.CreateMachineBackupRestore(ctx, ns, model.MachineBackupRestore{
		TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachineBackupRestore},
		Metadata: model.ObjectMeta{Name: name, Namespace: ns},
		Spec:     model.MachineBackupRestoreSpec{BackupName: backup, MachineName: machine, StorageClassName: storageClass},
	})
}
