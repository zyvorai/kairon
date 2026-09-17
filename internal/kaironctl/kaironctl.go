// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package kaironctl is kaironctl's own command dispatch and every
// subcommand implementation, factored out of cmd/kaironctl so
// cmd/kubectl-kairon can be a second, real binary sharing the exact same
// logic (a kubectl plugin -- `kubectl kairon ARGS` invokes
// `kubectl-kairon ARGS`, so Run's own args[0]-is-the-subcommand
// convention already matches kubectl's own plugin argument-passing
// exactly, no translation needed) rather than a shim that shells out to a
// separately-installed kaironctl binary.
package kaironctl

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zyvorai/kairon/internal/controller"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
	"github.com/zyvorai/kairon/internal/nodeliveness"
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

// parseKeyValues parses a repeated "key=value" flag (e.g. --label/
// --selector) into a map -- the map-flag counterpart to parseForwards's
// "hostPort:guestPort" splitting below. Returns a nil map for an empty
// input, matching how an unset map-shaped JSON field already behaves.
func parseKeyValues(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid key=value %q: want key=value", p)
		}
		out[k] = v
	}
	return out, nil
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

// Run dispatches one kaironctl invocation and returns the process exit
// code -- args is the command line without the program name itself
// (os.Args[1:]), the same convention kubectl hands a plugin binary its
// own os.Args[1:], so cmd/kaironctl and cmd/kubectl-kairon can both call
// this identically. version is printed by the "version" subcommand,
// injected by each cmd/* package's own main.go via -ldflags -X (kept
// there, not here, so the existing per-binary `-X main.version=...`
// build convention in Makefile/Dockerfile needs no change).
func Run(args []string, version string) int {
	if len(args) < 1 {
		usage()
		return 2
	}
	// Metadata commands must work on a developer laptop without kubeconfig or
	// in-cluster credentials.
	if args[0] == "version" {
		fmt.Println(version)
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	kc, err := kube.FromEnvironment()
	if err != nil {
		fatal(err)
	}
	switch args[0] {
	case "get":
		cmdGet(ctx, kc, args[1:])
	case "describe":
		cmdDescribe(ctx, kc, args[1:])
	case "create":
		cmdCreate(ctx, kc, args[1:])
	case "delete":
		cmdDelete(ctx, kc, args[1:])
	case "start":
		cmdPower(ctx, kc, args[1:], "Running")
	case "stop":
		cmdPower(ctx, kc, args[1:], "Stopped")
	case "pause":
		cmdPower(ctx, kc, args[1:], "Paused")
	case "resume":
		cmdPower(ctx, kc, args[1:], "Running")
	case "halt":
		cmdPower(ctx, kc, args[1:], "Halted")
	case "migrate":
		cmdMigrate(ctx, kc, args[1:])
	case "evacuate":
		cmdEvacuate(ctx, kc, args[1:])
	case "recover":
		cmdRecover(ctx, kc, args[1:])
	case "cancel-migration":
		cmdCancelMigration(ctx, kc, args[1:])
	case "fence":
		cmdFence(ctx, kc, args[1:])
	case "snapshot":
		cmdSnapshot(ctx, kc, args[1:])
	case "restore":
		cmdRestore(ctx, kc, args[1:])
	case "scale":
		cmdScale(ctx, kc, args[1:])
	case "edit":
		cmdEdit(ctx, kc, args[1:])
	case "trigger":
		cmdTrigger(ctx, kc, args[1:])
	case "top":
		cmdTop(ctx, kc, args[1:])
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

// selectorFilter narrows items to those whose labels (as returned by the
// caller-supplied accessor) satisfy every key=value pair in selector, via
// model.LabelsMatch -- the exact same "all pairs must match, an empty
// selector matches nothing" rule `kaironctl delete RESOURCE --selector`
// already applies for bulk delete, now shared by `kaironctl get`'s
// read-side equivalent. A generic helper rather than one filter function
// per kind (as cmdGet has a dozen of), since every kind's only difference
// here is how to reach its model.ObjectMeta.Labels -- Go generics can't
// express "any struct with a Metadata field" structurally, so the accessor
// closure stands in for that. An empty selector is a no-op (returns items
// unchanged) rather than the empty-matches-nothing rule below it, since
// unlike delete's --selector, get's is optional and its absence must keep
// meaning "list everything", exactly as it always has.
func selectorFilter[T any](items []T, selector map[string]string, labels func(T) map[string]string) []T {
	if len(selector) == 0 {
		return items
	}
	out := items[:0:0] // fresh backing array: never alias the caller's slice
	for _, it := range items {
		if model.LabelsMatch(labels(it), selector) {
			out = append(out, it)
		}
	}
	return out
}

// cmdGet lists every resource of one kind (default "machine", same as
// describe/delete's single-object form), optionally narrowed to those
// whose labels satisfy --selector k=v (repeatable, logical AND) -- the
// read-side counterpart to `kaironctl delete RESOURCE --selector` above,
// so an operator can preview exactly which objects a selector reaches (or
// just list, say, every Machine from one load test) without reaching for
// `kubectl get -l` or piping through grep.
func cmdGet(ctx context.Context, kc *kube.Client, args []string) {
	ns, args := nsFlag(args)
	resource := "machines"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		resource = strings.ToLower(args[0])
		args = args[1:]
	}
	fs := flag.NewFlagSet("get", flag.ExitOnError)
	var selectorFlag stringSliceFlag
	fs.Var(&selectorFlag, "selector", "label key=value every listed resource must carry (repeatable -- every pair must match); omitted lists every resource of this kind, exactly as before this flag existed")
	_ = fs.Parse(args)
	selector, err := parseKeyValues(selectorFlag)
	if err != nil {
		fatal(err)
	}
	switch resource {
	case "machine", "machines", "vm", "vms":
		items, err := kc.ListMachinesNamespace(ctx, ns)
		if err != nil {
			fatal(err)
		}
		items = selectorFilter(items, selector, func(m model.Machine) map[string]string { return m.Metadata.Labels })
		fmt.Printf("NAME\tNODE\tPHASE\tCPU\tMEMORY\tIP\n")
		for _, m := range items {
			fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n", m.Metadata.Name, dash(m.Spec.NodeName), dash(m.Status.Phase), m.Spec.Resources.CPU, m.Spec.Resources.Memory, dash(m.Status.GuestIP))
		}
	case "migration", "migrations", "machinemigrations":
		items, err := kc.ListMachineMigrationsNamespace(ctx, ns)
		if err != nil {
			fatal(err)
		}
		items = selectorFilter(items, selector, func(m model.MachineMigration) map[string]string { return m.Metadata.Labels })
		fmt.Printf("NAME\tMACHINE\tSTRATEGY\tSOURCE\tTARGET\tPHASE\n")
		for _, m := range items {
			fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n", m.Metadata.Name, m.Spec.MachineName, dash(m.Status.EffectiveStrategy), dash(m.Status.SourceNode), dash(m.Status.TargetNode), dash(m.Status.Phase))
		}
	case "snapshot", "snapshots", "machinesnapshots":
		items, err := kc.ListMachineSnapshotsNamespace(ctx, ns)
		if err != nil {
			fatal(err)
		}
		items = selectorFilter(items, selector, func(s model.MachineSnapshot) map[string]string { return s.Metadata.Labels })
		fmt.Printf("NAME\tMACHINE\tPHASE\tREADY\n")
		for _, s := range items {
			fmt.Printf("%s\t%s\t%s\t%t\n", s.Metadata.Name, s.Spec.MachineName, dash(s.Status.Phase), s.Status.ReadyToUse)
		}
	case "restore", "restores", "machinesnapshotrestores":
		items, err := kc.ListMachineSnapshotRestoresNamespace(ctx, ns)
		if err != nil {
			fatal(err)
		}
		items = selectorFilter(items, selector, func(r model.MachineSnapshotRestore) map[string]string { return r.Metadata.Labels })
		fmt.Printf("NAME\tSNAPSHOT\tCLAIM\tPHASE\n")
		for _, r := range items {
			fmt.Printf("%s\t%s\t%s\t%s\n", r.Metadata.Name, r.Spec.SnapshotName, dash(r.Status.RestoredClaimName), dash(r.Status.Phase))
		}
	case "quota", "quotas", "machinequotas":
		items, err := kc.ListMachineQuotasNamespace(ctx, ns)
		if err != nil {
			fatal(err)
		}
		items = selectorFilter(items, selector, func(q model.MachineQuota) map[string]string { return q.Metadata.Labels })
		fmt.Printf("NAME\tMAXMACHINES\tMAXCPU\tMAXMEMORY\tUSEDMACHINES\tUSEDCPU\tUSEDMEMORYMIB\n")
		for _, q := range items {
			maxMachines := "-"
			if q.Spec.MaxMachines != nil {
				maxMachines = strconv.Itoa(*q.Spec.MaxMachines)
			}
			fmt.Printf("%s\t%s\t%s\t%s\t%d\t%d\t%d\n", q.Metadata.Name, maxMachines, dash(q.Spec.MaxTotalCPU), dash(q.Spec.MaxTotalMemory), q.Status.UsedMachines, q.Status.UsedTotalCPUCores, q.Status.UsedTotalMemoryMiB)
		}
	case "budget", "budgets", "machinedisruptionbudgets":
		items, err := kc.ListMachineDisruptionBudgetsNamespace(ctx, ns)
		if err != nil {
			fatal(err)
		}
		items = selectorFilter(items, selector, func(b model.MachineDisruptionBudget) map[string]string { return b.Metadata.Labels })
		fmt.Printf("NAME\tMINAVAILABLE\tMAXUNAVAILABLE\tEXPECTED\tHEALTHY\tDESIRED\tALLOWED\n")
		for _, b := range items {
			fmt.Printf("%s\t%s\t%s\t%d\t%d\t%d\t%d\n", b.Metadata.Name, dash(b.Spec.MinAvailable), dash(b.Spec.MaxUnavailable), b.Status.ExpectedMachines, b.Status.CurrentHealthy, b.Status.DesiredHealthy, b.Status.DisruptionsAllowed)
		}
	case "machineset", "machinesets":
		items, err := kc.ListMachineSetsNamespace(ctx, ns)
		if err != nil {
			fatal(err)
		}
		items = selectorFilter(items, selector, func(s model.MachineSet) map[string]string { return s.Metadata.Labels })
		fmt.Printf("NAME\tSTRATEGY\tREPLICAS\tREADY\tUPDATED\n")
		for _, s := range items {
			fmt.Printf("%s\t%s\t%d\t%d\t%d\n", s.Metadata.Name, dash(defaultStrategy(s.Spec.Strategy)), s.Spec.Replicas, s.Status.ReadyReplicas, s.Status.UpdatedReplicas)
		}
	case "instancetype", "instancetypes", "machineinstancetypes":
		items, err := kc.ListMachineInstanceTypesNamespace(ctx, ns)
		if err != nil {
			fatal(err)
		}
		items = selectorFilter(items, selector, func(it model.MachineInstanceType) map[string]string { return it.Metadata.Labels })
		fmt.Printf("NAME\tCPU\tMEMORY\n")
		for _, it := range items {
			fmt.Printf("%s\t%s\t%s\n", it.Metadata.Name, dash(it.Spec.Resources.CPU), dash(it.Spec.Resources.Memory))
		}
	case "migrationpolicy", "migrationpolicies":
		items, err := kc.ListMigrationPoliciesNamespace(ctx, ns)
		if err != nil {
			fatal(err)
		}
		items = selectorFilter(items, selector, func(p model.MigrationPolicy) map[string]string { return p.Metadata.Labels })
		fmt.Printf("NAME\tBANDWIDTHMBPS\tACTIVEMIGRATIONS\n")
		for _, p := range items {
			bw := "-"
			if p.Spec.BandwidthMbps != 0 {
				bw = strconv.FormatUint(p.Spec.BandwidthMbps, 10)
			}
			fmt.Printf("%s\t%s\t%d\n", p.Metadata.Name, bw, p.Status.ActiveMigrations)
		}
	case "snapshotschedule", "snapshotschedules", "machinesnapshotschedules":
		items, err := kc.ListMachineSnapshotSchedulesNamespace(ctx, ns)
		if err != nil {
			fatal(err)
		}
		items = selectorFilter(items, selector, func(s model.MachineSnapshotSchedule) map[string]string { return s.Metadata.Labels })
		fmt.Printf("NAME\tINTERVALSECONDS\tSTARTINGDEADLINE\tSUSPEND\tLASTRUN\tLASTCOUNT\tNEXTRUN\n")
		for _, s := range items {
			lastRun := "-"
			if !s.Status.LastRunTime.IsZero() {
				lastRun = s.Status.LastRunTime.Format(time.RFC3339)
			}
			deadline := "-"
			if s.Spec.StartingDeadlineSeconds > 0 {
				deadline = strconv.Itoa(s.Spec.StartingDeadlineSeconds)
			}
			fmt.Printf("%s\t%d\t%s\t%t\t%s\t%d\t%s\n", s.Metadata.Name, s.Spec.IntervalSeconds, deadline, s.Spec.Suspend, lastRun, s.Status.LastRunSnapshotCount, formatNextRun(s))
		}
	// networkpolicy/securitygroup: MachineNetworkPolicy and
	// NetworkSecurityGroup drive FluxVM's real eBPF/TC enforcement (see
	// docs/guides/network-policy.md) but, unlike every other kind here,
	// previously had no kaironctl support at all -- an operator debugging
	// "why is my traffic blocked" could reach for the four network-*
	// diagnostic endpoints but couldn't even list the policy objects
	// themselves without kubectl.
	case "networkpolicy", "networkpolicies", "machinenetworkpolicies":
		items, err := kc.ListMachineNetworkPoliciesNamespace(ctx, ns)
		if err != nil {
			fatal(err)
		}
		items = selectorFilter(items, selector, func(p model.MachineNetworkPolicy) map[string]string { return p.Metadata.Labels })
		fmt.Printf("NAME\tTARGET\tDEFAULTALLOW\tPHASE\tSYNCED\n")
		for _, p := range items {
			target := dash(p.Spec.MachineName)
			if target == "-" && len(p.Spec.Selector) > 0 {
				target = fmt.Sprintf("selector(%d)", len(p.Spec.Selector))
			}
			fmt.Printf("%s\t%s\t%t\t%s\t%t\n", p.Metadata.Name, target, p.Spec.Policy.DefaultAllow, dash(p.Status.Phase), p.Status.EffectiveSynced)
		}
	case "securitygroup", "securitygroups", "networksecuritygroups":
		items, err := kc.ListNetworkSecurityGroupsNamespace(ctx, ns)
		if err != nil {
			fatal(err)
		}
		items = selectorFilter(items, selector, func(g model.NetworkSecurityGroup) map[string]string { return g.Metadata.Labels })
		fmt.Printf("NAME\tGROUPNAME\tPRIORITY\tDEFAULTALLOW\tPHASE\tAPPLIEDON\n")
		for _, g := range items {
			groupName := g.Spec.GroupName
			if groupName == "" {
				groupName = g.Metadata.Name
			}
			fmt.Printf("%s\t%s\t%d\t%t\t%s\t%s\n", g.Metadata.Name, groupName, g.Spec.Priority, g.Spec.Policy.DefaultAllow, dash(g.Status.Phase), dash(g.Status.AppliedOn))
		}
	// node/nodes: the real Kubernetes core Node object internal/scheduler
	// itself already reads (spec.taints, spec.unschedulable) to decide
	// where a Machine can land -- see internal/scheduler.go's own
	// toleratesTaint/eligible -- but which previously had no kaironctl
	// support at all, the exact same "no CLI access short of raw kubectl"
	// gap networkpolicy/securitygroup above closed for those two kinds.
	// That gap became sharper the moment Node taints started actually
	// gating scheduling (see 2fd0257): an operator asking "why won't my
	// Machine schedule onto NODE" now has a real reason to look at a
	// node's taints, and had no kaironctl way to do so. Unlike every other
	// case above, NODE is cluster-scoped -- ns (from --namespace/-n) is
	// simply unused here, exactly as it already is for any flag a given
	// kind's underlying API doesn't have.
	case "node", "nodes":
		items, err := kc.ListNodes(ctx)
		if err != nil {
			fatal(err)
		}
		items = selectorFilter(items, selector, func(n model.Node) map[string]string { return n.Metadata.Labels })
		fmt.Printf("NAME\tREADY\tUNSCHEDULABLE\tTAINTS\n")
		for _, n := range items {
			fmt.Printf("%s\t%s\t%t\t%s\n", n.Metadata.Name, nodeReadyStatus(n), n.Spec.Unschedulable, nodeTaintsSummary(n))
		}
	default:
		fatal(fmt.Errorf("unknown resource %q", resource))
	}
}

// cmdTop is kaironctl's kubectl-top-style view of live, cgroup-derived
// resource usage: `kaironctl top machines` prints each Machine's own
// Status.ResourceUsage (populated by internal/agent/agent.go straight from
// FluxVM's GET /v1/vms/{id}/stats -- see ResourceUsage's own doc comment),
// and `kaironctl top nodes` rolls those same per-Machine samples up by
// Spec.NodeName -- the fleet-wide, per-host hotspot view `kaironctl get
// machines` alone can't answer without an operator mentally grouping and
// summing rows themselves. Deliberately reuses exactly the data every
// Machine already reports rather than adding a new metrics pipeline: no new
// endpoint, no new controller-side aggregation, nothing to keep in sync --
// a rendering of state that already exists, the same "already-collected
// data, just not previously surfaced" shape as `kaironctl get nodes`
// itself.
//
// Like cmdGet, --selector k=v (repeatable) narrows which Machines count,
// via the exact same selectorFilter/model.LabelsMatch semantics; an empty
// selector means every Machine, never "none". "top machines" defaults to
// -n/--namespace exactly like "get machines" (cluster-wide only via an
// explicit selector isn't offered here, matching every other namespaced
// verb in this file); "top nodes" is cluster-wide by construction (a
// Machine's Spec.NodeName can span namespaces onto the same physical node),
// so -n/--namespace has no effect there, the same "ns silently unused for a
// cluster-scoped kind" precedent `kaironctl get nodes` already sets.
func cmdTop(ctx context.Context, kc *kube.Client, args []string) {
	ns, args := nsFlag(args)
	resource := "machines"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		resource = strings.ToLower(args[0])
		args = args[1:]
	}
	fs := flag.NewFlagSet("top", flag.ExitOnError)
	var selectorFlag stringSliceFlag
	fs.Var(&selectorFlag, "selector", "label key=value every counted Machine must carry (repeatable -- every pair must match); omitted counts every Machine, exactly as `kaironctl get`'s own --selector does")
	_ = fs.Parse(args)
	selector, err := parseKeyValues(selectorFlag)
	if err != nil {
		fatal(err)
	}
	switch resource {
	case "machine", "machines", "vm", "vms":
		items, err := kc.ListMachinesNamespace(ctx, ns)
		if err != nil {
			fatal(err)
		}
		items = selectorFilter(items, selector, func(m model.Machine) map[string]string { return m.Metadata.Labels })
		sort.Slice(items, func(i, j int) bool { return items[i].Metadata.Name < items[j].Metadata.Name })
		fmt.Printf("NAME\tNODE\tCPU%%\tMEMORY\tDISK-READ\tDISK-WRITE\n")
		for _, m := range items {
			u := m.Status.ResourceUsage
			if u == nil {
				fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n", m.Metadata.Name, dash(m.Spec.NodeName), "-", "-", "-", "-")
				continue
			}
			fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n", m.Metadata.Name, dash(m.Spec.NodeName), formatCPUPercent(u.CPUPercent), formatBytes(u.MemoryBytes), formatBytes(u.DiskReadBytes), formatBytes(u.DiskWriteBytes))
		}
	case "node", "nodes":
		// Cluster-wide by construction -- see this function's own doc
		// comment for why ns is unused here.
		items, err := kc.ListMachines(ctx)
		if err != nil {
			fatal(err)
		}
		items = selectorFilter(items, selector, func(m model.Machine) map[string]string { return m.Metadata.Labels })
		fmt.Printf("NODE\tMACHINES\tCPU%%\tMEMORY\n")
		for _, agg := range aggregateUsageByNode(items) {
			fmt.Printf("%s\t%d\t%s\t%s\n", agg.node, agg.machines, formatCPUPercent(agg.cpuPercent), formatBytes(agg.memoryBytes))
		}
	default:
		fatal(fmt.Errorf("unknown resource %q", resource))
	}
}

// nodeUsageAggregate is one row of `kaironctl top nodes` -- every matching
// Machine's own Status.ResourceUsage summed by the node it's scheduled onto
// (Spec.NodeName). A Machine with no ResourceUsage yet (never reported by
// its agent, or not yet scheduled) still counts toward "machines" -- an
// operator asking "how many Machines are on this node" wants that answer
// regardless of whether usage stats have arrived -- but contributes zero to
// the cpu/memory sums, exactly as if it were using none (the honest
// approximation: "not yet reported" and "using nothing" are indistinguishable
// from a summed total's point of view, and undercounting is the safer
// direction for a hotspot-spotting tool than fabricating a number).
type nodeUsageAggregate struct {
	node        string
	machines    int
	cpuPercent  float64
	memoryBytes uint64
}

// aggregateUsageByNode groups machines by Spec.NodeName (dash-rendered
// "-" for a not-yet-scheduled Machine, exactly matching `kaironctl get
// machines`' own dash(m.Spec.NodeName) rendering, so an unscheduled
// Machine's usage -- if it somehow has any -- is never silently dropped
// nor attributed to a real node) and returns one nodeUsageAggregate per
// distinct node, sorted by node name for deterministic, diffable output
// (map iteration order is otherwise unspecified).
func aggregateUsageByNode(machines []model.Machine) []nodeUsageAggregate {
	byNode := make(map[string]*nodeUsageAggregate)
	var order []string
	for _, m := range machines {
		node := dash(m.Spec.NodeName)
		agg, ok := byNode[node]
		if !ok {
			agg = &nodeUsageAggregate{node: node}
			byNode[node] = agg
			order = append(order, node)
		}
		agg.machines++
		if u := m.Status.ResourceUsage; u != nil {
			agg.cpuPercent += u.CPUPercent
			agg.memoryBytes += u.MemoryBytes
		}
	}
	sort.Strings(order)
	out := make([]nodeUsageAggregate, 0, len(order))
	for _, node := range order {
		out = append(out, *byNode[node])
	}
	return out
}

// formatCPUPercent renders ResourceUsage.CPUPercent to two decimal places
// with a trailing "%" -- kubectl top's own "123m"-style millicore notation
// doesn't apply here (CPUPercent is already a percentage of one core, per
// its own doc comment, not a raw core-second count), so this is simply a
// fixed, readable precision rather than Go's default float formatting
// (which would print a trailing ".000000000001"-style artifact for values
// like agent.go's own averaged-over-lifetime computation can produce).
func formatCPUPercent(p float64) string {
	return strconv.FormatFloat(p, 'f', 2, 64) + "%"
}

// formatBytes renders a byte count the same human-readable way `kaironctl`
// already expects an operator reading a terminal to want (MachineQuota's
// own MaxTotalMemory/MaxTotalMemoryMiB fields are hand-authored strings for
// exactly this reason) -- ResourceUsage's fields are raw uint64 byte
// counts straight off FluxVM's stats endpoint, and printing those
// unformatted would force every reader to mentally divide by 2^30 anyway.
// Binary (1024-based) units, matching Kubernetes' own Mi/Gi convention
// (never SI Mb/Gb) that MachineQuota/MachineInstanceType's own Memory
// fields already use throughout this codebase.
func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// nodeReadyStatus renders a Node's core "Ready" NodeCondition for
// `kaironctl get nodes` -- the same condition kairon-controller's own
// ConditionNodeUnreachable/fencing logic (see cmdFence) ultimately treats
// as the node's reachability signal. "Unknown" covers both a genuinely
// absent Ready condition and one whose Status is neither "True" nor
// "False", since a real kube-apiserver only ever reports those three
// values for it anyway.
func nodeReadyStatus(n model.Node) string {
	for _, c := range n.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status
		}
	}
	return "Unknown"
}

