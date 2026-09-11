// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	// Metadata commands must work on a developer laptop without kubeconfig or
	// in-cluster credentials.
	if os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	kc, err := kube.FromEnvironment()
	if err != nil {
		fatal(err)
	}
	switch os.Args[1] {
	case "get":
		cmdGet(ctx, kc, os.Args[2:])
	case "describe":
		cmdDescribe(ctx, kc, os.Args[2:])
	case "create":
		cmdCreate(ctx, kc, os.Args[2:])
	case "delete":
		cmdDelete(ctx, kc, os.Args[2:])
	case "start":
		cmdPower(ctx, kc, os.Args[2:], "Running")
	case "stop":
		cmdPower(ctx, kc, os.Args[2:], "Stopped")
	case "migrate":
		cmdMigrate(ctx, kc, os.Args[2:])
	case "evacuate":
		cmdEvacuate(ctx, kc, os.Args[2:])
	case "snapshot":
		cmdSnapshot(ctx, kc, os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func nsFlag(args []string) (string, []string) {
	ns := "default"
	var out []string
	for i := 0; i < len(args); i++ {
		if (args[i] == "-n" || args[i] == "--namespace") && i+1 < len(args) {
			ns = args[i+1]
			i++
			continue
		}
		out = append(out, args[i])
	}
	return ns, out
}

func cmdGet(ctx context.Context, kc *kube.Client, args []string) {
	ns, args := nsFlag(args)
	resource := "machines"
	if len(args) > 0 {
		resource = strings.ToLower(args[0])
	}
	switch resource {
	case "machine", "machines", "vm", "vms":
		items, err := kc.ListMachinesNamespace(ctx, ns)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("NAME\tNODE\tPHASE\tCPU\tMEMORY\tIP\n")
		for _, m := range items {
			fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n", m.Metadata.Name, dash(m.Spec.NodeName), dash(m.Status.Phase), m.Spec.Resources.CPU, m.Spec.Resources.Memory, dash(m.Status.GuestIP))
		}
	case "migration", "migrations", "machinemigrations":
		items, err := kc.ListMachineMigrationsNamespace(ctx, ns)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("NAME\tMACHINE\tSTRATEGY\tSOURCE\tTARGET\tPHASE\n")
		for _, m := range items {
			fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n", m.Metadata.Name, m.Spec.MachineName, dash(m.Status.EffectiveStrategy), dash(m.Status.SourceNode), dash(m.Status.TargetNode), dash(m.Status.Phase))
		}
	case "snapshot", "snapshots", "machinesnapshots":
		items, err := kc.ListMachineSnapshotsNamespace(ctx, ns)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("NAME\tMACHINE\tPHASE\tREADY\n")
		for _, s := range items {
			fmt.Printf("%s\t%s\t%s\t%t\n", s.Metadata.Name, s.Spec.MachineName, dash(s.Status.Phase), s.Status.ReadyToUse)
		}
	default:
		fatal(fmt.Errorf("unknown resource %q", resource))
	}
}

func cmdDescribe(ctx context.Context, kc *kube.Client, args []string) {
	ns, args := nsFlag(args)
	if len(args) != 1 {
		fatal(fmt.Errorf("describe requires NAME"))
	}
	m, err := kc.GetMachine(ctx, ns, args[0])
	if err != nil {
		fatal(err)
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	fmt.Println(string(b))
}

func cmdCreate(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl create NAME --image PATH [flags]"))
	}
	name := args[0]
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	image := fs.String("image", "", "FluxVM host-local image path")
	cpu := fs.String("cpu", "2", "vCPU quantity")
	memory := fs.String("memory", "2Gi", "memory quantity")
	backend := fs.String("backend", "qemu", "qemu|cloud-hypervisor|firecracker|flux-vm|auto")
	network := fs.String("network", "user", "user|tap|macvtap")
	netns := fs.Bool("netns", false, "use per-VM network namespace for TAP")
	_ = fs.Parse(args[1:])
	if *image == "" {
		fatal(fmt.Errorf("--image PATH is required"))
	}
	m := model.Machine{TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachine}, Metadata: model.ObjectMeta{Name: name, Namespace: *ns}, Spec: model.MachineSpec{Image: model.ImageSpec{Path: *image}, Resources: model.ResourceSpec{CPU: *cpu, Memory: *memory}, Runtime: model.RuntimeSpec{Backend: *backend}, Network: model.NetworkSpec{Mode: *network, NetNS: *netns}, PowerState: "Running"}}
	out, err := kc.CreateMachine(ctx, *ns, m)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("machine/%s created\n", out.Metadata.Name)
}

func cmdDelete(ctx context.Context, kc *kube.Client, args []string) {
	ns, args := nsFlag(args)
	if len(args) != 1 {
		fatal(fmt.Errorf("delete requires NAME"))
	}
	if err := kc.DeleteMachine(ctx, ns, args[0]); err != nil {
		fatal(err)
	}
	fmt.Printf("machine/%s deleted\n", args[0])
}

func cmdPower(ctx context.Context, kc *kube.Client, args []string, state string) {
	ns, args := nsFlag(args)
	if len(args) != 1 {
		fatal(fmt.Errorf("command requires NAME"))
	}
	if err := kc.PatchMachine(ctx, ns, args[0], map[string]any{"spec": map[string]any{"powerState": state}}); err != nil {
		fatal(err)
	}
	fmt.Printf("machine/%s -> %s\n", args[0], state)
}

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
	fmt.Printf("machinemigration/%s created\n", out.Metadata.Name)
}

