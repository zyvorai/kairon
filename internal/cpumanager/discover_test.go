// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package cpumanager

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverSubtractsKubeletExclusiveAndDefault(t *testing.T) {
	dir := t.TempDir()
	online := filepath.Join(dir, "online")
	state := filepath.Join(dir, "cpu_manager_state")
	os.WriteFile(online, []byte("0-7\n"), 0o644)
	os.WriteFile(state, []byte(`{"policyName":"static","defaultCpuSet":"0-1","entries":{"p":{"c":"2-3"}}}`), 0o644)
	got, err := Discover(Input{OnlinePath: online, StatePath: state})
	if err != nil {
		t.Fatal(err)
	}
	if got.Refused != "" || got.List != "4-7" {
		t.Fatalf("got %+v", got)
	}
}

func TestDiscoverRefusesWithoutProof(t *testing.T) {
	dir := t.TempDir()
	online := filepath.Join(dir, "online")
	os.WriteFile(online, []byte("0-3\n"), 0o644)
	got, err := Discover(Input{OnlinePath: online, StatePath: filepath.Join(dir, "missing")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Refused == "" || got.List != "" {
		t.Fatalf("got %+v, want refused", got)
	}
}

func TestLabelPatchDoesNotClobberOperator(t *testing.T) {
	if LabelPatch("2-5", "", "4-7", false, false) != nil {
		t.Fatal("operator label was overwritten")
	}
	got := LabelPatch("2-5", SourceDiscovered, "4-7", true, false)
	labels := got["metadata"].(map[string]any)["labels"].(map[string]any)
	if labels["kairon.zyvor.dev/pinnable-cpus"] != "4-7" {
		t.Fatalf("patch = %#v", got)
	}
	if LabelPatch("4-7", SourceDiscovered, "", true, true) == nil {
		t.Fatal("refused discovery should clear an owned label")
	}
}