// nodeTaintsSummary renders a Node's spec.taints as a comma-separated
// key[=value]:Effect list ("-" when there are none) for `kaironctl get
// nodes`/`describe node`'s own raw dump -- the same key[=value] shorthand
// internal/scheduler.go's own unexported taintKV renders for its
// excluded-node reason strings, reimplemented here (rather than exported
// from internal/scheduler just for this) since it's a two-line, purely
// cosmetic formatting helper with no shared behavior that could drift.
func nodeTaintsSummary(n model.Node) string {
	if len(n.Spec.Taints) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(n.Spec.Taints))
	for _, t := range n.Spec.Taints {
		kv := t.Key
		if t.Value != "" {
			kv += "=" + t.Value
		}
		parts = append(parts, kv+":"+t.Effect)
	}
	return strings.Join(parts, ",")
}

// defaultStrategy names the MachineSet rollout strategy the reconciler
// itself defaults to when spec.strategy is left empty, so `kaironctl get
// machinesets` never prints a bare "-" for the common case.
func defaultStrategy(strategy string) string {
	if strategy == "" {
		return "RollingUpdate"
	}
	return strategy
}

// formatNextRun renders `kaironctl get snapshotschedules`' NEXTRUN column.
// It deliberately does NOT just print s.Status.NextRunTime verbatim: that
// field is only ever updated when a schedule actually fires (see
// MachineSnapshotScheduleStatus.NextRunTime's own doc comment), so a
// schedule suspended sometime *after* its last run would otherwise show an
// already-passed timestamp as if a run were still pending. Checking the
// live spec.suspend flag here, at display time, means this can never
// happen -- "suspended" always wins over a stale projection.
func formatNextRun(s model.MachineSnapshotSchedule) string {
	if s.Spec.Suspend {
		return "suspended"
	}
	if s.Status.NextRunTime.IsZero() {
		return "pending" // never yet run -- fires on the next reconcile tick
	}
	return s.Status.NextRunTime.Format(time.RFC3339)
}

// resourceKindAndName splits describe/delete's positional args into a
// resource kind and a NAME, recognizing exactly the same resource-type
// aliases cmdGet already does -- reused here so all three verbs treat
// "snapshot"/"machineset"/etc. identically. KIND is always optional and
// disambiguated purely by argument count, never by matching NAME itself
// against the alias list: 1 positional arg is NAME alone (kind defaults
// to "machine", preserving the exact prior calling convention of
// `describe NAME`/`delete NAME`), 2 positional args are KIND NAME. This
// avoids the alternative of guessing from NAME's own text, which would
// silently misfire for the (rare but real) case of a Machine actually
// named "snapshot" or "budget".
func resourceKindAndName(verb string, args []string) (kind, name string) {
	switch len(args) {
	case 1:
		return "machine", args[0]
	case 2:
		return strings.ToLower(args[0]), args[1]
	default:
		fatal(fmt.Errorf("usage: kaironctl %s [RESOURCE] NAME", verb))
		return "", ""
	}
}

