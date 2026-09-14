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
	"strconv"
	"strings"
	"time"

	"github.com/zyvorai/kairon/internal/controller"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

// stringSliceFlag collects a repeatable flag (e.g. --ssh-key a --ssh-key b)
// into an ordered slice, since the standard flag package has no built-in
// repeatable-flag type.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// parseForwards parses "hostPort:guestPort[/proto]" specs, matching FluxVM's
// SLIRP hostfwd syntax (protocol defaults to tcp).
func parseForwards(specs []string) ([]model.PortForward, error) {
	var out []model.PortForward
	for _, s := range specs {
		orig := s
		proto := "tcp"
		if idx := strings.LastIndex(s, "/"); idx >= 0 {
			proto = s[idx+1:]
			s = s[:idx]
		}
		hostStr, guestStr, ok := strings.Cut(s, ":")
		if !ok {
			return nil, fmt.Errorf("invalid --forward %q: want hostPort:guestPort[/proto]", orig)
		}
		hostPort, err := strconv.ParseUint(hostStr, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("invalid --forward %q: bad host port: %w", orig, err)
		}
		guestPort, err := strconv.ParseUint(guestStr, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("invalid --forward %q: bad guest port: %w", orig, err)
		}
		out = append(out, model.PortForward{HostPort: uint16(hostPort), GuestPort: uint16(guestPort), Protocol: proto})
	}
	return out, nil
}

var version = "dev"

func main() {
	os.Exit(run())
}

