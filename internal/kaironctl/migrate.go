// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kaironctl

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

func cmdMigrate(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl migrate MACHINE [--strategy auto|live|cold] [flags]"))
	}
	machine := args[0]
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	name := fs.String("name", "", "MachineMigration name")
	strategy := fs.String("strategy", "auto", "auto|live|cold")
	target := fs.String("target-node", "", "target Kubernetes node; empty lets the scheduler choose")
	mode := fs.String("mode", "pre-copy", "pre-copy|post-copy")
	bandwidth := fs.Uint64("bandwidth-mbps", 0, "migration adapter bandwidth limit")
	downtime := fs.Uint64("max-downtime-ms", 0, "maximum requested downtime")
	multifd := fs.Uint("multifd-channels", 0, "QEMU multifd channels (0 disables explicit setting)")
	migrationNetwork := fs.String("migration-network", "", "migration network name configured on the target node's adapter (-migration-network name=ip); empty uses the adapter's default advertise address")
	_ = fs.Parse(args[1:])
	if *multifd > 255 {
		fatal(fmt.Errorf("--multifd-channels must be <= 255"))
	}
	if *name == "" {
		*name = resourceName("migration-" + machine + "-" + time.Now().UTC().Format("20060102-150405"))
	}
	migration := model.MachineMigration{
		TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachineMigration},
		Metadata: model.ObjectMeta{Name: *name, Namespace: *ns},
		Spec:     model.MachineMigrationSpec{MachineName: machine, Strategy: *strategy, TargetNode: *target, Mode: *mode, BandwidthMbps: *bandwidth, MaxDowntimeMs: *downtime, MultifdChannels: uint8(*multifd), MigrationNetwork: *migrationNetwork},
	}
	out, err := kc.CreateMachineMigration(ctx, *ns, migration)
	if err != nil {
		fatal(err)
	}
	okf("machinemigration/%s created", out.Metadata.Name)
}

// migrationStillPending reports whether a MachineMigration has NOT yet
// reached one of its real terminal phases (Succeeded/Failed/Blocked/
// Cancelled) -- deliberately different from internal/controller/disruption.go's
// own isTerminalMigrationPhase, which treats "" as terminal too, for that
// function's narrower "does this count toward a budget's currentHealthy"
// purpose. Here "" means "just created, not yet reconciled by
// kairon-controller" -- exactly the case evacuatePass must NOT treat as
// safe to recreate. Found the hard way against a real cluster: running
// evacuate twice in quick succession (within one reconcile interval, before
// kairon-controller had advanced the first migration's phase past "")
// created a real duplicate MachineMigration for the same Machine.
func migrationStillPending(phase string) bool {
	switch phase {
	case "Succeeded", "Failed", "Blocked", "Cancelled":
		return false
	}
	return true
}