func cmdDescribe(ctx context.Context, kc *kube.Client, args []string) {
	ns, args := nsFlag(args)
	kind, name := resourceKindAndName("describe", args)
	switch kind {
	case "snapshotschedule", "snapshotschedules", "machinesnapshotschedules":
		// One of four kinds describe doesn't just raw-JSON-dump -- see
		// describeSnapshotSchedule's own doc comment for why this
		// narrow exception is justified for this specific CRD and isn't
		// a generalized richer-describe change for every kind.
		describeSnapshotSchedule(ctx, kc, ns, name)
		return
	case "migrationpolicy", "migrationpolicies":
		// The second -- see describeMigrationPolicy's own doc comment.
		describeMigrationPolicy(ctx, kc, ns, name)
		return
	case "quota", "quotas", "machinequotas":
		// The third -- see describeQuota's own doc comment.
		describeQuota(ctx, kc, ns, name)
		return
	case "budget", "budgets", "machinedisruptionbudgets":
		// The fourth -- see describeBudget's own doc comment.
		describeBudget(ctx, kc, ns, name)
		return
	}
	var (
		out any
		err error
	)
	switch kind {
	case "machine", "machines", "vm", "vms":
		out, err = kc.GetMachine(ctx, ns, name)
	case "migration", "migrations", "machinemigrations":
		out, err = kc.GetMachineMigration(ctx, ns, name)
	case "snapshot", "snapshots", "machinesnapshots":
		out, err = kc.GetMachineSnapshot(ctx, ns, name)
	case "restore", "restores", "machinesnapshotrestores":
		out, err = kc.GetMachineSnapshotRestore(ctx, ns, name)
	case "quota", "quotas", "machinequotas":
		out, err = kc.GetMachineQuota(ctx, ns, name)
	case "budget", "budgets", "machinedisruptionbudgets":
		out, err = kc.GetMachineDisruptionBudget(ctx, ns, name)
	case "machineset", "machinesets":
		out, err = kc.GetMachineSet(ctx, ns, name)
	case "instancetype", "instancetypes", "machineinstancetypes":
		out, err = kc.GetMachineInstanceType(ctx, ns, name)
	case "migrationpolicy", "migrationpolicies":
		out, err = kc.GetMigrationPolicy(ctx, ns, name)
	case "networkpolicy", "networkpolicies", "machinenetworkpolicies":
		out, err = kc.GetMachineNetworkPolicy(ctx, ns, name)
	case "securitygroup", "securitygroups", "networksecuritygroups":
		out, err = kc.GetNetworkSecurityGroup(ctx, ns, name)
	case "node", "nodes":
		// Cluster-scoped, like `kaironctl get node` above -- ns is unused.
		out, err = kc.GetNode(ctx, name)
	default:
		fatal(fmt.Errorf("unknown resource %q", kind))
		return
	}
	if err != nil {
		fatal(err)
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}

// describeSnapshotSchedule prints a MachineSnapshotSchedule the same
// raw-JSON way every other kind's `describe` does, then appends a
// "Matching machines" preview: exactly which Machines the schedule's own
// spec.selector currently matches in its namespace, and whether the very
// next reconcile tick would actually fire a new round of snapshots for
// them -- evaluated by calling the schedule's own Spec.Due(status.
// lastRunTime, time.Now()) and, if Due, Spec.DeadlineExceeded(status.
// lastRunTime, time.Now()) -- the identical pure functions
// reconcileMachineSnapshotSchedules itself calls every tick, so this
// preview can never drift from what the controller will actually do. A
// schedule that's Due but past its own spec.startingDeadlineSeconds is
// reported as a distinct third outcome ("due, but will be SKIPPED") rather
// than folded into either "due now" or "not due yet" -- it fires no
// snapshots this tick, same as "not due", but for a different, worth-
// surfacing reason.
//
// This is a deliberate, narrow exception to this project's otherwise
// uniform "describe just dumps the raw object as JSON" convention (every
// other kind still does exactly that, unchanged -- describeMigrationPolicy
// below is the only other exception) -- justified because kubectl's own
// `describe` already appends non-raw derived information beyond an
// object's literal fields when it's operationally useful (e.g. related
// Events), and "which Machines would this schedule snapshot right now, and
// would it even fire" is a real question an operator asks before
// loosening/tightening spec.selector or spec.intervalSeconds, not
// something a generalized richer-describe-for-every-kind change would be
// needed for.
func describeSnapshotSchedule(ctx context.Context, kc *kube.Client, ns, name string) {
	sched, err := kc.GetMachineSnapshotSchedule(ctx, ns, name)
	if err != nil {
		fatal(err)
	}
	b, _ := json.MarshalIndent(sched, "", "  ")
	fmt.Println(string(b))

	machines, err := kc.ListMachinesNamespace(ctx, sched.Namespace())
	if err != nil {
		fatal(err)
	}
	var matches []string
	for _, m := range machines {
		if model.LabelsMatch(m.Metadata.Labels, sched.Spec.Selector) {
			matches = append(matches, m.Metadata.Name)
		}
	}
	sort.Strings(matches)

	fmt.Println()
	now := time.Now()
	switch {
	case model.TriggerNowRequested(sched.Metadata.Annotations[model.AnnotationSnapshotScheduleTriggerNow], sched.Status.LastHandledTriggerTime):
		fmt.Printf("Matching machines (%d) -- manual run requested (kaironctl trigger snapshotschedule), the next reconcile tick will snapshot these regardless of spec.suspend or the normal interval:\n", len(matches))
	case sched.Spec.Due(sched.Status.LastRunTime, now) && sched.Spec.DeadlineExceeded(sched.Status.LastRunTime, now):
		fmt.Printf("Matching machines (%d) -- due, but will be SKIPPED: this run is more than startingDeadlineSeconds (%ds) late:\n", len(matches), sched.Spec.StartingDeadlineSeconds)
	case sched.Spec.Due(sched.Status.LastRunTime, now):
		fmt.Printf("Matching machines (%d) -- due now, the next reconcile tick will snapshot these:\n", len(matches))
	default:
		fmt.Printf("Matching machines (%d) -- not due yet (next projected run: %s):\n", len(matches), formatNextRun(sched))
	}
	if len(matches) == 0 {
		fmt.Println("  (none -- check spec.selector against these Machines' own labels)")
		return
	}
	for _, m := range matches {
		fmt.Printf("  %s\n", m)
	}
}

// describeMigrationPolicy is `describe`'s other raw-JSON-plus-preview
// exception (see describeSnapshotSchedule's doc comment just above for why
// this is deliberately narrow, not a generalized richer-describe change):
// after the usual raw-JSON dump, it appends exactly which Machines in the
// policy's own namespace currently match spec.selector, and for each one,
// what creating a migration for it *right now* would actually get from
// this policy -- the same two decisions kairon-controller's own migration
// reconcile loop makes for every migration, computed by calling its own
// exported, pure controller.AdmitMigrationPolicy/
// controller.BandwidthMbpsFromPolicies rather than re-deriving either
// decision, so this preview can never drift from what the controller will
// actually do (identical justification, and identical mechanism, to
// describeSnapshotSchedule calling the schedule's own Spec.Due/
// DeadlineExceeded).
//
// Both decisions depend on state beyond this one policy, which this preview
// surfaces rather than hides: AdmitMigrationPolicy spends a shared
// per-policy MaxConcurrent slot as it's asked about each matching Machine
// in turn -- mirroring a real sequential batch of migration creations, not
// a static "would it fit in isolation" check per Machine -- so Machines are
// walked in the same stable (name-sorted) order every run, and once a
// policy's own cap is exhausted, every remaining match correctly reports
// blocked, exactly like a real batch created in that order would.
// BandwidthMbpsFromPolicies additionally depends on every other
// MigrationPolicy in the namespace, not just this one: when an earlier
// (list-order) policy also matches a Machine and sets its own
// spec.bandwidthMbps, that other policy's value wins over this policy's --
// firstOverlappingBandwidthPolicy below identifies which policy actually
// won by name, so this never misattributes another policy's number as this
// policy's own (a real, silent surprise this preview exists to head off).
func describeMigrationPolicy(ctx context.Context, kc *kube.Client, ns, name string) {
	policy, err := kc.GetMigrationPolicy(ctx, ns, name)
	if err != nil {
		fatal(err)
	}
	b, _ := json.MarshalIndent(policy, "", "  ")
	fmt.Println(string(b))

	machines, err := kc.ListMachinesNamespace(ctx, ns)
	if err != nil {
		fatal(err)
	}
	migrations, err := kc.ListMachineMigrationsNamespace(ctx, ns)
	if err != nil && !kube.IsNotFound(err) {
		fatal(err)
	}
	policies, err := kc.ListMigrationPoliciesNamespace(ctx, ns)
	if err != nil && !kube.IsNotFound(err) {
		fatal(err)
	}
	states := controller.LoadMigrationPolicyStates(policies, machines, migrations)

	var matches []model.Machine
	for _, m := range machines {
		if model.LabelsMatch(m.Metadata.Labels, policy.Spec.Selector) {
			matches = append(matches, m)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Metadata.Name < matches[j].Metadata.Name })

	fmt.Println()
	limit := "unlimited"
	if policy.Spec.MaxConcurrent > 0 {
		limit = fmt.Sprintf("%d", policy.Spec.MaxConcurrent)
	}
	fmt.Printf("Matching machines (%d) -- status.activeMigrations %d/%s; a migration created for each of these right now, in this order, would get:\n", len(matches), policy.Status.ActiveMigrations, limit)
	if len(matches) == 0 {
		fmt.Println("  (none -- check spec.selector against these Machines' own labels)")
		return
	}
	for _, m := range matches {
		verdict := "admitted"
		if blocker := controller.AdmitMigrationPolicy(states, m); blocker != "" {
			verdict = "BLOCKED (" + blocker + ")"
		}
		bwLine := "no bandwidth override"
		if bw := controller.BandwidthMbpsFromPolicies(states, m); bw > 0 {
			if winner := firstOverlappingBandwidthPolicy(policies, m); winner == policy.Metadata.Name {
				bwLine = fmt.Sprintf("%d Mbps (this policy)", bw)
			} else {
				bwLine = fmt.Sprintf("%d Mbps (from %q, an earlier-matching MigrationPolicy, not this one)", bw, winner)
			}
		}
		fmt.Printf("  %-24s %-40s %s\n", m.Metadata.Name, verdict, bwLine)
	}
}

// firstOverlappingBandwidthPolicy names the first (list-order) policy that
// both matches m's labels and sets a nonzero spec.bandwidthMbps -- the same
// selection controller.BandwidthMbpsFromPolicies makes internally to pick a
// *value*, walked here only to attach a human-readable name to that value
// for describeMigrationPolicy's output, never to make or duplicate the
// admission/bandwidth decision itself.
func firstOverlappingBandwidthPolicy(policies []model.MigrationPolicy, m model.Machine) string {
	for _, p := range policies {
		if model.LabelsMatch(m.Metadata.Labels, p.Spec.Selector) && p.Spec.BandwidthMbps > 0 {
			return p.Metadata.Name
		}
	}
	return ""
}

// describeQuota is describe's third raw-JSON-plus-preview exception (see
// describeSnapshotSchedule's doc comment above for why these are
// deliberately narrow, not a generalized richer-describe change). A
// MachineQuota's raw JSON already contains both spec.maxTotalCpu/
// maxTotalMemory (human strings like "8Gi") and status.usedTotalCpuCores/
// usedTotalMemoryMiB (normalized numbers, written by kairon-controller's
// own reconcileQuotasStatus-equivalent tally every tick) -- but they sit in
// two disjoint top-level objects, in two different unit systems, so reading
// "how close is this namespace to its cap" out of the raw dump means
// mentally converting a Gi string to MiB and cross-referencing it against a
// separate field by eye. This preview does that conversion once, lines used
// up against limit in matching units the way `kubectl describe
// resourcequota` lines up its own Used/Hard columns, and additionally lists
// exactly which Machines are counted -- the same "don't just report a
// number, show the receipts" precedent describeSnapshotSchedule's own
// "Matching machines" list and describeMigrationPolicy's per-Machine
// breakdown already established.
//
// Usage is recomputed fresh from the namespace's current Machines via
// controller.MachineCountsTowardQuota/MachineFootprint -- the exact
// predicate and footprint calculation kairon-controller's own scheduling
// loop uses to admit or block a new Machine (see internal/controller/
// quota.go) -- rather than trusted from status, which is only ever as
// fresh as the last reconcile tick's patch. That also means this preview's
// totals can very occasionally read a few Machines ahead of status.used* if
// run between a Machine's admission and the next tick's status patch; that
// is a real, honest gap (not a bug to paper over), so the header below says
// "recomputed fresh" rather than implying it's reading status verbatim.
func describeQuota(ctx context.Context, kc *kube.Client, ns, name string) {
	quota, err := kc.GetMachineQuota(ctx, ns, name)
	if err != nil {
		fatal(err)
	}
	b, _ := json.MarshalIndent(quota, "", "  ")
	fmt.Println(string(b))

	machines, err := kc.ListMachinesNamespace(ctx, ns)
	if err != nil {
		fatal(err)
	}
	var counted []model.Machine
	for _, m := range machines {
		if controller.MachineCountsTowardQuota(m) {
			counted = append(counted, m)
		}
	}
	sort.Slice(counted, func(i, j int) bool { return counted[i].Metadata.Name < counted[j].Metadata.Name })

	var usedMachines int
	var usedCPU uint32
	var usedMemMiB uint64
	for _, m := range counted {
		usedMachines++
		cpu, mem := controller.MachineFootprint(m)
		usedCPU += cpu
		usedMemMiB += mem
	}

	fmt.Println()
	fmt.Println("Usage (recomputed fresh from Machines counted right now -- same MachineCountsTowardQuota predicate kairon-controller's own scheduling loop uses):")
	if quota.Spec.MaxMachines != nil {
		fmt.Printf("  %-9s %d / %d\n", "machines", usedMachines, *quota.Spec.MaxMachines)
	} else {
		fmt.Printf("  %-9s %d / (no limit)\n", "machines", usedMachines)
	}
	if quota.Spec.MaxTotalCPU != "" {
		max, _ := model.ParseVCPUs(quota.Spec.MaxTotalCPU) // already admitted onto this object, so already valid
		fmt.Printf("  %-9s %d vCPU / %d vCPU (spec.maxTotalCpu %q; %d vCPU headroom)\n", "cpu", usedCPU, max, quota.Spec.MaxTotalCPU, int64(max)-int64(usedCPU))
	} else {
		fmt.Printf("  %-9s %d vCPU / (no limit)\n", "cpu", usedCPU)
	}
	if quota.Spec.MaxTotalMemory != "" {
		max, _ := model.ParseMemoryMiB(quota.Spec.MaxTotalMemory) // already admitted onto this object, so already valid
		fmt.Printf("  %-9s %d MiB / %d MiB (spec.maxTotalMemory %q; %d MiB headroom)\n", "memory", usedMemMiB, max, quota.Spec.MaxTotalMemory, int64(max)-int64(usedMemMiB))
	} else {
		fmt.Printf("  %-9s %d MiB / (no limit)\n", "memory", usedMemMiB)
	}

	fmt.Println()
	fmt.Printf("Counted machines (%d):\n", len(counted))
	if len(counted) == 0 {
		fmt.Println("  (none -- no scheduled, non-Stopped/Halted Machine in this namespace counts against this quota right now)")
		return
	}
	for _, m := range counted {
		cpu, mem := controller.MachineFootprint(m)
		fmt.Printf("  %-24s %d vCPU  %d MiB\n", m.Metadata.Name, cpu, mem)
	}
}

// describeBudget is describe's fourth (and, for now, last -- see
// describeQuota's own doc comment for the third) raw-JSON-plus-preview
// exception. Unlike MachineQuota's status, a MachineDisruptionBudget's
// status.expectedMachines/currentHealthy/desiredHealthy/disruptionsAllowed
// are already four clearly-labeled, already-resolved counts in the same
// unit (Machines) -- reading the raw JSON tells an operator the *numbers*
// perfectly well on its own. What it can't tell them is *which* Machines
// are counted, and specifically *why* any of them isn't currently healthy
// -- the same "show the receipts behind the number" gap describeQuota's
// counted-machines list and describeSnapshotSchedule's/
// describeMigrationPolicy's own matching-machines lists already exist to
// close for their own CRDs. That's the one genuine addition this preview
// makes: list every Machine spec.selector currently matches, and for any
// that isn't counted as healthy, say whether it's because status.phase
// isn't Running or because a non-terminal MachineMigration already has it
// in flight.
//
// Status is recomputed fresh via controller.LoadBudgetStates -- the exact
// function both `kaironctl evacuate` and the opt-in admission webhook call
// to decide the same thing -- rather than trusted from status.*, which
// (like MachineQuota's) is only ever as fresh as the last reconcile tick.
// The per-Machine in-flight check below deliberately duplicates
// LoadBudgetStates' own inline terminal-phase test rather than importing
// it (that helper is unexported, and internal/controller/disruption.go's
// BudgetState doc comment already establishes this codebase's precedent of
// each consumer keeping its own narrow copy of a one-boolean predicate
// rather than exporting it for a single caller) -- so if that set of
// terminal phases ever changes, this comment is the reminder to update
// both places together.
func describeBudget(ctx context.Context, kc *kube.Client, ns, name string) {
	budget, err := kc.GetMachineDisruptionBudget(ctx, ns, name)
	if err != nil {
		fatal(err)
	}
	b, _ := json.MarshalIndent(budget, "", "  ")
	fmt.Println(string(b))

	machines, err := kc.ListMachinesNamespace(ctx, ns)
	if err != nil {
		fatal(err)
	}
	migrations, err := kc.ListMachineMigrationsNamespace(ctx, ns)
	if err != nil && !kube.IsNotFound(err) {
		fatal(err)
	}
	states, err := controller.LoadBudgetStates([]model.MachineDisruptionBudget{budget}, machines, migrations)
	if err != nil {
		fatal(err)
	}
	status := states[0].Status()

	inFlight := map[string]bool{}
	for _, mig := range migrations {
		terminal := false
		switch mig.Status.Phase {
		case "Succeeded", "Failed", "Blocked", "Cancelled", "":
			terminal = true
		}
		if !terminal {
			inFlight[mig.Namespace()+"/"+mig.Spec.MachineName] = true
		}
	}

	var matches []model.Machine
	for _, m := range machines {
		if model.LabelsMatch(m.Metadata.Labels, budget.Spec.Selector) {
			matches = append(matches, m)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Metadata.Name < matches[j].Metadata.Name })

	fmt.Println()
	fmt.Printf("Status (recomputed fresh from Machines/MachineMigrations right now): %d matching, %d healthy, %d desired healthy, %d disruptions allowed\n",
		status.ExpectedMachines, status.CurrentHealthy, status.DesiredHealthy, status.DisruptionsAllowed)
	fmt.Printf("Matching machines (%d):\n", len(matches))
	if len(matches) == 0 {
		fmt.Println("  (none -- check spec.selector against these Machines' own labels)")
		return
	}
	for _, m := range matches {
		health := "healthy"
		switch {
		case m.Status.Phase != "Running":
			health = fmt.Sprintf("NOT healthy (status.phase %q, not Running)", m.Status.Phase)
		case inFlight[m.Namespace()+"/"+m.Metadata.Name]:
			health = "NOT healthy (non-terminal MachineMigration in flight)"
		}
		fmt.Printf("  %-24s %s\n", m.Metadata.Name, health)
	}
}

// cmdTrigger dispatches `kaironctl trigger KIND NAME` -- deliberately its
// own top-level verb, not folded into `edit`, because it isn't editing any
// field a person would want to read back afterward (unlike every `edit`
// flag): it's a one-shot imperative request, the same shape
// cancel-migration already established for "ask the controller to do a
// thing now" rather than "change this object's configuration." Only
// snapshotschedule supports it today -- MigrationPolicy/
// MachineDisruptionBudget/etc. have nothing analogous to "run this right
// now" since they don't create anything on a timer in the first place.
func cmdTrigger(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 2 {
		fatal(fmt.Errorf("usage: kaironctl trigger snapshotschedule NAME [-n NAMESPACE]"))
	}
	kind := strings.ToLower(args[0])
	name := args[1]
	ns, _ := nsFlag(args[2:])
	switch kind {
	case "snapshotschedule", "snapshotschedules", "machinesnapshotschedules":
		cmdTriggerSnapshotSchedule(ctx, kc, ns, name)
	default:
		fatal(fmt.Errorf("trigger only supports snapshotschedule, got %q", kind))
	}
}

// cmdTriggerSnapshotSchedule requests an immediate, out-of-band run of a
// MachineSnapshotSchedule -- "back up these Machines right now" without
// waiting for spec.intervalSeconds to elapse, and without the awkward
// workarounds an operator had to reach for before this existed (temporarily
// shrinking intervalSeconds, or flipping spec.suspend off and back on).
//
// Implemented as the same durable-annotation-request pattern
// AnnotationQuiesceRequest/AnnotationCordonEvacuateAttemptedAt already
// establish for "ask the controller to do a thing on its next reconcile
// tick, and remember it was asked" -- not a bespoke RPC or subresource of
// its own. This just merge-patches
// model.AnnotationSnapshotScheduleTriggerNow to the current RFC3339
// timestamp (touching no other annotation the schedule already carries);
// reconcileMachineSnapshotSchedules notices any value that doesn't match
// status.lastHandledTriggerTime yet as an unhandled request on its very
// next tick, fires exactly like a normal due run (including
// spec.keepLast pruning), and records the timestamp it handled into
// status.lastHandledTriggerTime so the same request is never re-fired on a
// later tick.
//
// Bypasses BOTH spec.suspend and spec.startingDeadlineSeconds on the
// controller side (see reconcileMachineSnapshotSchedules) -- a paused
// schedule can still be asked for one snapshot right now without
// permanently unpausing it first, and "too late" has no meaning for a
// request that's asking to run at this exact instant. It does NOT bypass
// spec.selector: a schedule matching zero Machines right now still counts
// as triggered (status advances exactly like a normal zero-match tick), it
// just creates nothing.
//
// Firing this way still advances status.lastRunTime/nextRunTime exactly
// like any other fire -- the schedule's normal interval countdown restarts
// from this manual run, it doesn't layer a "bonus" run on top of the
// pre-existing schedule.
func cmdTriggerSnapshotSchedule(ctx context.Context, kc *kube.Client, ns, name string) {
	ts := time.Now().UTC().Format(time.RFC3339)
	patch := map[string]any{"metadata": map[string]any{"annotations": map[string]any{model.AnnotationSnapshotScheduleTriggerNow: ts}}}
	if err := kc.PatchMachineSnapshotSchedule(ctx, ns, name, patch); err != nil {
		fatal(fmt.Errorf("patch machinesnapshotschedule %s/%s: %w", ns, name, err))
	}
	fmt.Printf("snapshotschedule/%s: manual run requested; kairon-controller will snapshot every matching Machine on its next reconcile tick, regardless of spec.suspend or the normal interval\n", name)
}

// machineSpecFromFlags registers the Machine-spec-shaped flags shared by a
// plain `create` (one Machine) and `create machineset` (every replica's
// template) onto fs, returning a closure that builds the resulting
// model.MachineSpec once fs.Parse has run, plus the --image flag's own
// pointer so each caller can enforce its own "image is required" check
// after parsing (both do; a MachineSet with no image would never actually
// boot). Extracted so the two callers can never drift apart on how a flag
// maps onto MachineSpec -- anything this doesn't cover (placement, device
// claims, security, per-volume claims) needs kubectl apply/YAML for either
// caller, same limit `create` already had before `create machineset`
// existed.
func machineSpecFromFlags(fs *flag.FlagSet) (spec func() model.MachineSpec, image *string) {
	image = fs.String("image", "", "FluxVM host-local image path")
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
	priority := fs.Int("priority", 0, "scheduling priority: when a reconcile tick can't fit every pending Machine (node capacity or MachineQuota), higher values are attempted first; default 0, negative values are valid for a below-default class")
	return func() model.MachineSpec {
		pf, err := parseForwards(forwards)
		if err != nil {
			fatal(err)
		}
		return model.MachineSpec{
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
			Priority:   int32(*priority),
		}
	}, image
}

// cmdCreate handles both `kaironctl create NAME --image PATH [flags]` (a
// Machine, its original and unchanged form) and, dispatched by keyword,
// `create machineset|instancetype|migrationpolicy NAME [flags]`.
// "machineset"/"instancetype"/"migrationpolicy" are recognized as a KIND
// only by exact match against this fixed alias list, never by any other
// heuristic -- the same tradeoff resourceKindAndName's own doc comment
// already accepts for get/describe/delete: a Machine actually named e.g.
// "machineset" can't be created through this bare form (kubectl apply is
// the escape hatch, as it already is for every field these flags don't
// cover). Every other NAME falls straight through to the original
// create-a-Machine behavior below, byte-for-byte unchanged.
func cmdCreate(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl create NAME --image PATH [flags] | create machineset|instancetype|migrationpolicy|snapshotschedule|quota|budget|networkpolicy|securitygroup NAME [flags]"))
	}
	switch strings.ToLower(args[0]) {
	case "machineset", "machinesets":
		cmdCreateMachineSet(ctx, kc, args[1:])
		return
	case "instancetype", "instancetypes", "machineinstancetypes":
		cmdCreateInstanceType(ctx, kc, args[1:])
		return
	case "migrationpolicy", "migrationpolicies":
		cmdCreateMigrationPolicy(ctx, kc, args[1:])
		return
	case "snapshotschedule", "snapshotschedules", "machinesnapshotschedules":
		cmdCreateSnapshotSchedule(ctx, kc, args[1:])
		return
	case "quota", "quotas", "machinequotas":
		cmdCreateQuota(ctx, kc, args[1:])
		return
	case "budget", "budgets", "machinedisruptionbudgets":
		cmdCreateBudget(ctx, kc, args[1:])
		return
	case "networkpolicy", "networkpolicies", "machinenetworkpolicies":
		cmdCreateNetworkPolicy(ctx, kc, args[1:])
		return
	case "securitygroup", "securitygroups", "networksecuritygroups":
		cmdCreateSecurityGroup(ctx, kc, args[1:])
		return
	}
	name := args[0]
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	specFn, image := machineSpecFromFlags(fs)
	_ = fs.Parse(args[1:])
	if *image == "" {
		fatal(fmt.Errorf("--image PATH is required"))
	}
	m := model.Machine{
		TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachine},
		Metadata: model.ObjectMeta{Name: name, Namespace: *ns},
		Spec:     specFn(),
	}
	out, err := kc.CreateMachine(ctx, *ns, m)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("machine/%s created\n", out.Metadata.Name)
}

