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

	"github.com/zyvorai/kairon/internal/agentplane"
	"github.com/zyvorai/kairon/internal/mcp"
	"github.com/zyvorai/kairon/internal/model"
)

// agentPlaneTools are pure. They do not call the apiserver and they
// never apply. Write-gated audit_record is the only one that records.
func agentPlaneTools() []mcp.Tool {
	return []mcp.Tool{
		{
			Name:        "compile_network_policy",
			Description: "Compile a strict egress allowlist into a MachineNetworkPolicy. Does not apply. Rejects empty allowlists and bare wildcards.",
			Schema:      mcp.Object(map[string]any{"intent": map[string]any{"type": "object", "description": "PolicyIntent JSON"}}, "intent"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Intent agentplane.PolicyIntent `json:"intent"`
				}
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				if err := callerTenant(a.Intent.Tenant); err != nil {
					return "", err
				}
				pol, err := agentplane.CompilePolicy(a.Intent)
				if err != nil {
					return "", err
				}
				return mcp.JSON(map[string]any{"apply": false, "policy": pol})
			},
		},
		{
			Name:        "explain_drops",
			Description: "Explain attributed eBPF drops and propose a policy change. Apply is always false.",
			Schema:      mcp.Object(map[string]any{"drops": map[string]any{"type": "array", "description": "Drop samples"}}, "drops"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Drops []agentplane.Drop `json:"drops"`
				}
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				return mcp.JSON(map[string]any{"hypotheses": agentplane.ExplainDrops(a.Drops)})
			},
		},
		{
			Name:        "explain_pending",
			Description: "Explain why a Machine is pending from scheduler reason strings. Does not reschedule.",
			Schema:      mcp.Object(map[string]any{"machine": mcp.String("Machine name"), "nodes": map[string]any{"type": "array"}}, "machine"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Machine string               `json:"machine"`
					Nodes   []agentplane.NodeFit `json:"nodes"`
				}
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				return mcp.JSON(agentplane.ExplainPending(a.Machine, a.Nodes))
			},
		},
		{
			Name:        "detect_edge_anomalies",
			Description: "Rule-based beacon, DNS-tunnel shape, SNI spread and deny-burst findings. Apply is always false.",
			Schema:      mcp.Object(map[string]any{"flows": map[string]any{"type": "array"}, "drops": map[string]any{"type": "array"}}),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Flows []agentplane.Flow `json:"flows"`
					Drops []agentplane.Drop `json:"drops"`
				}
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				return mcp.JSON(map[string]any{"findings": agentplane.Detect(a.Flows, a.Drops)})
			},
		},
		{
			Name:        "validate_agent_claim",
			Description: "Validate a sealed agent claim: tenant, TTL, egress allowlist, hypervisor, no control-plane tools for the guest.",
			Schema:      mcp.Object(map[string]any{"claim": map[string]any{"type": "object"}}, "claim"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Claim agentplane.ClaimRequest `json:"claim"`
				}
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				if err := callerTenant(a.Claim.Tenant); err != nil {
					return "", err
				}
				if err := agentplane.ValidateClaim(a.Claim); err != nil {
					return "", err
				}
				return mcp.JSON(map[string]any{"ok": true, "apply": false})
			},
		},
		{
			Name:        "propose_boot_repair",
			Description: "Propose an offline boot repair from GuestKit-style findings. Does not modify a disk.",
			Schema:      mcp.Object(map[string]any{"findings": map[string]any{"type": "array"}}, "findings"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Findings []agentplane.BootFinding `json:"findings"`
				}
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				return mcp.JSON(map[string]any{"steps": agentplane.ProposeRepair(a.Findings), "apply": false})
			},
		},
		{
			Name:        "migration_claim",
			Description: "Check a lab matrix report against the v0.7 cases (cold, live, live-ebpf, source-failure, controller-failover).",
			Schema:      mcp.Object(map[string]any{"cases": map[string]any{"type": "array"}}, "cases"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Cases []agentplane.MatrixCase `json:"cases"`
				}
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				err := agentplane.MigrationClaim(a.Cases)
				return mcp.JSON(map[string]any{"green": err == nil, "error": errString(err)})
			},
		},
		{
			Name:        "step_agent_claim",
			Description: "Decide bind, hold, wait or expire for one MachineClaim against warm Machines. Does not write.",
			Schema:      mcp.Object(map[string]any{"claim": map[string]any{"type": "object"}, "warm": map[string]any{"type": "array"}}, "claim"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Now   time.Time                `json:"now"`
					Claim model.MachineClaim       `json:"claim"`
					Warm  []agentplane.WarmMachine `json:"warm"`
				}
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				if a.Now.IsZero() {
					a.Now = time.Now().UTC()
				}
				decision, err := agentplane.StepClaim(a.Now, a.Claim, a.Warm)
				if err != nil {
					return "", err
				}
				return mcp.JSON(decision)
			},
		},
		{
			Name:        "anomaly_events",
			Description: "Turn edge findings into Warning events. Does not emit them.",
			Schema:      mcp.Object(map[string]any{"machine": mcp.String("Machine name"), "flows": map[string]any{"type": "array"}, "drops": map[string]any{"type": "array"}}, "machine"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Machine string            `json:"machine"`
					Flows   []agentplane.Flow `json:"flows"`
					Drops   []agentplane.Drop `json:"drops"`
				}
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				findings := agentplane.Detect(a.Flows, a.Drops)
				return mcp.JSON(map[string]any{"findings": findings, "events": agentplane.EventsFromFindings(a.Machine, findings), "apply": false})
			},
		},
		{
			Name:        "project_cpu_label",
			Description: "Project kairon.zyvor.dev/pinnable-cpus from cpuset text. Does not label the node.",
			Schema:      mcp.Object(map[string]any{"node": mcp.String("node name"), "effective": mcp.String("cpuset.cpus.effective"), "reserved": mcp.String("reserved cpus")}, "effective"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a agentplane.NodeCPUReport
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				up, err := agentplane.CPULabelUpdate(a)
				if err != nil {
					return "", err
				}
				return mcp.JSON(up)
			},
		},
		{
			Name:        "project_confidential",
			Description: "Project sealed/not-sealed from a node attestation report. Does not schedule.",
			Schema:      mcp.Object(map[string]any{"requested": mcp.String("sev-snp or tdx"), "node": map[string]any{"type": "object"}}, "requested"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Requested string                 `json:"requested"`
					Node      agentplane.Attestation `json:"node"`
				}
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				return mcp.JSON(agentplane.ProjectConfidential(a.Requested, a.Node))
			},
		},
		{
			Name:        "bind_gateway",
			Description: "Build a Gateway binding from port forwards. Does not create the Gateway.",
			Schema:      mcp.Object(map[string]any{"name": mcp.String("gateway name"), "forwards": map[string]any{"type": "array"}}, "name", "forwards"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Name     string                   `json:"name"`
					Forwards []agentplane.PortForward `json:"forwards"`
				}
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				b, err := agentplane.BindGateway(a.Name, a.Forwards)
				if err != nil {
					return "", err
				}
				return mcp.JSON(map[string]any{"apply": false, "binding": b})
			},
		},
		{
			Name:        "replay_audit",
			Description: "Replay audit events for one claim (all when claim is empty). Reads the server's verified audit log under --allow-write; otherwise pass events.",
			Schema:      mcp.Object(map[string]any{"claim": mcp.String("claim name"), "events": map[string]any{"type": "array"}}),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Claim  string             `json:"claim"`
					Events []agentplane.Event `json:"events"`
				}
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				if a.Events == nil && mcpAudit != nil {
					events, err := mcpAudit.Replay(a.Claim)
					if err != nil {
						return "", err
					}
					return mcp.JSON(map[string]any{"events": events, "verified": true})
				}
				return mcp.JSON(map[string]any{"events": agentplane.Replay(a.Events, a.Claim)})
			},
		},
		{
			Name:        "audit_record",
			Description: "Record one MCP write as a replayable audit event. Requires --allow-write.",
			Write:       true,
			Schema:      mcp.Object(map[string]any{"event": map[string]any{"type": "object"}}, "event"),
			Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var a struct {
					Event agentplane.Event `json:"event"`
				}
				if err := decodeArgs(raw, &a); err != nil {
					return "", err
				}
				var ev agentplane.Event
				var err error
				if mcpAudit != nil {
					ev, err = mcpAudit.Append(a.Event)
				} else {
					ev, err = agentplane.Record(a.Event)
				}
				if err != nil {
					return "", err
				}
				return mcp.JSON(ev)
			},
		},
	}
}

