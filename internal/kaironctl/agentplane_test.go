// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kaironctl

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentPlaneCompileTool(t *testing.T) {
	var compile, audit interface{ Name() string }
	_ = compile
	_ = audit
	tools := agentPlaneTools()
	byName := map[string]func(context.Context, json.RawMessage) (string, error){}
	write := map[string]bool{}
	for _, tool := range tools {
		byName[tool.Name] = tool.Call
		write[tool.Name] = tool.Write
	}
	if write["audit_record"] != true {
		t.Fatal("audit_record must be write-gated")
	}
	if write["compile_network_policy"] {
		t.Fatal("compile must stay read-only")
	}
	raw := json.RawMessage(`{"intent":{"name":"agents","namespace":"ml","tenant":"acme","allowFqdns":["registry.internal"]}}`)
	out, err := byName["compile_network_policy"](context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"defaultAllow":false`) && !strings.Contains(out, `"defaultAllow": false`) {
		t.Fatalf("policy output: %s", out)
	}
	if _, err := byName["compile_network_policy"](context.Background(), json.RawMessage(`{"intent":{"name":"open","allowFqdns":["*"]}}`)); err == nil {
		t.Fatal("wildcard should fail")
	}
	t.Setenv("KAIRON_MCP_TENANT", "acme")
	if _, err := byName["compile_network_policy"](context.Background(), json.RawMessage(`{"intent":{"name":"x","tenant":"other","allowFqdns":["a.example"]}}`)); err == nil {
		t.Fatal("cross-tenant compile should fail")
	}
}