// cmdCreateMachineSet handles `kaironctl create machineset NAME [flags]`,
// dispatched from cmdCreate. Reuses machineSpecFromFlags for
// Template.Spec so a MachineSet's per-replica spec is built exactly the
// same way a plain Machine's is.
func cmdCreateMachineSet(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl create machineset NAME --image PATH [--replicas N] [flags]"))
	}
	name := args[0]
	fs := flag.NewFlagSet("create machineset", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	replicas := fs.Int("replicas", 1, "desired Machine count")
	strategy := fs.String("strategy", "", "RollingUpdate (default) | Recreate")
	maxUnavailable := fs.String("max-unavailable", "", "integer or percentage bound on simultaneously-missing/outdated replicas during RollingUpdate; empty defaults to 1")
	var labels stringSliceFlag
	fs.Var(&labels, "label", "label key=value applied to every replica this MachineSet creates, in addition to its own bookkeeping labels (repeatable)")
	specFn, image := machineSpecFromFlags(fs)
	_ = fs.Parse(args[1:])
	if *image == "" {
		fatal(fmt.Errorf("--image PATH is required"))
	}
	labelMap, err := parseKeyValues(labels)
	if err != nil {
		fatal(err)
	}
	ms := model.MachineSet{
		TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachineSet},
		Metadata: model.ObjectMeta{Name: name, Namespace: *ns},
		Spec: model.MachineSetSpec{
			Replicas:       *replicas,
			Template:       model.MachineTemplate{Labels: labelMap, Spec: specFn()},
			Strategy:       *strategy,
			MaxUnavailable: *maxUnavailable,
		},
	}
	out, err := kc.CreateMachineSet(ctx, *ns, ms)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("machineset/%s created\n", out.Metadata.Name)
}

// cmdCreateInstanceType handles `kaironctl create instancetype NAME --cpu N
// --memory SIZE [flags]`, dispatched from cmdCreate. A strict subset of
// cmdCreate's own resource flags -- MachineInstanceTypeSpec is just a
// ResourceSpec, nothing else to map.
func cmdCreateInstanceType(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl create instancetype NAME --cpu N --memory SIZE [flags]"))
	}
	name := args[0]
	fs := flag.NewFlagSet("create instancetype", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	cpu := fs.String("cpu", "", "vCPU quantity (required)")
	memory := fs.String("memory", "", "memory quantity (required)")
	maxCPU := fs.String("max-cpu", "", "hotplug headroom vCPU ceiling")
	maxMemory := fs.String("max-memory", "", "hotplug headroom memory ceiling")
	hugepages := fs.Bool("hugepages", false, "back guest memory with hugepages (qemu only)")
	numaNode := fs.Int("numa-node", -1, "pin to a specific host NUMA node (qemu only); -1 leaves it unset")
	cpuSet := fs.String("cpu-set", "", "guest-visible vNUMA CPUSet hint (qemu only)")
	cpuPinning := fs.Bool("cpu-pinning", false, "real, exclusive host-core allocation (qemu only)")
	_ = fs.Parse(args[1:])
	if *cpu == "" || *memory == "" {
		fatal(fmt.Errorf("--cpu and --memory are both required"))
	}
	resources := model.ResourceSpec{
		CPU: *cpu, Memory: *memory, MaxCPU: *maxCPU, MaxMemory: *maxMemory,
		Hugepages: *hugepages, CPUSet: *cpuSet, CPUPinning: *cpuPinning,
	}
	if *numaNode >= 0 {
		resources.NUMANode = numaNode
	}
	it := model.MachineInstanceType{
		TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachineInstanceType},
		Metadata: model.ObjectMeta{Name: name, Namespace: *ns},
		Spec:     model.MachineInstanceTypeSpec{Resources: resources},
	}
	out, err := kc.CreateMachineInstanceType(ctx, *ns, it)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("instancetype/%s created\n", out.Metadata.Name)
}

// cmdCreateMigrationPolicy handles `kaironctl create migrationpolicy NAME
// --selector k=v [flags]`, dispatched from cmdCreate.
func cmdCreateMigrationPolicy(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl create migrationpolicy NAME --selector k=v [flags]"))
	}
	name := args[0]
	fs := flag.NewFlagSet("create migrationpolicy", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	var selector stringSliceFlag
	fs.Var(&selector, "selector", "label key=value this policy applies to (repeatable, required)")
	bandwidth := fs.Uint64("bandwidth-mbps", 0, "default migration bandwidth for a matching Machine's migration, if it didn't already set one explicitly")
	maxConcurrent := fs.Int("max-concurrent", 0, "cap on simultaneous non-terminal migrations of matching Machines cluster-wide; 0 is unlimited within this policy's own scope")
	_ = fs.Parse(args[1:])
	if len(selector) == 0 {
		fatal(fmt.Errorf("--selector k=v is required (repeatable)"))
	}
	selectorMap, err := parseKeyValues(selector)
	if err != nil {
		fatal(err)
	}
	p := model.MigrationPolicy{
		TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMigrationPolicy},
		Metadata: model.ObjectMeta{Name: name, Namespace: *ns},
		Spec:     model.MigrationPolicySpec{Selector: selectorMap, BandwidthMbps: *bandwidth, MaxConcurrent: *maxConcurrent},
	}
	out, err := kc.CreateMigrationPolicy(ctx, *ns, p)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("migrationpolicy/%s created\n", out.Metadata.Name)
}

