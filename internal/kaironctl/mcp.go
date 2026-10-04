// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kaironctl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zyvorai/kairon/internal/ebpfedge"
	"github.com/zyvorai/kairon/internal/kaironctl/style"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/mcp"
	"github.com/zyvorai/kairon/internal/model"
)

const mcpCallTimeout = 30 * time.Second

func newMCPCmd(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Model Context Protocol server for AI agents",
	}
	var allowWrite bool
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Serve Kairon tools over MCP stdio (for Hermes Agent and other MCP clients)",
		Long: `Speaks MCP (JSON-RPC 2.0) on stdin/stdout. Read tools list and inspect
Machines, network policies and VM-edge observability. With --allow-write,
tools that set power state, create snapshots and run packet captures are
also offered.

Needs KAIRON_KUBE_URL/KAIRON_KUBE_TOKEN (or in-cluster credentials) for
Machines, and KAIRON_UI_URL/KAIRON_UI_TOKEN for network observability and
capture. Logs go to stderr; stdout carries only protocol messages.`,
		Example: `  kaironctl mcp serve
  kaironctl mcp serve --allow-write`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			style.SetQuiet(true)
			s := mcp.NewServer("kairon", opts.Version, allowWrite)
			s.Add(kaironTools(opts, kube.FromEnvironment)...)
			return s.Serve(cmd.Context(), os.Stdin, os.Stdout)
		},
	}
	serve.Flags().BoolVar(&allowWrite, "allow-write", false, "offer tools that change state (power, snapshot, capture)")
	cmd.AddCommand(serve)
	return cmd
}

var networkKinds = []string{"network-effective", "network-stats", "network-flows", "network-drops", "network-drop-reasons", "network-capture"}

type machineRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

func (r machineRef) ns(def string) string {
	if r.Namespace != "" {
		return r.Namespace
	}
	if def != "" {
		return def
	}
	return "default"
}

