// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kaironctl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zyvorai/kairon/internal/agentplane"
	"github.com/zyvorai/kairon/internal/mcp"
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
				ev, err := agentplane.Record(a.Event)
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
	cmd.AddCommand(newAgentCompileCmd(), newAgentDropsCmd(), newAgentMatrixCmd())
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