// cmdCreateSnapshotSchedule handles `kaironctl create snapshotschedule NAME
// --selector k=v --interval-seconds N [flags]`, dispatched from cmdCreate.
func cmdCreateSnapshotSchedule(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl create snapshotschedule NAME --selector k=v --interval-seconds N [flags]"))
	}
	name := args[0]
	fs := flag.NewFlagSet("create snapshotschedule", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	var selector stringSliceFlag
	fs.Var(&selector, "selector", "label key=value a Machine must match to be snapshotted (repeatable, required)")
	intervalSeconds := fs.Int("interval-seconds", 0, "minimum seconds between runs (required, minimum 60)")
	volumeSnapshotClassName := fs.String("volume-snapshot-class", "", "VolumeSnapshotClassName passed through to every MachineSnapshot this schedule creates")
	suspend := fs.Bool("suspend", false, "create the schedule already suspended")
	keepLast := fs.Int("keep-last", 0, "retain only the N most recent ready-to-use snapshots this schedule created per Machine, deleting older ones (0, the default, never prunes)")
	startingDeadlineSeconds := fs.Int("starting-deadline-seconds", 0, "skip (rather than immediately fire) a run found more than this many seconds late, e.g. after the controller was down (0, the default, never skips -- an overdue run always fires)")
	_ = fs.Parse(args[1:])
	if len(selector) == 0 {
		fatal(fmt.Errorf("--selector k=v is required (repeatable)"))
	}
	if *intervalSeconds < 60 {
		fatal(fmt.Errorf("--interval-seconds N is required and must be at least 60"))
	}
	selectorMap, err := parseKeyValues(selector)
	if err != nil {
		fatal(err)
	}
	s := model.MachineSnapshotSchedule{
		TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachineSnapshotSchedule},
		Metadata: model.ObjectMeta{Name: name, Namespace: *ns},
		Spec: model.MachineSnapshotScheduleSpec{
			Selector:                selectorMap,
			IntervalSeconds:         *intervalSeconds,
			VolumeSnapshotClassName: *volumeSnapshotClassName,
			Suspend:                 *suspend,
			KeepLast:                *keepLast,
			StartingDeadlineSeconds: *startingDeadlineSeconds,
		},
	}
	out, err := kc.CreateMachineSnapshotSchedule(ctx, *ns, s)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("snapshotschedule/%s created\n", out.Metadata.Name)
}

// cmdCreateQuota handles `kaironctl create quota NAME [--max-machines N]
// [--max-total-cpu N] [--max-total-memory SIZE]`, dispatched from cmdCreate.
// Unlike migrationpolicy/snapshotschedule's selector, MachineQuotaSpec has
// no required field the CRD itself enforces (every dimension is optional --
// see internal/model/machinequota.go's own doc comment), but a MachineQuota
// with every dimension unset caps nothing at all, so this still refuses to
// create one -- the same "don't let an operator create a resource that
// provably does nothing" instinct as requiring --selector elsewhere, just
// enforced client-side here since the CRD schema can't express "at least
// one of these three."
func cmdCreateQuota(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl create quota NAME [--max-machines N] [--max-total-cpu N] [--max-total-memory SIZE]"))
	}
	name := args[0]
	fs := flag.NewFlagSet("create quota", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	maxMachines := fs.Int("max-machines", -1, "cap on scheduled Machine count in this namespace; -1 (the default) leaves it unset (no cap on this dimension)")
	maxTotalCPU := fs.String("max-total-cpu", "", "cap on total vCPUs scheduled in this namespace, summed across every scheduled Machine's spec.resources.cpu")
	maxTotalMemory := fs.String("max-total-memory", "", "cap on total memory scheduled in this namespace, summed across every scheduled Machine's spec.resources.memory")
	_ = fs.Parse(args[1:])
	if *maxMachines < 0 && *maxTotalCPU == "" && *maxTotalMemory == "" {
		fatal(fmt.Errorf("at least one of --max-machines, --max-total-cpu, --max-total-memory is required -- a quota with no dimension set caps nothing"))
	}
	spec := model.MachineQuotaSpec{MaxTotalCPU: *maxTotalCPU, MaxTotalMemory: *maxTotalMemory}
	if *maxMachines >= 0 {
		spec.MaxMachines = maxMachines
	}
	q := model.MachineQuota{
		TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachineQuota},
		Metadata: model.ObjectMeta{Name: name, Namespace: *ns},
		Spec:     spec,
	}
	out, err := kc.CreateMachineQuota(ctx, *ns, q)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("quota/%s created\n", out.Metadata.Name)
}

// cmdCreateBudget handles `kaironctl create budget NAME --selector k=v
// (--min-available X | --max-unavailable X)`, dispatched from cmdCreate.
// Requires exactly one of --min-available/--max-unavailable, mirroring
// MachineDisruptionBudgetSpec.DesiredHealthy's own documented contract
// (internal/model/disruption.go) -- a budget created with both or neither
// set would parse fine against the CRD schema (which doesn't express
// "exactly one of," the same limit --max-machines above works around) but
// would then fail DesiredHealthy on every reconcile tick and every
// `evacuate` check, so this catches the mistake up front instead of
// shipping a silently-broken budget.
func cmdCreateBudget(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl create budget NAME --selector k=v (--min-available X | --max-unavailable X)"))
	}
	name := args[0]
	fs := flag.NewFlagSet("create budget", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	var selector stringSliceFlag
	fs.Var(&selector, "selector", "label key=value a Machine must match to count against this budget (repeatable, required)")
	minAvailable := fs.String("min-available", "", "integer or \"N%\" floor on healthy matching Machines (exactly one of this and --max-unavailable is required)")
	maxUnavailable := fs.String("max-unavailable", "", "integer or \"N%\" ceiling on unhealthy/disrupted matching Machines (exactly one of this and --min-available is required)")
	_ = fs.Parse(args[1:])
	if len(selector) == 0 {
		fatal(fmt.Errorf("--selector k=v is required (repeatable)"))
	}
	if (*minAvailable == "") == (*maxUnavailable == "") {
		fatal(fmt.Errorf("exactly one of --min-available or --max-unavailable is required, not both or neither"))
	}
	selectorMap, err := parseKeyValues(selector)
	if err != nil {
		fatal(err)
	}
	b := model.MachineDisruptionBudget{
		TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachineDisruptionBudget},
		Metadata: model.ObjectMeta{Name: name, Namespace: *ns},
		Spec:     model.MachineDisruptionBudgetSpec{Selector: selectorMap, MinAvailable: *minAvailable, MaxUnavailable: *maxUnavailable},
	}
	out, err := kc.CreateMachineDisruptionBudget(ctx, *ns, b)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("budget/%s created\n", out.Metadata.Name)
}

// vmNetworkPolicyFromFlags registers every model.VmNetworkPolicy field as a
// flag on fs and returns a closure that builds the struct from whatever was
// parsed -- shared between cmdCreateNetworkPolicy and cmdCreateSecurityGroup
// since both CRDs embed the exact same nested policy shape (see
// model.VmNetworkPolicy's own doc comment: "mirrors FluxVM VmNetworkPolicy").
// spec.cnp (MachineNetworkPolicy-only, a free-form FluxVM CiliumNetworkPolicy
// document) is deliberately not exposed here -- same "stays create-time-only
// through kubectl/YAML, not a CLI flag" treatment cmdEditMachine already
// gives every Machine-spec field this CLI doesn't expose flags for; an
// arbitrary nested JSON document has no sensible flag shape anyway.
func vmNetworkPolicyFromFlags(fs *flag.FlagSet) func() model.VmNetworkPolicy {
	defaultAllow := fs.Bool("default-allow", false, "allow everything by default; allow/deny-cidr and allow-port then narrow that instead of building up allowlists from a default-deny baseline")
	var allowCidrs, denyCidrs, allowPorts, allowFqdns, policyGroups, policyLabels, entities stringSliceFlag
	fs.Var(&allowCidrs, "allow-cidr", "destination CIDR to allow, e.g. 10.0.0.0/8 (repeatable)")
	fs.Var(&denyCidrs, "deny-cidr", "destination CIDR to deny (repeatable)")
	fs.Var(&allowPorts, "allow-port", "proto/port rule to allow, e.g. tcp/443 or udp/53 (repeatable)")
	fs.Var(&allowFqdns, "allow-fqdn", "FQDN to allow, resolved by FluxVM at apply time (repeatable)")
	fs.Var(&policyGroups, "policy-group", "NetworkSecurityGroup name whose membership this policy inherits (repeatable)")
	fs.Var(&policyLabels, "policy-label", "key=value tag this policy matches NetworkSecurityGroup membership against, e.g. tier=frontend (repeatable)")
	fs.Var(&entities, "entity", "well-known FluxVM entity to allow, e.g. world/cluster (repeatable)")
	maxEgressMbps := fs.Uint64("max-egress-mbps", 0, "cap egress bandwidth in Mbps; 0 (the default) leaves it uncapped")
	maxEgressPps := fs.Uint64("max-egress-pps", 0, "cap egress packet rate in packets/sec; 0 (the default) leaves it uncapped")
	auditMode := fs.Bool("audit-mode", false, "log traffic that would be denied instead of dropping it")
	allowIcmp := fs.Bool("allow-icmp", false, "permit ICMP/ICMPv6 regardless of --allow-port")
	sampleRate := fs.Uint("sample-rate", 0, "flow-log sampling rate; 0 (the default) means no sampling")
	return func() model.VmNetworkPolicy {
		p := model.VmNetworkPolicy{
			DefaultAllow: *defaultAllow,
			AllowCidrs:   []string(allowCidrs),
			DenyCidrs:    []string(denyCidrs),
			AllowPorts:   []string(allowPorts),
			AllowFqdns:   []string(allowFqdns),
			Groups:       []string(policyGroups),
			Labels:       []string(policyLabels),
			Entities:     []string(entities),
			AuditMode:    *auditMode,
			AllowIcmp:    *allowIcmp,
			SampleRate:   uint32(*sampleRate),
		}
		if *maxEgressMbps > 0 {
			v := uint32(*maxEgressMbps)
			p.MaxEgressMbps = &v
		}
		if *maxEgressPps > 0 {
			v := uint32(*maxEgressPps)
			p.MaxEgressPps = &v
		}
		return p
	}
}

// cmdCreateNetworkPolicy handles `kaironctl create networkpolicy NAME
// (--machine-name X | --selector k=v) [policy flags]`, dispatched from
// cmdCreate -- closing the same "no kaironctl create for this CRD at all"
// gap cmdCreateSecurityGroup closes for its sibling. Requires at least one
// of --machine-name/--selector, the same "don't create an object that
// provably matches nothing" instinct as cmdCreateQuota's own dimension
// check: MachineNetworkPolicySpec.MachineName/Selector are both optional at
// the CRD-schema level, but a policy with neither set matches no Machine at
// all (model.LabelsMatch's own doc comment: an empty selector never
// matches), the same silently-does-nothing shape this project has
// consistently refused to let `create` produce elsewhere.
func cmdCreateNetworkPolicy(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl create networkpolicy NAME (--machine-name X | --selector k=v) [--allow-cidr CIDR] [--deny-cidr CIDR] [--allow-port proto/port] [flags]"))
	}
	name := args[0]
	fs := flag.NewFlagSet("create networkpolicy", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	machineName := fs.String("machine-name", "", "single Machine (same namespace) this policy targets; takes precedence over --selector")
	var selector stringSliceFlag
	fs.Var(&selector, "selector", "label key=value a Machine must match when --machine-name is unset (repeatable)")
	policyFn := vmNetworkPolicyFromFlags(fs)
	_ = fs.Parse(args[1:])
	if *machineName == "" && len(selector) == 0 {
		fatal(fmt.Errorf("at least one of --machine-name or --selector is required -- a policy with neither set matches no Machine"))
	}
	selectorMap, err := parseKeyValues(selector)
	if err != nil {
		fatal(err)
	}
	p := model.MachineNetworkPolicy{
		TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachineNetworkPolicy},
		Metadata: model.ObjectMeta{Name: name, Namespace: *ns},
		Spec: model.MachineNetworkPolicySpec{
			MachineName: *machineName,
			Selector:    selectorMap,
			Policy:      policyFn(),
		},
	}
	out, err := kc.CreateMachineNetworkPolicy(ctx, *ns, p)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("networkpolicy/%s created\n", out.Metadata.Name)
}

// cmdCreateSecurityGroup handles `kaironctl create securitygroup NAME
// [--group-name X] [--priority N] [--description TEXT] [policy flags]`,
// dispatched from cmdCreate. Unlike MachineNetworkPolicy, NetworkSecurityGroup
// has no required targeting field -- it's a named, reusable group other
// policies opt into via their own --policy-group -- so an empty --group-name
// is fine (Spec.GroupName defaults to metadata.name, see
// NetworkSecurityGroup.FluxGroupName) and there is no "matches nothing"
// failure mode to guard against here.
func cmdCreateSecurityGroup(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl create securitygroup NAME [--group-name X] [--priority N] [--description TEXT] [--allow-cidr CIDR] [flags]"))
	}
	name := args[0]
	fs := flag.NewFlagSet("create securitygroup", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	groupName := fs.String("group-name", "", "FluxVM group name; defaults to NAME when unset")
	priority := fs.Uint("priority", 0, "lower value wins on a rate/deny tie against another group")
	description := fs.String("description", "", "human-readable note carried through to FluxVM")
	var groupLabels stringSliceFlag
	fs.Var(&groupLabels, "group-label", "key=value tag FluxVM matches group membership against, e.g. tier=frontend (repeatable)")
	policyFn := vmNetworkPolicyFromFlags(fs)
	_ = fs.Parse(args[1:])
	g := model.NetworkSecurityGroup{
		TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindNetworkSecurityGroup},
		Metadata: model.ObjectMeta{Name: name, Namespace: *ns},
		Spec: model.NetworkSecurityGroupSpec{
			GroupName:   *groupName,
			Labels:      []string(groupLabels),
			Priority:    uint32(*priority),
			Description: *description,
			Policy:      policyFn(),
		},
	}
	out, err := kc.CreateNetworkSecurityGroup(ctx, *ns, g)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("securitygroup/%s created\n", out.Metadata.Name)
}

// cmdScale mutates spec.replicas on an existing MachineSet -- the only
// resource kind this verb supports for a first cut, since no other kind
// kaironctl manages has a sensible "scale" operation. Takes its own
// lightweight KIND NAME split (not resourceKindAndName, which is shaped
// for get/describe/delete's "1 arg defaults to machine" convention and
// doesn't fit a verb that always requires an explicit KIND plus trailing
// flags). Once --selector appears anywhere after KIND, dispatches to
// cmdScaleSelector for the bulk "same replica count across every match"
// form instead of expecting a single trailing NAME -- see
// cmdScaleSelector's own doc comment for why scale (unlike edit) is a
// genuinely good fit for that.
func cmdScale(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl scale machineset NAME --replicas N | kaironctl scale machineset --selector k=v --replicas N [--dry-run]"))
	}
	kind := strings.ToLower(args[0])
	if kind != "machineset" && kind != "machinesets" {
		fatal(fmt.Errorf("scale only supports machineset, got %q", kind))
	}
	rest := args[1:]
	if hasFlag(rest, "selector") {
		cmdScaleSelector(ctx, kc, rest)
		return
	}
	if len(rest) < 1 {
		fatal(fmt.Errorf("usage: kaironctl scale machineset NAME --replicas N"))
	}
	name := rest[0]
	fs := flag.NewFlagSet("scale", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	replicas := fs.Int("replicas", -1, "desired replica count (required)")
	_ = fs.Parse(rest[1:])
	if *replicas < 0 {
		fatal(fmt.Errorf("--replicas N is required"))
	}
	if err := kc.PatchMachineSet(ctx, *ns, name, map[string]any{"spec": map[string]any{"replicas": *replicas}}); err != nil {
		fatal(err)
	}
	fmt.Printf("machineset/%s scaled to %d replicas\n", name, *replicas)
}