// run returns the process exit code rather than calling os.Exit directly,
// so every deferred cleanup (e.g. cancel()) actually runs before exit.
func run() int {
	if len(os.Args) < 2 {
		usage()
		return 2
	}
	// Metadata commands must work on a developer laptop without kubeconfig or
	// in-cluster credentials.
	if os.Args[1] == "version" {
		fmt.Println(version)
		return 0
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
	case "recover":
		cmdRecover(ctx, kc, os.Args[2:])
	case "snapshot":
		cmdSnapshot(ctx, kc, os.Args[2:])
	case "restore":
		cmdRestore(ctx, kc, os.Args[2:])
	default:
		usage()
		return 2
	}
	return 0
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
	case "restore", "restores", "machinesnapshotrestores":
		items, err := kc.ListMachineSnapshotRestoresNamespace(ctx, ns)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("NAME\tSNAPSHOT\tCLAIM\tPHASE\n")
		for _, r := range items {
			fmt.Printf("%s\t%s\t%s\t%s\n", r.Metadata.Name, r.Spec.SnapshotName, dash(r.Status.RestoredClaimName), dash(r.Status.Phase))
		}
	case "quota", "quotas", "machinequotas":
		items, err := kc.ListMachineQuotasNamespace(ctx, ns)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("NAME\tMAXMACHINES\tMAXCPU\tMAXMEMORY\tUSEDMACHINES\tUSEDCPU\tUSEDMEMORYMIB\n")
		for _, q := range items {
			maxMachines := "-"
			if q.Spec.MaxMachines != nil {
				maxMachines = strconv.Itoa(*q.Spec.MaxMachines)
			}
			fmt.Printf("%s\t%s\t%s\t%s\t%d\t%d\t%d\n", q.Metadata.Name, maxMachines, dash(q.Spec.MaxTotalCPU), dash(q.Spec.MaxTotalMemory), q.Status.UsedMachines, q.Status.UsedTotalCPUCores, q.Status.UsedTotalMemoryMiB)
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
	var forwards stringSliceFlag
	fs.Var(&forwards, "forward", "host port forward hostPort:guestPort[/proto], e.g. 2222:22 (repeatable; mode=user only)")
	hostname := fs.String("hostname", "", "guest hostname to set via cloud-init")
	guestUser := fs.String("user", "", "guest username to configure via cloud-init")
	var sshKeys stringSliceFlag
	fs.Var(&sshKeys, "ssh-key", "SSH public key to authorize in the guest via cloud-init (repeatable)")
	var packages stringSliceFlag
	fs.Var(&packages, "package", "package to install via cloud-init at first boot (repeatable)")
	var runcmd stringSliceFlag
	fs.Var(&runcmd, "runcmd", "shell command to run via cloud-init at first boot (repeatable)")
	_ = fs.Parse(args[1:])
	if *image == "" {
		fatal(fmt.Errorf("--image PATH is required"))
	}
	pf, err := parseForwards(forwards)
	if err != nil {
		fatal(err)
	}
	m := model.Machine{
		TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachine},
		Metadata: model.ObjectMeta{Name: name, Namespace: *ns},
		Spec: model.MachineSpec{
			Image:     model.ImageSpec{Path: *image},
			Resources: model.ResourceSpec{CPU: *cpu, Memory: *memory},
			Runtime:   model.RuntimeSpec{Backend: *backend},
			Network:   model.NetworkSpec{Mode: *network, NetNS: *netns, Forwards: pf},
			CloudInit: model.CloudInitSpec{
				Hostname:          *hostname,
				User:              *guestUser,
				SSHAuthorizedKeys: sshKeys,
				Packages:          packages,
				RunCmd:            runcmd,
			},
			PowerState: "Running",
		},
	}
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
	migrations, err := kc.ListMachineMigrations(ctx)
	if err != nil && !kube.IsNotFound(err) {
		fatal(err)
	}
	budgets, err := kc.ListMachineDisruptionBudgets(ctx)
	if err != nil && !kube.IsNotFound(err) {
		fatal(err)
	}
	states, err := controller.LoadBudgetStates(budgets, machines, migrations)
	if err != nil {
		fatal(err)
	}

	created, skipped := 0, 0
	stamp := time.Now().UTC().Format("20060102-150405")
	for _, machine := range machines {
		if machine.Spec.NodeName != node || machine.Metadata.DeletionTimestamp != nil {
			continue
		}
		if blocker := controller.AdmitDisruption(states, machine); blocker != "" {
			fmt.Printf("machine %s/%s skipped: %s\n", machine.Namespace(), machine.Metadata.Name, blocker)
			skipped++
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
	fmt.Printf("evacuation queued: %d machine(s) from %s", created, node)
	if skipped > 0 {
		fmt.Printf(", %d skipped by MachineDisruptionBudget\n", skipped)
		os.Exit(1)
	}
	fmt.Println()
}

// cmdRecover is a thin convenience layer over spec.recovery, not a second
// source of truth: it prints the migration's current status.Recovery
// diagnosis (so the operator sees ground truth before acting), then patches
// spec.recovery -- internal/agent's reconcileNeedsRecovery is what actually
// validates and applies it.
func cmdRecover(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl recover MIGRATION --action ACTION --diagnosis DIAGNOSIS --reason REASON\n" +
			"  ACTION: ConfirmDestinationCommitted | ConfirmDestinationNotCommitted | ForceAbort\n" +
			"  DIAGNOSIS: DestinationCommitted | DestinationNotCommitted | Unknown"))
	}
	name := args[0]
	fs := flag.NewFlagSet("recover", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	action := fs.String("action", "", "recovery action (required)")
	diagnosis := fs.String("diagnosis", "", "what you observed, attested (required)")
	reason := fs.String("reason", "", "evidence for this decision (required)")
	_ = fs.Parse(args[1:])

	current, err := kc.GetMachineMigration(ctx, *ns, name)
	if err != nil {
		fatal(fmt.Errorf("get machinemigration %s/%s: %w", *ns, name, err))
	}
	if current.Status.Phase != "NeedsRecovery" {
		fatal(fmt.Errorf("machinemigration %s/%s is in phase %q, not NeedsRecovery", *ns, name, current.Status.Phase))
	}
	fmt.Println("current diagnosis:")
	if r := current.Status.Recovery; r != nil {
		fmt.Printf("  source runtime status:       %s\n", dash(r.SourceRuntimeStatus))
		fmt.Printf("  destination session phase:   %s\n", dash(r.DestinationSessionPhase))
		fmt.Printf("  destination runtime found:   %v\n", r.DestinationRuntimeFound)
		fmt.Printf("  destination runtime status:  %s\n", dash(r.DestinationRuntimeStatus))
		if r.DiagnosedAt != nil {
			fmt.Printf("  diagnosed at:                %s\n", r.DiagnosedAt.Format(time.RFC3339))
		}
	} else {
		fmt.Println("  (none yet -- wait for the source agent's next reconcile tick)")
	}

	if *action == "" || *diagnosis == "" || *reason == "" {
		fatal(fmt.Errorf("--action, --diagnosis and --reason are all required"))
	}
	patch := map[string]any{
		"spec": map[string]any{
			"recovery": model.MachineMigrationRecoverySpec{
				Action:                *action,
				AcknowledgedDiagnosis: *diagnosis,
				Reason:                *reason,
			},
		},
	}
	if err := kc.PatchMachineMigration(ctx, *ns, name, patch); err != nil {
		fatal(fmt.Errorf("patch machinemigration %s/%s: %w", *ns, name, err))
	}
	fmt.Printf("machinemigration/%s: recovery %s requested; the source node's agent will validate and apply it on its next reconcile\n", name, *action)
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
	fmt.Printf("machinesnapshotrestore/%s created\n", out.Metadata.Name)
	fmt.Printf("once Succeeded, point a new Machine's spec.volumes[0].claimName at %q\n", *targetClaim)
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
	fmt.Fprintln(os.Stderr, "kaironctl get [machines|migrations|snapshots|restores|quotas] | describe | create | delete | start | stop | migrate | evacuate | recover | snapshot | restore | version")
}
func fatal(err error) { fmt.Fprintln(os.Stderr, "error:", err); os.Exit(1) }
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