func callerTenant(objectTenant string) error {
	caller := strings.TrimSpace(os.Getenv("KAIRON_MCP_TENANT"))
	return agentplane.CheckTenant(agentplane.Principal{Tenant: caller}, "", objectTenant)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Agent-plane helpers (compile, explain, claim check). Nothing here applies.",
	}
	cmd.AddCommand(newAgentCompileCmd(), newAgentDropsCmd(), newAgentMatrixCmd(), newAgentStepCmd(), newAgentCPUCmd(), newAgentGatewayCmd(), newAgentAuditVerifyCmd())
	return cmd
}

func newAgentCompileCmd() *cobra.Command {
	var file string
	c := &cobra.Command{
		Use:   "compile-policy",
		Short: "Compile a PolicyIntent JSON file into a MachineNetworkPolicy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var in agentplane.PolicyIntent
			if err := readJSON(file, &in); err != nil {
				return err
			}
			pol, err := agentplane.CompilePolicy(in)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(pol)
		},
	}
	c.Flags().StringVar(&file, "file", "", "PolicyIntent JSON file, or - for stdin")
	return c
}

func newAgentDropsCmd() *cobra.Command {
	var file string
	c := &cobra.Command{
		Use:   "explain-drops",
		Short: "Explain attributed drops from a JSON file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var in []agentplane.Drop
			if err := readJSON(file, &in); err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(agentplane.ExplainDrops(in))
		},
	}
	c.Flags().StringVar(&file, "file", "", "JSON array of drops, or - for stdin")
	return c
}