// cmdScaleSelector implements `scale machineset --selector k=v [--selector
// k2=v2] --replicas N [--namespace NS] [--dry-run]`: patches spec.replicas
// to the SAME value on every MachineSet in ns whose labels satisfy every
// given key=value pair -- e.g. scaling every MachineSet labeled
// env=staging to 0 before a maintenance window, or every tier=web
// MachineSet up together after a capacity change. This mirrors
// cmdDeleteSelector's shape deliberately (matchingNames to resolve the
// selector, required non-empty --selector, sorted deterministic order,
// --dry-run, one PATCH per match through the exact same single-object
// kc.PatchMachineSet call the non-bulk path above uses) since it's the
// same "resolve a selector to a list of names, then act on each one
// through the existing single-object path" bulk pattern.
//
// scale is a genuinely good fit for this, unlike edit: it has exactly one
// mutable field (replicas), and a bulk caller wants that ONE value applied
// identically to every match. edit's kinds (migrationpolicy,
// snapshotschedule, quota, budget, machine) each expose several
// independent fields, and "apply the same --max-concurrent to N
// differently-configured policies at once" is a far less obviously safe
// or wanted operation than "scale everything matching this selector to
// N" -- so --selector is intentionally NOT added to cmdEdit.
func cmdScaleSelector(ctx context.Context, kc *kube.Client, args []string) {
	fs := flag.NewFlagSet("scale", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	var selectorFlag stringSliceFlag
	fs.Var(&selectorFlag, "selector", "label key=value every matching MachineSet must carry (repeatable -- every pair must match; required and must be non-empty)")
	replicas := fs.Int("replicas", -1, "desired replica count applied to every matching MachineSet (required)")
	dryRun := fs.Bool("dry-run", false, "print what would be scaled without scaling anything")
	_ = fs.Parse(args)
	if *replicas < 0 {
		fatal(fmt.Errorf("--replicas N is required"))
	}
	sel, err := parseKeyValues(selectorFlag)
	if err != nil {
		fatal(err)
	}
	if len(sel) == 0 {
		fatal(fmt.Errorf("--selector is required and must be non-empty for bulk scale (usage: kaironctl scale machineset --selector k=v --replicas N) -- an empty selector matches nothing, by design, rather than risk being misread as \"everything\""))
	}
	names, err := matchingNames(ctx, kc, *ns, "machineset", sel)
	if err != nil {
		fatal(err)
	}
	if len(names) == 0 {
		fmt.Println("no machinesets matched selector; nothing to scale")
		return
	}
	sort.Strings(names) // deterministic order for a repeatable dry-run/real-run diff
	for _, name := range names {
		if *dryRun {
			fmt.Printf("machineset/%s (dry-run, not scaled)\n", name)
			continue
		}
		if err := kc.PatchMachineSet(ctx, *ns, name, map[string]any{"spec": map[string]any{"replicas": *replicas}}); err != nil {
			fatal(fmt.Errorf("scaling machineset/%s: %w", name, err))
		}
		fmt.Printf("machineset/%s scaled to %d replicas\n", name, *replicas)
	}
}

// cmdEdit patches a subset of an existing object's spec fields, touching
// only the ones an explicit flag was actually passed for on this
// invocation (tracked via fs.Visit, never a flag's zero-value default) so
// an omitted flag can never clobber an already-set value back to zero.
// migrationpolicy, snapshotschedule, quota, budget, machine, networkpolicy,
// and securitygroup are the only kinds this verb supports for a first cut.
func cmdEdit(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 2 {
		fatal(fmt.Errorf("usage: kaironctl edit migrationpolicy NAME [--bandwidth-mbps N] [--max-concurrent N] | edit snapshotschedule NAME [--suspend true|false] [--interval-seconds N] [--keep-last N] [--starting-deadline-seconds N] | edit quota NAME [--max-machines N] [--max-total-cpu N] [--max-total-memory SIZE] | edit budget NAME [--selector k=v] [--min-available X] [--max-unavailable X] | edit machine NAME --priority N | edit networkpolicy NAME [--machine-name X] [--selector k=v] [--allow-cidr CIDR] [--deny-cidr CIDR] [--allow-port proto/port] [--default-allow BOOL] [--audit-mode BOOL] [--max-egress-mbps N] [--max-egress-pps N] | edit securitygroup NAME [--group-label k=v] [--priority N] [--description TEXT] [policy flags as above]"))
	}
	kind, name := strings.ToLower(args[0]), args[1]
	switch kind {
	case "migrationpolicy", "migrationpolicies":
		cmdEditMigrationPolicy(ctx, kc, name, args[2:])
	case "snapshotschedule", "snapshotschedules", "machinesnapshotschedules":
		cmdEditSnapshotSchedule(ctx, kc, name, args[2:])
	case "quota", "quotas", "machinequotas":
		cmdEditQuota(ctx, kc, name, args[2:])
	case "budget", "budgets", "machinedisruptionbudgets":
		cmdEditBudget(ctx, kc, name, args[2:])
	case "machine", "machines":
		cmdEditMachine(ctx, kc, name, args[2:])
	case "networkpolicy", "networkpolicies", "machinenetworkpolicies":
		cmdEditNetworkPolicy(ctx, kc, name, args[2:])
	case "securitygroup", "securitygroups", "networksecuritygroups":
		cmdEditSecurityGroup(ctx, kc, name, args[2:])
	default:
		fatal(fmt.Errorf("edit only supports migrationpolicy, snapshotschedule, quota, budget, machine, networkpolicy, or securitygroup, got %q", kind))
	}
}

// cmdEditMachine patches only spec.priority for a first cut -- the one
// Machine-spec field this project considers safe to change on an
// already-created (possibly already-scheduled) Machine via a narrow
// merge-patch, since it's purely an admission-order hint for a future
// reconcile tick (see MachineSpec.Priority's own doc comment) and never
// itself triggers a re-realization. Every other Machine-spec field
// (image, resources, network, ...) stays create-time-only through this
// CLI, same as before `edit machine` existed -- kubectl apply/YAML is
// still the only way to change those, exactly like the fields `create`
// itself doesn't expose flags for.
func cmdEditMachine(ctx context.Context, kc *kube.Client, name string, args []string) {
	fs := flag.NewFlagSet("edit machine", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	priority := fs.Int("priority", 0, "new scheduling priority (higher wins a scarce reconcile-tick race for node capacity or MachineQuota headroom; see kaironctl create --priority)")
	_ = fs.Parse(args)
	spec := map[string]any{}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "priority" {
			spec["priority"] = *priority
		}
	})
	if len(spec) == 0 {
		fatal(fmt.Errorf("nothing to edit: pass --priority"))
	}
	if err := kc.PatchMachine(ctx, *ns, name, map[string]any{"spec": spec}); err != nil {
		fatal(err)
	}
	fmt.Printf("machine/%s updated\n", name)
}

func cmdEditMigrationPolicy(ctx context.Context, kc *kube.Client, name string, args []string) {
	fs := flag.NewFlagSet("edit migrationpolicy", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	bandwidth := fs.Uint64("bandwidth-mbps", 0, "new default migration bandwidth")
	maxConcurrent := fs.Int("max-concurrent", 0, "new cap on simultaneous non-terminal migrations")
	_ = fs.Parse(args)
	spec := map[string]any{}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "bandwidth-mbps":
			spec["bandwidthMbps"] = *bandwidth
		case "max-concurrent":
			spec["maxConcurrent"] = *maxConcurrent
		}
	})
	if len(spec) == 0 {
		fatal(fmt.Errorf("nothing to edit: pass at least one of --bandwidth-mbps or --max-concurrent"))
	}
	if err := kc.PatchMigrationPolicy(ctx, *ns, name, map[string]any{"spec": spec}); err != nil {
		fatal(err)
	}
	fmt.Printf("migrationpolicy/%s updated\n", name)
}

func cmdEditSnapshotSchedule(ctx context.Context, kc *kube.Client, name string, args []string) {
	fs := flag.NewFlagSet("edit snapshotschedule", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	suspend := fs.Bool("suspend", false, "pause (true) or resume (false) this schedule")
	intervalSeconds := fs.Int("interval-seconds", 0, "new minimum seconds between runs (minimum 60)")
	keepLast := fs.Int("keep-last", 0, "retain only the N most recent ready-to-use snapshots this schedule created per Machine (0 disables pruning again)")
	startingDeadlineSeconds := fs.Int("starting-deadline-seconds", 0, "skip (rather than immediately fire) a run found more than this many seconds late (0 disables the deadline again -- an overdue run always fires)")
	_ = fs.Parse(args)
	spec := map[string]any{}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "suspend":
			spec["suspend"] = *suspend
		case "interval-seconds":
			spec["intervalSeconds"] = *intervalSeconds
		case "keep-last":
			spec["keepLast"] = *keepLast
		case "starting-deadline-seconds":
			spec["startingDeadlineSeconds"] = *startingDeadlineSeconds
		}
	})
	if len(spec) == 0 {
		fatal(fmt.Errorf("nothing to edit: pass at least one of --suspend, --interval-seconds, --keep-last, or --starting-deadline-seconds"))
	}
	if err := kc.PatchMachineSnapshotSchedule(ctx, *ns, name, map[string]any{"spec": spec}); err != nil {
		fatal(err)
	}
	fmt.Printf("snapshotschedule/%s updated\n", name)
}

// cmdEditQuota patches only the MachineQuota dimensions an explicit flag
// was passed for, same fs.Visit convention as cmdEditMigrationPolicy/
// cmdEditSnapshotSchedule above -- an omitted flag never clobbers an
// already-configured cap back to "unset."
func cmdEditQuota(ctx context.Context, kc *kube.Client, name string, args []string) {
	fs := flag.NewFlagSet("edit quota", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	maxMachines := fs.Int("max-machines", 0, "new cap on scheduled Machine count in this namespace")
	maxTotalCPU := fs.String("max-total-cpu", "", "new cap on total vCPUs scheduled in this namespace")
	maxTotalMemory := fs.String("max-total-memory", "", "new cap on total memory scheduled in this namespace")
	_ = fs.Parse(args)
	spec := map[string]any{}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "max-machines":
			spec["maxMachines"] = *maxMachines
		case "max-total-cpu":
			spec["maxTotalCpu"] = *maxTotalCPU
		case "max-total-memory":
			spec["maxTotalMemory"] = *maxTotalMemory
		}
	})
	if len(spec) == 0 {
		fatal(fmt.Errorf("nothing to edit: pass at least one of --max-machines, --max-total-cpu, or --max-total-memory"))
	}
	if err := kc.PatchMachineQuota(ctx, *ns, name, map[string]any{"spec": spec}); err != nil {
		fatal(err)
	}
	fmt.Printf("quota/%s updated\n", name)
}

// cmdEditBudget patches only the MachineDisruptionBudget fields an explicit
// flag was passed for, same fs.Visit convention as the other edit
// subcommands. Passing both --min-available and --max-unavailable in the
// same call is refused, matching cmdCreateBudget's own exactly-one
// validation -- passing only one of them is always fine, including to
// switch a budget from one bound type to the other (the apiserver-side
// merge patch leaves the other field's prior value in place, so a full
// switch needs a follow-up kubectl/YAML edit to clear the old field, a
// known first-cut limit of this verb's simple merge-patch shape).
func cmdEditBudget(ctx context.Context, kc *kube.Client, name string, args []string) {
	fs := flag.NewFlagSet("edit budget", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	var selector stringSliceFlag
	fs.Var(&selector, "selector", "new label key=value selector (repeatable; replaces the entire existing selector when passed)")
	minAvailable := fs.String("min-available", "", "new integer or \"N%\" floor on healthy matching Machines")
	maxUnavailable := fs.String("max-unavailable", "", "new integer or \"N%\" ceiling on unhealthy/disrupted matching Machines")
	_ = fs.Parse(args)
	if *minAvailable != "" && *maxUnavailable != "" {
		fatal(fmt.Errorf("cannot pass both --min-available and --max-unavailable in the same edit"))
	}
	spec := map[string]any{}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "selector":
			selectorMap, err := parseKeyValues(selector)
			if err != nil {
				fatal(err)
			}
			spec["selector"] = selectorMap
		case "min-available":
			spec["minAvailable"] = *minAvailable
		case "max-unavailable":
			spec["maxUnavailable"] = *maxUnavailable
		}
	})
	if len(spec) == 0 {
		fatal(fmt.Errorf("nothing to edit: pass at least one of --selector, --min-available, or --max-unavailable"))
	}
	if err := kc.PatchMachineDisruptionBudget(ctx, *ns, name, map[string]any{"spec": spec}); err != nil {
		fatal(err)
	}
	fmt.Printf("budget/%s updated\n", name)
}

// editableVmNetworkPolicyFlags registers the subset of model.VmNetworkPolicy
// fields this project considers safe to change in place on an existing
// MachineNetworkPolicy/NetworkSecurityGroup -- the same "first cut, narrower
// than create" scoping cmdEditMachine already applies to Machine.spec (see
// its own doc comment): --allow-cidr/--deny-cidr/--allow-port/
// --default-allow/--audit-mode/--max-egress-mbps/--max-egress-pps cover the
// routine "widen/narrow this rule" and "flip to dry-run before enforcing"
// edits an incident or a policy review actually needs day to day.
// --allow-fqdn/--policy-group/--policy-label/--entity/--allow-icmp/
// --sample-rate stay create-time-only through this CLI for now; kubectl
// edit/apply is still how those change, exactly like every Machine-spec
// field cmdEditMachine doesn't expose a flag for. Returns the JSON-tag-keyed
// patch fragment for spec.policy, or nil if nothing was touched -- the
// caller merges it under "policy" only when non-nil, so an edit that
// changes none of these fields (e.g. only --selector) never sends a bogus
// empty policy object.
func editableVmNetworkPolicyFlags(fs *flag.FlagSet) func() map[string]any {
	var allowCidrs, denyCidrs, allowPorts stringSliceFlag
	fs.Var(&allowCidrs, "allow-cidr", "new destination CIDRs to allow (repeatable; replaces the entire existing list when passed)")
	fs.Var(&denyCidrs, "deny-cidr", "new destination CIDRs to deny (repeatable; replaces the entire existing list when passed)")
	fs.Var(&allowPorts, "allow-port", "new proto/port rules to allow, e.g. tcp/443 (repeatable; replaces the entire existing list when passed)")
	defaultAllow := fs.Bool("default-allow", false, "new default-allow setting")
	auditMode := fs.Bool("audit-mode", false, "new audit-mode setting (true logs would-be-denied traffic instead of dropping it)")
	maxEgressMbps := fs.Uint64("max-egress-mbps", 0, "new egress bandwidth cap in Mbps")
	maxEgressPps := fs.Uint64("max-egress-pps", 0, "new egress packet-rate cap in packets/sec")
	return func() map[string]any {
		policy := map[string]any{}
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "allow-cidr":
				policy["allowCidrs"] = []string(allowCidrs)
			case "deny-cidr":
				policy["denyCidrs"] = []string(denyCidrs)
			case "allow-port":
				policy["allowPorts"] = []string(allowPorts)
			case "default-allow":
				policy["defaultAllow"] = *defaultAllow
			case "audit-mode":
				policy["auditMode"] = *auditMode
			case "max-egress-mbps":
				policy["maxEgressMbps"] = *maxEgressMbps
			case "max-egress-pps":
				policy["maxEgressPps"] = *maxEgressPps
			}
		})
		if len(policy) == 0 {
			return nil
		}
		return policy
	}
}