func cmdEvacuate(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl evacuate NODE [--strategy cold|auto]"))
	}
	node := args[0]
	fs := flag.NewFlagSet("evacuate", flag.ExitOnError)
	strategy := fs.String("strategy", "cold", "cold|auto; use migrate --strategy live for the secure peer handshake")
	_ = fs.Parse(args[1:])
	if *strategy != "cold" && *strategy != "auto" {
		fatal(fmt.Errorf("evacuate supports --strategy cold|auto; use migrate for explicit live migration"))
	}
	machines, err := kc.ListMachines(ctx)
	if err != nil {
		fatal(err)
	}
	created := 0
	stamp := time.Now().UTC().Format("20060102-150405")
	for _, machine := range machines {
		if machine.Spec.NodeName != node || machine.Metadata.DeletionTimestamp != nil {
			continue
		}
		name := resourceName("evacuate-" + node + "-" + machine.Metadata.Name + "-" + stamp)
		migration := model.MachineMigration{
			TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachineMigration},
			Metadata: model.ObjectMeta{Name: name, Namespace: machine.Namespace()},
			Spec:     model.MachineMigrationSpec{MachineName: machine.Metadata.Name, Strategy: *strategy},
		}
		if _, err := kc.CreateMachineMigration(ctx, machine.Namespace(), migration); err != nil {
			fatal(fmt.Errorf("create migration for %s/%s: %w", machine.Namespace(), machine.Metadata.Name, err))
		}
		fmt.Printf("machinemigration/%s created for %s/%s\n", name, machine.Namespace(), machine.Metadata.Name)
		created++
	}
	fmt.Printf("evacuation queued: %d machine(s) from %s\n", created, node)
}

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
	fmt.Printf("machinesnapshot/%s created\n", out.Metadata.Name)
}

var invalidResourceName = regexp.MustCompile(`[^a-z0-9-]+`)

func resourceName(s string) string {
	s = strings.ToLower(s)
	s = invalidResourceName.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 63 {
		s = strings.TrimRight(s[:63], "-")
	}
	return s
}

func usage() {
	fmt.Fprintln(os.Stderr, "kaironctl get [machines|migrations|snapshots] | describe | create | delete | start | stop | migrate | evacuate | snapshot | version")
}
func fatal(err error) { fmt.Fprintln(os.Stderr, "error:", err); os.Exit(1) }
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