func newAgentMatrixCmd() *cobra.Command {
	var file string
	c := &cobra.Command{
		Use:   "migration-claim",
		Short: "Exit non-zero unless the lab matrix covers every v0.7 case",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var in []agentplane.MatrixCase
			if err := readJSON(file, &in); err != nil {
				return err
			}
			if err := agentplane.MigrationClaim(in); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "migration claim green")
			return nil
		},
	}
	c.Flags().StringVar(&file, "file", "", "JSON array of {name, passed}, or - for stdin")
	return c
}

func readJSON(file string, v any) error {
	var r *os.File
	var err error
	if file == "" || file == "-" {
		r = os.Stdin
	} else {
		r, err = os.Open(file)
		if err != nil {
			return err
		}
		defer r.Close()
	}
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("read json: %w", err)
	}
	return nil
}

func newAgentStepCmd() *cobra.Command {
	var file string
	c := &cobra.Command{
		Use:   "step-claim",
		Short: "Decide bind, hold, wait or expire for a MachineClaim JSON file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var in struct {
				Now   time.Time                `json:"now"`
				Claim model.MachineClaim       `json:"claim"`
				Warm  []agentplane.WarmMachine `json:"warm"`
			}
			if err := readJSON(file, &in); err != nil {
				return err
			}
			if in.Now.IsZero() {
				in.Now = time.Now().UTC()
			}
			decision, err := agentplane.StepClaim(in.Now, in.Claim, in.Warm)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(decision)
		},
	}
	c.Flags().StringVar(&file, "file", "", "JSON with claim and warm, or - for stdin")
	return c
}

func newAgentCPUCmd() *cobra.Command {
	var effective, reserved, node string
	c := &cobra.Command{
		Use:   "cpu-label",
		Short: "Project pinnable-cpus from a cpuset list",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			up, err := agentplane.CPULabelUpdate(agentplane.NodeCPUReport{Node: node, Effective: effective, Reserved: reserved})
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), up.Key+"="+up.Value)
			return nil
		},
	}
	c.Flags().StringVar(&effective, "effective", "", "cpuset.cpus.effective text")
	c.Flags().StringVar(&reserved, "reserved", "", "cpus to keep for the host")
	c.Flags().StringVar(&node, "node", "", "node name")
	return c
}

func newAgentGatewayCmd() *cobra.Command {
	var name string
	var guest, host int
	c := &cobra.Command{
		Use:   "gateway",
		Short: "Build a Gateway binding for one guest port",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := agentplane.BindGateway(name, []agentplane.PortForward{{GuestPort: guest, HostPort: host}})
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(b)
		},
	}
	c.Flags().StringVar(&name, "name", "", "gateway name")
	c.Flags().IntVar(&guest, "guest-port", 0, "guest port")
	c.Flags().IntVar(&host, "host-port", 0, "host port")
	return c
}

func newAgentAuditVerifyCmd() *cobra.Command {
	var file, claim string
	var show bool
	c := &cobra.Command{
		Use:   "audit-verify",
		Short: "Verify the MCP audit log hash chain and optionally replay one claim",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := agentplane.VerifyFile(file)
			if err != nil {
				return err
			}
			if !show && claim == "" {
				fmt.Fprintf(cmd.OutOrStdout(), "audit log ok: %d records\n", n)
				return nil
			}
			log, err := agentplane.OpenFileLog(file)
			if err != nil {
				return err
			}
			events, err := log.Replay(claim)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(events)
		},
	}
	c.Flags().StringVar(&file, "file", defaultAuditLogPath(), "audit log path")
	c.Flags().StringVar(&claim, "claim", "", "replay records for this claim")
	c.Flags().BoolVar(&show, "show", false, "print every record")
	return c
}