// cmdEditNetworkPolicy patches only the MachineNetworkPolicy fields an
// explicit flag was passed for, same fs.Visit convention as every other
// edit subcommand -- see editableVmNetworkPolicyFlags's own doc comment for
// exactly which spec.policy fields this covers.
func cmdEditNetworkPolicy(ctx context.Context, kc *kube.Client, name string, args []string) {
	fs := flag.NewFlagSet("edit networkpolicy", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	machineName := fs.String("machine-name", "", "new single-Machine target; takes precedence over spec.selector")
	var selector stringSliceFlag
	fs.Var(&selector, "selector", "new label key=value selector (repeatable; replaces the entire existing selector when passed)")
	policyFn := editableVmNetworkPolicyFlags(fs)
	_ = fs.Parse(args)
	spec := map[string]any{}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "machine-name":
			spec["machineName"] = *machineName
		case "selector":
			selectorMap, err := parseKeyValues(selector)
			if err != nil {
				fatal(err)
			}
			spec["selector"] = selectorMap
		}
	})
	if policy := policyFn(); policy != nil {
		spec["policy"] = policy
	}
	if len(spec) == 0 {
		fatal(fmt.Errorf("nothing to edit: pass at least one of --machine-name, --selector, --allow-cidr, --deny-cidr, --allow-port, --default-allow, --audit-mode, --max-egress-mbps, or --max-egress-pps"))
	}
	if err := kc.PatchMachineNetworkPolicy(ctx, *ns, name, map[string]any{"spec": spec}); err != nil {
		fatal(err)
	}
	fmt.Printf("networkpolicy/%s updated\n", name)
}

// cmdEditSecurityGroup is cmdEditNetworkPolicy's exact counterpart for
// NetworkSecurityGroup, plus its three own top-level spec fields
// (--group-label/--priority/--description) in place of
// --machine-name/--selector.
func cmdEditSecurityGroup(ctx context.Context, kc *kube.Client, name string, args []string) {
	fs := flag.NewFlagSet("edit securitygroup", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	var groupLabels stringSliceFlag
	fs.Var(&groupLabels, "group-label", "new key=value tags FluxVM matches group membership against (repeatable; replaces the entire existing list when passed)")
	priority := fs.Uint("priority", 0, "new priority (lower wins on a rate/deny tie against another group)")
	description := fs.String("description", "", "new human-readable note")
	policyFn := editableVmNetworkPolicyFlags(fs)
	_ = fs.Parse(args)
	spec := map[string]any{}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "group-label":
			spec["labels"] = []string(groupLabels)
		case "priority":
			spec["priority"] = *priority
		case "description":
			spec["description"] = *description
		}
	})
	if policy := policyFn(); policy != nil {
		spec["policy"] = policy
	}
	if len(spec) == 0 {
		fatal(fmt.Errorf("nothing to edit: pass at least one of --group-label, --priority, --description, --allow-cidr, --deny-cidr, --allow-port, --default-allow, --audit-mode, --max-egress-mbps, or --max-egress-pps"))
	}
	if err := kc.PatchNetworkSecurityGroup(ctx, *ns, name, map[string]any{"spec": spec}); err != nil {
		fatal(err)
	}
	fmt.Printf("securitygroup/%s updated\n", name)
}

// cmdDelete deletes either a single named resource (`delete [RESOURCE]
// NAME`, the original and still-default calling convention -- unchanged
// below) or, once --selector appears anywhere in args, every resource of
// an explicitly named RESOURCE kind whose labels satisfy every given
// key=value pair (`delete RESOURCE --selector k=v [--selector k2=v2]
// [--dry-run]`). Every other kaironctl verb addresses exactly one named
// object; cleaning up, say, every Machine a finished load test left
// behind meant either a shell loop around `kaironctl get -o` output or
// reaching for kubectl. hasFlag, not resourceKindAndName's own strict
// "1 or 2 positional args" arithmetic, is what tells the two calling
// conventions apart, since bulk mode has a KIND but never a NAME.
func cmdDelete(ctx context.Context, kc *kube.Client, args []string) {
	ns, args := nsFlag(args)
	if hasFlag(args, "selector") {
		cmdDeleteSelector(ctx, kc, ns, args)
		return
	}
	kind, name := resourceKindAndName("delete", args)
	canonical, err := deleteByKindName(ctx, kc, ns, kind, name)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("%s/%s deleted\n", canonical, name)
}

// hasFlag reports whether args contains --name or --name=value anywhere
// in the slice, used ahead of any flag.FlagSet parsing purely to pick
// which of delete's two calling conventions applies -- a single strings
// scan, not a full flag parse, since at this point args may still start
// with a bare KIND that isn't a flag at all.
func hasFlag(args []string, name string) bool {
	prefix := "--" + name
	for _, a := range args {
		if a == prefix || strings.HasPrefix(a, prefix+"=") {
			return true
		}
	}
	return false
}

// canonicalKind maps every alias cmdGet/cmdDescribe/cmdDelete/cmdEdit
// already recognize for one resource kind down to that kind's single
// canonical name (e.g. "vm"/"vms"/"machine"/"machines" -> "machine") --
// factored out of the old inline delete switch so both deleteByKindName
// and cmdDeleteSelector's --dry-run listing print the exact same
// canonical name a real delete would have used, without duplicating the
// alias list a second time.
func canonicalKind(kind string) (string, error) {
	switch kind {
	case "machine", "machines", "vm", "vms":
		return "machine", nil
	case "migration", "migrations", "machinemigrations":
		return "migration", nil
	case "snapshot", "snapshots", "machinesnapshots":
		return "snapshot", nil
	case "restore", "restores", "machinesnapshotrestores":
		return "restore", nil
	case "quota", "quotas", "machinequotas":
		return "quota", nil
	case "budget", "budgets", "machinedisruptionbudgets":
		return "budget", nil
	case "machineset", "machinesets":
		return "machineset", nil
	case "instancetype", "instancetypes", "machineinstancetypes":
		return "instancetype", nil
	case "migrationpolicy", "migrationpolicies":
		return "migrationpolicy", nil
	case "snapshotschedule", "snapshotschedules", "machinesnapshotschedules":
		return "snapshotschedule", nil
	case "networkpolicy", "networkpolicies", "machinenetworkpolicies":
		return "networkpolicy", nil
	case "securitygroup", "securitygroups", "networksecuritygroups":
		return "securitygroup", nil
	default:
		return "", fmt.Errorf("unknown resource %q", kind)
	}
}

// deleteByKindName is cmdDelete's original single-object delete dispatch,
// factored out so cmdDeleteSelector can call it once per label-matched
// name below without duplicating the kind switch a second time.
func deleteByKindName(ctx context.Context, kc *kube.Client, ns, kind, name string) (canonical string, err error) {
	canonical, err = canonicalKind(kind)
	if err != nil {
		return "", err
	}
	switch canonical {
	case "machine":
		return canonical, kc.DeleteMachine(ctx, ns, name)
	case "migration":
		return canonical, kc.DeleteMachineMigration(ctx, ns, name)
	case "snapshot":
		return canonical, kc.DeleteMachineSnapshot(ctx, ns, name)
	case "restore":
		return canonical, kc.DeleteMachineSnapshotRestore(ctx, ns, name)
	case "quota":
		return canonical, kc.DeleteMachineQuota(ctx, ns, name)
	case "budget":
		return canonical, kc.DeleteMachineDisruptionBudget(ctx, ns, name)
	case "machineset":
		return canonical, kc.DeleteMachineSet(ctx, ns, name)
	case "instancetype":
		return canonical, kc.DeleteMachineInstanceType(ctx, ns, name)
	case "migrationpolicy":
		return canonical, kc.DeleteMigrationPolicy(ctx, ns, name)
	case "snapshotschedule":
		return canonical, kc.DeleteMachineSnapshotSchedule(ctx, ns, name)
	case "networkpolicy":
		return canonical, kc.DeleteMachineNetworkPolicy(ctx, ns, name)
	case "securitygroup":
		return canonical, kc.DeleteNetworkSecurityGroup(ctx, ns, name)
	default:
		return "", fmt.Errorf("unknown resource %q", kind) // unreachable: canonicalKind above already rejected anything else
	}
}

// appendIfMatch appends name to names when labels satisfies every
// key=value pair in selector, per model.LabelsMatch -- the same
// "matches" definition MachineDisruptionBudget.Spec.Selector,
// MachineNetworkPolicy.Spec.Selector and TopologySpreadConstraint's own
// LabelSelector already use, including LabelsMatch's own "an empty
// selector matches nothing, never everything" rule. cmdDeleteSelector
// never actually reaches matchingNames with an empty selector (it
// refuses first), but matchingNames stays correct on its own terms
// either way.
func appendIfMatch(names []string, name string, labels, selector map[string]string) []string {
	if model.LabelsMatch(labels, selector) {
		return append(names, name)
	}
	return names
}

// matchingNames lists every existing resource of kind in ns and returns
// the metadata.name of each one whose labels satisfy selector (see
// appendIfMatch). One case per kind, mirroring cmdGet's own per-kind
// switch, since each kind has its own List*Namespace call and item type.
func matchingNames(ctx context.Context, kc *kube.Client, ns, kind string, selector map[string]string) ([]string, error) {
	var names []string
	switch kind {
	case "machine", "machines", "vm", "vms":
		items, err := kc.ListMachinesNamespace(ctx, ns)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			names = appendIfMatch(names, it.Metadata.Name, it.Metadata.Labels, selector)
		}
	case "migration", "migrations", "machinemigrations":
		items, err := kc.ListMachineMigrationsNamespace(ctx, ns)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			names = appendIfMatch(names, it.Metadata.Name, it.Metadata.Labels, selector)
		}
	case "snapshot", "snapshots", "machinesnapshots":
		items, err := kc.ListMachineSnapshotsNamespace(ctx, ns)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			names = appendIfMatch(names, it.Metadata.Name, it.Metadata.Labels, selector)
		}
	case "restore", "restores", "machinesnapshotrestores":
		items, err := kc.ListMachineSnapshotRestoresNamespace(ctx, ns)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			names = appendIfMatch(names, it.Metadata.Name, it.Metadata.Labels, selector)
		}
	case "quota", "quotas", "machinequotas":
		items, err := kc.ListMachineQuotasNamespace(ctx, ns)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			names = appendIfMatch(names, it.Metadata.Name, it.Metadata.Labels, selector)
		}
	case "budget", "budgets", "machinedisruptionbudgets":
		items, err := kc.ListMachineDisruptionBudgetsNamespace(ctx, ns)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			names = appendIfMatch(names, it.Metadata.Name, it.Metadata.Labels, selector)
		}
	case "machineset", "machinesets":
		items, err := kc.ListMachineSetsNamespace(ctx, ns)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			names = appendIfMatch(names, it.Metadata.Name, it.Metadata.Labels, selector)
		}
	case "instancetype", "instancetypes", "machineinstancetypes":
		items, err := kc.ListMachineInstanceTypesNamespace(ctx, ns)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			names = appendIfMatch(names, it.Metadata.Name, it.Metadata.Labels, selector)
		}
	case "migrationpolicy", "migrationpolicies":
		items, err := kc.ListMigrationPoliciesNamespace(ctx, ns)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			names = appendIfMatch(names, it.Metadata.Name, it.Metadata.Labels, selector)
		}
	case "snapshotschedule", "snapshotschedules", "machinesnapshotschedules":
		items, err := kc.ListMachineSnapshotSchedulesNamespace(ctx, ns)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			names = appendIfMatch(names, it.Metadata.Name, it.Metadata.Labels, selector)
		}
	case "networkpolicy", "networkpolicies", "machinenetworkpolicies":
		items, err := kc.ListMachineNetworkPoliciesNamespace(ctx, ns)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			names = appendIfMatch(names, it.Metadata.Name, it.Metadata.Labels, selector)
		}
	case "securitygroup", "securitygroups", "networksecuritygroups":
		items, err := kc.ListNetworkSecurityGroupsNamespace(ctx, ns)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			names = appendIfMatch(names, it.Metadata.Name, it.Metadata.Labels, selector)
		}
	default:
		return nil, fmt.Errorf("unknown resource %q", kind)
	}
	return names, nil
}

// cmdDeleteSelector implements `delete RESOURCE --selector k=v [--selector
// k2=v2] [--dry-run]`: RESOURCE is required and explicit (no "1 arg
// defaults to machine" convention here, unlike resourceKindAndName --
// bulk deletion is exactly the place a wrong default is most dangerous),
// and --selector is required and must resolve to a non-empty map. That
// last rule is deliberate and load-bearing: model.LabelsMatch already
// treats an empty selector as matching nothing, so without this check a
// mistyped `--selector` (or one that parses to an empty map for some
// other reason) would silently no-op instead of surfacing the mistake --
// `--dry-run` is not itself the safety mechanism, both together are. Every
// deletion happens one object at a time via the same deleteByKindName
// cmdDelete's single-object path already uses, so a bulk delete leaves
// exactly the same audit trail (individual DELETE calls, individual
// finalizer/reconcile handling) as running `delete NAME` in a loop by
// hand -- there is no separate bulk-delete API call to reason about.
func cmdDeleteSelector(ctx context.Context, kc *kube.Client, ns string, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl delete RESOURCE --selector k=v [--selector k2=v2] [--dry-run]"))
	}
	kind := strings.ToLower(args[0])
	canonical, err := canonicalKind(kind)
	if err != nil {
		fatal(err)
	}
	fs := flag.NewFlagSet("delete", flag.ExitOnError)
	var selector stringSliceFlag
	fs.Var(&selector, "selector", "label key=value every matching resource must carry (repeatable -- every pair must match; required and must be non-empty)")
	dryRun := fs.Bool("dry-run", false, "print what would be deleted without deleting anything")
	_ = fs.Parse(args[1:])
	sel, err := parseKeyValues(selector)
	if err != nil {
		fatal(err)
	}
	if len(sel) == 0 {
		fatal(fmt.Errorf("--selector is required and must be non-empty for bulk delete (usage: kaironctl delete %s --selector k=v) -- an empty selector matches nothing, by design, rather than risk being misread as \"everything\"", kind))
	}
	names, err := matchingNames(ctx, kc, ns, kind, sel)
	if err != nil {
		fatal(err)
	}
	if len(names) == 0 {
		fmt.Println("no resources matched selector; nothing to delete")
		return
	}
	sort.Strings(names) // deterministic order for a repeatable dry-run/real-run diff
	for _, name := range names {
		if *dryRun {
			fmt.Printf("%s/%s (dry-run, not deleted)\n", canonical, name)
			continue
		}
		if _, err := deleteByKindName(ctx, kc, ns, kind, name); err != nil {
			fatal(fmt.Errorf("deleting %s/%s: %w", canonical, name, err))
		}
		fmt.Printf("%s/%s deleted\n", canonical, name)
	}
}