func decodeArgs(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

type machineSummary struct {
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	Phase      string `json:"phase,omitempty"`
	PowerState string `json:"powerState,omitempty"`
	Node       string `json:"node,omitempty"`
	GuestIP    string `json:"guestIP,omitempty"`
	CPU        string `json:"cpu,omitempty"`
	Memory     string `json:"memory,omitempty"`
	Message    string `json:"message,omitempty"`
}

func summarize(m model.Machine) machineSummary {
	return machineSummary{
		Namespace:  m.Namespace(),
		Name:       m.Metadata.Name,
		Phase:      m.Status.Phase,
		PowerState: m.Spec.PowerState,
		Node:       m.Status.NodeName,
		GuestIP:    m.Status.GuestIP,
		CPU:        m.Spec.Resources.CPU,
		Memory:     m.Spec.Resources.Memory,
		Message:    m.Status.Message,
	}
}

// kaironTools builds the tool set; newKube is injected so tests can
// point it at a fake API server.
func kaironTools(opts *Options, newKube func() (*kube.Client, error)) []mcp.Tool {
	nsDefault := opts.Namespace
	nsProp := mcp.String("Kubernetes namespace; defaults to " + nsOrDefault(nsDefault))
	refSchema := func(extra map[string]any, required ...string) map[string]any {
		props := map[string]any{"namespace": nsProp, "name": mcp.String("Machine name")}
		for k, v := range extra {
			props[k] = v
		}
		return mcp.Object(props, append([]string{"name"}, required...)...)
	}
	withKube := func(ctx context.Context, timeout time.Duration, fn func(context.Context, *kube.Client) (string, error)) (string, error) {
		kc, err := newKube()
		if err != nil {
			return "", err
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return fn(ctx, kc)
	}

	return []mcp.Tool{
		{
			Name:        "list_machines",
			Description: "List Kairon Machines (VMs) with phase, power state, node, guest IP and size. Omit namespace to list all namespaces.",
			Schema:      mcp.Object(map[string]any{"namespace": mcp.String("Kubernetes namespace; omit for all")}),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Namespace string `json:"namespace"`
				}
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				return withKube(ctx, mcpCallTimeout, func(ctx context.Context, kc *kube.Client) (string, error) {
					var items []model.Machine
					var err error
					if a.Namespace != "" {
						items, err = kc.ListMachinesNamespace(ctx, a.Namespace)
					} else {
						items, err = kc.ListMachines(ctx)
					}
					if err != nil {
						return "", err
					}
					out := make([]machineSummary, 0, len(items))
					for _, m := range items {
						out = append(out, summarize(m))
					}
					return mcp.JSON(map[string]any{"machines": out})
				})
			},
		},
		{
			Name:        "get_machine",
			Description: "Get one Machine's full spec and status, including network, edge and conditions.",
			Schema:      refSchema(nil),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a machineRef
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				return withKube(ctx, mcpCallTimeout, func(ctx context.Context, kc *kube.Client) (string, error) {
					m, err := kc.GetMachine(ctx, a.ns(nsDefault), a.Name)
					if err != nil {
						return "", err
					}
					return mcp.JSON(map[string]any{"metadata": m.Metadata, "spec": m.Spec, "status": m.Status})
				})
			},
		},
		{
			Name:        "list_network_policies",
			Description: "List MachineNetworkPolicies (selector, allow lists, rate limits, status). Omit namespace for all.",
			Schema:      mcp.Object(map[string]any{"namespace": mcp.String("Kubernetes namespace; omit for all")}),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Namespace string `json:"namespace"`
				}
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				return withKube(ctx, mcpCallTimeout, func(ctx context.Context, kc *kube.Client) (string, error) {
					items, err := kc.ListMachineNetworkPolicies(ctx)
					if err != nil {
						return "", err
					}
					out := []model.MachineNetworkPolicy{}
					for _, p := range items {
						if a.Namespace == "" || p.Metadata.Namespace == a.Namespace {
							out = append(out, p)
						}
					}
					return mcp.JSON(map[string]any{"policies": out})
				})
			},
		},
		{
			Name: "machine_network",
			Description: "Live VM-edge network data for a Machine from FluxVM, via kairon-ui: " +
				"network-effective (merged policy), network-stats, network-flows, network-drops (attributed drops with reason and policy), " +
				"network-drop-reasons (kernel reasons) or network-capture (packet capture sessions).",
			Schema: refSchema(map[string]any{
				"kind":  mcp.String("which view", networkKinds...),
				"limit": mcp.Integer("max entries for flows and drops", 1, 1000),
			}, "kind"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					machineRef
					Kind  string `json:"kind"`
					Limit int    `json:"limit"`
				}
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				if !contains(networkKinds, a.Kind) {
					return "", fmt.Errorf("kind must be one of %s", strings.Join(networkKinds, ", "))
				}
				ctx, cancel := context.WithTimeout(ctx, mcpCallTimeout)
				defer cancel()
				body, err := fetchObservability(ctx, a.ns(nsDefault), a.Name, a.Kind, a.Limit)
				if err != nil {
					return "", err
				}
				var v any
				if json.Unmarshal(body, &v) == nil {
					return mcp.JSON(v)
				}
				return string(body), nil
			},
		},
		{
			Name:        "machine_edge_identity",
			Description: "The Machine's stable VM-edge identity (FNV-1a of namespace/name), as used in policy and conntrack. Computed locally.",
			Schema:      refSchema(nil),
			Call: func(_ context.Context, raw json.RawMessage) (string, error) {
				var a machineRef
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				ns := a.ns(nsDefault)
				return mcp.JSON(map[string]any{"namespace": ns, "name": a.Name, "identity": ebpfedge.StableIdentity(ns, a.Name)})
			},
		},
		{
			Name:        "set_power_state",
			Description: "Set a Machine's spec.powerState: Running (start/resume), Stopped, Paused or Halted. kairon-node applies it on its next tick.",
			Write:       true,
			Schema:      refSchema(map[string]any{"state": mcp.String("desired power state", "Running", "Stopped", "Paused", "Halted")}, "state"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					machineRef
					State string `json:"state"`
				}
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				if !contains([]string{"Running", "Stopped", "Paused", "Halted"}, a.State) {
					return "", fmt.Errorf("state must be Running, Stopped, Paused or Halted")
				}
				return withKube(ctx, mcpCallTimeout, func(ctx context.Context, kc *kube.Client) (string, error) {
					ns := a.ns(nsDefault)
					if err := setPowerState(ctx, kc, ns, a.Name, a.State); err != nil {
						return "", err
					}
					return fmt.Sprintf("machine %s/%s powerState set to %s", ns, a.Name, a.State), nil
				})
			},
		},
		{
			Name:        "create_snapshot",
			Description: "Create a MachineSnapshot of a Machine's volumes. The name is generated when omitted.",
			Write:       true,
			Schema: refSchema(map[string]any{
				"snapshotName": mcp.String("MachineSnapshot name (optional)"),
				"class":        mcp.String("VolumeSnapshotClass name (optional)"),
			}),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					machineRef
					SnapshotName string `json:"snapshotName"`
					Class        string `json:"class"`
				}
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				return withKube(ctx, mcpCallTimeout, func(ctx context.Context, kc *kube.Client) (string, error) {
					out, err := createSnapshot(ctx, kc, a.ns(nsDefault), a.Name, a.SnapshotName, a.Class)
					if err != nil {
						return "", err
					}
					return fmt.Sprintf("machinesnapshot %s/%s created", out.Metadata.Namespace, out.Metadata.Name), nil
				})
			},
		},
		{
			Name: "machine_volumes",
			Description: "A Machine's volumes: source (pvc, atlas-pvc, atlas-rbd), claim, size, Atlas provisioning phase, " +
				"backend id (pvc:NAME or rbd:POOL/IMAGE) and error message.",
			Schema: refSchema(nil),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a machineRef
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				return withKube(ctx, mcpCallTimeout, func(ctx context.Context, kc *kube.Client) (string, error) {
					m, err := kc.GetMachine(ctx, a.ns(nsDefault), a.Name)
					if err != nil {
						return "", err
					}
					return mcp.JSON(map[string]any{"volumes": machineVolumes(m)})
				})
			},
		},
		{
			Name: "snapshot_volume",
			Description: "Snapshot one of a Machine's volumes (a MachineSnapshot with spec.volumeNames). Atlas volumes use Atlas " +
				"snapshots, others CSI VolumeSnapshots. Check progress with get_machine_snapshot.",
			Write: true,
			Schema: refSchema(map[string]any{
				"volume":       mcp.String("spec.volumes name to snapshot"),
				"snapshotName": mcp.String("MachineSnapshot name (optional)"),
				"class":        mcp.String("VolumeSnapshotClass name for CSI volumes (optional)"),
			}, "volume"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					machineRef
					Volume       string `json:"volume"`
					SnapshotName string `json:"snapshotName"`
					Class        string `json:"class"`
				}
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				if a.Volume == "" {
					return "", fmt.Errorf("volume is required")
				}
				return withKube(ctx, mcpCallTimeout, func(ctx context.Context, kc *kube.Client) (string, error) {
					ns := a.ns(nsDefault)
					m, err := kc.GetMachine(ctx, ns, a.Name)
					if err != nil {
						return "", err
					}
					found := false
					for _, v := range m.Spec.Volumes {
						found = found || v.Name == a.Volume
					}
					if !found {
						return "", fmt.Errorf("machine %s/%s has no volume %q", ns, a.Name, a.Volume)
					}
					name := a.SnapshotName
					if name == "" {
						name = resourceName(a.Name + "-" + a.Volume + "-" + time.Now().UTC().Format("20060102-150405"))
					}
					out, err := createSnapshot(ctx, kc, ns, a.Name, name, a.Class, a.Volume)
					if err != nil {
						return "", err
					}
					return fmt.Sprintf("machinesnapshot %s/%s created for volume %s", out.Metadata.Namespace, out.Metadata.Name, a.Volume), nil
				})
			},
		},
		{
			Name:        "get_machine_snapshot",
			Description: "Get a MachineSnapshot's phase, readiness and per-volume snapshots (CSI VolumeSnapshot or Atlas snapshot id).",
			Schema: mcp.Object(map[string]any{
				"namespace": nsProp,
				"name":      mcp.String("MachineSnapshot name"),
			}, "name"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a machineRef
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				return withKube(ctx, mcpCallTimeout, func(ctx context.Context, kc *kube.Client) (string, error) {
					s, err := kc.GetMachineSnapshot(ctx, a.ns(nsDefault), a.Name)
					if err != nil {
						return "", err
					}
					return mcp.JSON(map[string]any{"metadata": s.Metadata, "spec": s.Spec, "status": s.Status})
				})
			},
		},
		{
			Name: "network_capture",
			Description: "Run a bounded tcpdump capture (1-30 s) on a Machine's VM edge via kairon-ui and FluxVM. " +
				"With output, waits for it and writes the pcap to that local path; otherwise returns the session token " +
				"(check progress with machine_network kind=network-capture).",
			Write: true,
			Schema: refSchema(map[string]any{
				"seconds": mcp.Integer("capture length in seconds", 1, 30),
				"filter":  mcp.String("optional tcpdump filter expression, e.g. \"udp port 53\""),
				"output":  mcp.String("optional local file path for the pcap"),
			}),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					machineRef
					Seconds int    `json:"seconds"`
					Filter  string `json:"filter"`
					Output  string `json:"output"`
				}
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				if a.Seconds == 0 {
					a.Seconds = 10
				}
				ns := a.ns(nsDefault)
				session, err := ebpfedge.NewCapture(ns, a.Name, a.Filter, a.Seconds, time.Now().UTC())
				if err != nil {
					return "", err
				}
				ctx, cancel := context.WithTimeout(ctx, time.Duration(a.Seconds)*time.Second+45*time.Second)
				defer cancel()
				posted, err := postCapture(ctx, ns, a.Name, session)
				if err != nil {
					return "", err
				}
				if !posted {
					return "", fmt.Errorf("set KAIRON_UI_URL to run captures")
				}
				if a.Output == "" {
					return mcp.JSON(map[string]any{"token": session.Token, "seconds": session.Seconds, "state": "running"})
				}
				n, err := downloadCapture(ctx, ns, a.Name, session, a.Output)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("capture %s done: wrote %d bytes to %s", session.Token, n, a.Output), nil
			},
		},
	}
}

func nsOrDefault(ns string) string {
	if ns == "" {
		return "default"
	}
	return ns
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