// cmdFence is the operator-attested recovery action for a Machine whose
// node kairon-controller has detected as unreachable (see
// internal/controller/fencing.go's detectUnreachableNodes) -- the same
// "park it, wait for an attested human decision" shape as `recover` uses
// for a NeedsRecovery migration, applied to a dead node instead of an
// ambiguous migration commit. Kairon cannot itself confirm a node is
// truly gone rather than just unreachable, so this refuses to run at all
// unless status already shows NodeUnreachable=True, and --reason is
// required as the operator's own attestation (e.g. "confirmed powered off
// via iDRAC at 14:02" or "node deleted from the cluster") -- if the old
// node comes back while its FluxVM runtime is still actually running,
// fencing it anyway risks the exact split-brain NeedsRecovery exists to
// prevent for migrations. Clears spec.nodeName (picked up by the normal
// scheduling loop on the next tick, same as a brand-new Machine) and every
// status field tied to the old runtime, so the new node treats this as a
// fresh create rather than trying to adopt something that may not exist.
func cmdFence(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl fence MACHINE --reason REASON"))
	}
	name := args[0]
	fs := flag.NewFlagSet("fence", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	reason := fs.String("reason", "", "your out-of-band evidence the node is truly gone, not just unreachable (required)")
	livenessLeaseNamespace := fs.String("liveness-lease-namespace", "", "namespace holding kairon-node's own liveness Leases (see node.livenessLease.enabled); when set, fence cross-checks the fenced node's own Lease against ConditionNodeUnreachable and refuses if kairon-node itself still looks alive there -- empty (the default) skips this extra check entirely, exactly kaironctl fence's behavior before it existed")
	forceIgnoreLiveness := fs.Bool("force-ignore-liveness", false, "proceed even if the liveness lease cross-check above would refuse")
	_ = fs.Parse(args[1:])
	if *reason == "" {
		fatal(fmt.Errorf("--reason is required: state the out-of-band evidence you have that the node is truly gone"))
	}

	m, err := kc.GetMachine(ctx, *ns, name)
	if err != nil {
		fatal(fmt.Errorf("get machine %s/%s: %w", *ns, name, err))
	}
	cond, found := findMachineCondition(m.Status.Conditions, model.ConditionNodeUnreachable)
	if !found || cond.Status != "True" {
		fatal(fmt.Errorf("machine %s/%s does not currently have %s=True -- nothing to fence (its node looks Ready to kairon-controller)", *ns, name, model.ConditionNodeUnreachable))
	}
	fencedNode := m.Spec.NodeName
	// ConditionNodeUnreachable is purely a kubelet Node-Ready-derived
	// signal (internal/controller/fencing.go) -- kubelet can flap
	// NotReady (a brief network blip, an apiserver hiccup) while
	// kairon-node's own reconcile loop keeps running fine on that node.
	// When an operator has opted a node into node.livenessLease.enabled,
	// cross-check kairon-node's own independent liveness signal before
	// trusting Node Ready alone -- see internal/nodeliveness's own doc
	// comment for the full reasoning. Skipped entirely (falls back to
	// today's exact behavior) when --liveness-lease-namespace isn't set,
	// or when no Lease is found for this node (liveness leases disabled
	// on it, or it never came up) -- this is an additional safety gate on
	// top of the existing check, never a replacement for it.
	if *livenessLeaseNamespace != "" {
		lease, err := kc.GetLease(ctx, *livenessLeaseNamespace, nodeliveness.LeaseName(fencedNode))
		switch {
		case kube.IsNotFound(err):
			// No liveness signal recorded for this node -- proceed exactly
			// as before this check existed.
		case err != nil:
			fmt.Fprintf(os.Stderr, "warning: could not read kairon-node's liveness lease for node %q (%v) -- proceeding without this extra check\n", fencedNode, err)
		default:
			if nodeliveness.IsFresh(lease, 0) && !*forceIgnoreLiveness {
				fatal(fmt.Errorf("refusing to fence: kairon-node on node %q renewed its own liveness lease recently, despite %s=True -- kairon-node's reconcile loop may still be alive and actively managing this Machine, and fencing now risks abandoning a still-live VM instead of a truly dead one; pass --force-ignore-liveness if you are certain this is safe", fencedNode, model.ConditionNodeUnreachable))
			}
		}
	}
	fmt.Printf("fencing machine/%s off node %q (kairon-controller's last-observed reason: %s)\n", name, fencedNode, cond.Message)

	if err := kc.PatchMachine(ctx, *ns, name, map[string]any{"spec": map[string]any{"nodeName": ""}}); err != nil {
		fatal(fmt.Errorf("clear spec.nodeName on %s/%s: %w", *ns, name, err))
	}
	status := m.Status
	status.Phase = ""
	status.NodeName = ""
	status.RuntimeID = ""
	status.GuestIP = ""
	status.GuestIPs = nil
	status.Network = nil
	status.AppliedVCPUs = 0
	status.AppliedMemoryMiB = 0
	status.Conditions = setMachineCondition(status.Conditions, model.Condition{
		Type: model.ConditionFenced, Status: "True", Reason: "OperatorAttested",
		Message: fmt.Sprintf("fenced off node %q by an operator: %s", fencedNode, *reason), LastTransitionTime: time.Now().UTC(),
	})
	if err := kc.PatchMachineStatus(ctx, *ns, name, status); err != nil {
		fatal(fmt.Errorf("clear runtime status on %s/%s: %w", *ns, name, err))
	}
	fmt.Printf("machine/%s: spec.nodeName cleared; will be rescheduled onto a different node on kairon-controller's next reconcile tick\n", name)
}

func findMachineCondition(conditions []model.Condition, condType string) (model.Condition, bool) {
	for _, c := range conditions {
		if c.Type == condType {
			return c, true
		}
	}
	return model.Condition{}, false
}

func setMachineCondition(conditions []model.Condition, cond model.Condition) []model.Condition {
	out := make([]model.Condition, 0, len(conditions)+1)
	replaced := false
	for _, c := range conditions {
		if c.Type == cond.Type {
			out = append(out, cond)
			replaced = true
			continue
		}
		out = append(out, c)
	}
	if !replaced {
		out = append(out, cond)
	}
	return out
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

func cmdEvacuate(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl evacuate NODE [--strategy cold|auto] [--wait] [--timeout 15m] [--poll-interval 10s]"))
	}
	node := args[0]
	fs := flag.NewFlagSet("evacuate", flag.ExitOnError)
	strategy := fs.String("strategy", "cold", "cold|auto; use migrate --strategy live for the secure peer handshake")
	wait := fs.Bool("wait", false, "keep retrying -- respecting MachineDisruptionBudget and skipping any Machine already mid-migration -- until every Machine has left NODE or --timeout elapses, instead of the default single pass that leaves budget-blocked Machines behind for good. Matches kubectl drain's own retry model; this is the closest thing to an automatic node-drain Kairon has, deliberately still operator-invoked rather than triggered silently by e.g. node.spec.unschedulable, the same 'operator attests, Kairon then follows through' shape as kaironctl fence/recover.")
	timeout := fs.Duration("timeout", 15*time.Minute, "with --wait, how long to keep retrying before giving up")
	pollInterval := fs.Duration("poll-interval", 10*time.Second, "with --wait, how often to re-check and retry")
	_ = fs.Parse(args[1:])
	if *strategy != "cold" && *strategy != "auto" {
		fatal(fmt.Errorf("evacuate supports --strategy cold|auto; use migrate for explicit live migration"))
	}

	created, skipped, remaining, err := evacuatePass(ctx, kc, node, *strategy)
	if err != nil {
		fatal(err)
	}
	if !*wait {
		fmt.Printf("evacuation queued: %d machine(s) from %s", created, node)
		if skipped > 0 {
			fmt.Printf(", %d skipped (MachineDisruptionBudget or already mid-migration)\n", skipped)
			os.Exit(1)
		}
		fmt.Println()
		return
	}

	deadline := time.Now().Add(*timeout)
	for remaining > 0 {
		if time.Now().After(deadline) {
			fatal(fmt.Errorf("evacuate --wait timed out after %s: %d machine(s) still on %s", *timeout, remaining, node))
		}
		select {
		case <-ctx.Done():
			fatal(ctx.Err())
		case <-time.After(*pollInterval):
		}
		createdThisPass, _, r, err := evacuatePass(ctx, kc, node, *strategy)
		if err != nil {
			fatal(err)
		}
		created += createdThisPass
		remaining = r
		fmt.Printf("%s: %d machine(s) still on %s (%d newly queued this pass, %d total queued)\n", time.Now().UTC().Format(time.RFC3339), remaining, node, createdThisPass, created)
	}
	fmt.Printf("evacuation of %s complete: %d machine(s) migrated\n", node, created)
}

// evacuatePass makes one pass over every Machine currently on node: creates
// a MachineMigration for each one MachineDisruptionBudget allows and that
// isn't already mid-migration (a Machine can only sensibly have one active
// MachineMigration at a time -- skip rather than create a conflicting
// second one; nothing else in this codebase already guarded against that,
// so this closes a real latent gap, not just something --wait needed).
// remaining is how many Machines are still assigned to node afterward
// (including ones just queued this pass, since they haven't actually left
// yet) -- the number cmdEvacuate's --wait loop polls until it reaches 0.
func evacuatePass(ctx context.Context, kc *kube.Client, node, strategy string) (created, skipped, remaining int, err error) {
	machines, err := kc.ListMachines(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	migrations, err := kc.ListMachineMigrations(ctx)
	if err != nil && !kube.IsNotFound(err) {
		return 0, 0, 0, err
	}
	budgets, err := kc.ListMachineDisruptionBudgets(ctx)
	if err != nil && !kube.IsNotFound(err) {
		return 0, 0, 0, err
	}
	states, err := controller.LoadBudgetStates(budgets, machines, migrations)
	if err != nil {
		return 0, 0, 0, err
	}
	inFlight := map[string]bool{}
	for _, mig := range migrations {
		if migrationStillPending(mig.Status.Phase) {
			inFlight[mig.Namespace()+"/"+mig.Spec.MachineName] = true
		}
	}

	stamp := time.Now().UTC().Format("20060102-150405")
	for _, machine := range machines {
		if machine.Spec.NodeName != node || machine.Metadata.DeletionTimestamp != nil {
			continue
		}
		remaining++
		key := machine.Namespace() + "/" + machine.Metadata.Name
		if inFlight[key] {
			continue // already migrating -- wait for it, don't create a second one
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
			Spec:     model.MachineMigrationSpec{MachineName: machine.Metadata.Name, Strategy: strategy},
		}
		if _, err := kc.CreateMachineMigration(ctx, machine.Namespace(), migration); err != nil {
			return created, skipped, remaining, fmt.Errorf("create migration for %s/%s: %w", machine.Namespace(), machine.Metadata.Name, err)
		}
		fmt.Printf("machinemigration/%s created for %s/%s\n", name, machine.Namespace(), machine.Metadata.Name)
		created++
	}
	return created, skipped, remaining, nil
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

// cmdCancelMigration is a thin convenience layer over spec.cancel, matching
// cmdRecover's own "print current state, then patch spec, then let the
// source node's agent actually apply it" shape. Only meaningful while the
// migration is still Starting or Running (before the destination has
// committed) -- refuses locally rather than silently patching a migration
// that's already past the point where cancelling is safe, so an operator
// gets an immediate, clear answer instead of a spec.cancel that the agent
// will just ignore as a documented no-op.
func cmdCancelMigration(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl cancel-migration MIGRATION [-n NAMESPACE]"))
	}
	name := args[0]
	ns, _ := nsFlag(args[1:])

	current, err := kc.GetMachineMigration(ctx, ns, name)
	if err != nil {
		fatal(fmt.Errorf("get machinemigration %s/%s: %w", ns, name, err))
	}
	if current.Status.Phase != "Starting" && current.Status.Phase != "Running" {
		fatal(fmt.Errorf("machinemigration %s/%s is in phase %q; cancel only applies to a live migration still in Starting or Running, before the destination has committed", ns, name, current.Status.Phase))
	}
	if current.Status.EffectiveStrategy != "live" {
		fatal(fmt.Errorf("machinemigration %s/%s is a %q-strategy migration; cancel only applies to live migrations", ns, name, current.Status.EffectiveStrategy))
	}
	if current.Spec.Cancel {
		fmt.Printf("machinemigration/%s: cancel already requested; waiting for the source node's agent to apply it\n", name)
		return
	}
	patch := map[string]any{"spec": map[string]any{"cancel": true}}
	if err := kc.PatchMachineMigration(ctx, ns, name, patch); err != nil {
		fatal(fmt.Errorf("patch machinemigration %s/%s: %w", ns, name, err))
	}
	fmt.Printf("machinemigration/%s: cancel requested; the source node's agent will abort the in-flight transfer and mark it Cancelled on its next reconcile\n", name)
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
	fmt.Fprintln(os.Stderr, "kaironctl get [machines|migrations|snapshots|restores|quotas|budgets|machinesets|instancetypes|migrationpolicies|snapshotschedules|networkpolicies|securitygroups|nodes] [--selector k=v] | describe [RESOURCE] NAME | create [machineset|instancetype|migrationpolicy|snapshotschedule|quota|budget|networkpolicy|securitygroup] NAME | delete [RESOURCE] NAME | delete RESOURCE --selector k=v [--dry-run] | scale machineset NAME --replicas N | edit [machine|migrationpolicy|snapshotschedule|quota|budget|networkpolicy|securitygroup] NAME | trigger snapshotschedule NAME | top [machines|nodes] [--selector k=v] | start | stop | pause | resume | halt | migrate | evacuate | recover | cancel-migration | fence | snapshot | restore | version")
}
func fatal(err error) { fmt.Fprintln(os.Stderr, "error:", err); os.Exit(1) }
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
