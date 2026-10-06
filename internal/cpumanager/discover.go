// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package cpumanager proves a node's pinnable CPU set from sysfs and
// kubelet cpu_manager_state. An operator label is not proof. Discovery
// fails closed when neither the online set nor the kubelet state can be
// read, so kairon-node does not publish kairon.zyvor.dev/pinnable-cpus.
package cpumanager

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/zyvorai/kairon/internal/model"
)

const (
	// AnnSource marks a pinnable-cpus label this agent published.
	// An operator label without this annotation is left alone.
	AnnSource = "kairon.zyvor.dev/pinnable-cpus-source"
	// SourceDiscovered is the only value this agent writes.
	SourceDiscovered = "discovered"
)

// Input is the files and reserved set for one node.
type Input struct {
	OnlinePath string
	StatePath  string
	// Reserved is a cpuset list the operator still wants kept off the
	// pinnable set (OS, kairon-node). Empty means "use kubelet defaultCpuSet".
	Reserved string
}

// Result is a cpuset list safe to publish, or Refused when the node
// cannot prove a set.
type Result struct {
	CPUs    []uint32
	List    string
	Refused string
}

type cpuManagerState struct {
	PolicyName    string                       `json:"policyName"`
	DefaultCPUSet string                       `json:"defaultCpuSet"`
	Entries       map[string]map[string]string `json:"entries"`
}

// Discover reads online CPUs and subtracts kubelet exclusive allocations
// and the reserved set. Missing online CPUs refuse. A missing state file
// refuses unless Reserved is set, so an empty node does not advertise
// every core.
func Discover(in Input) (Result, error) {
	onlineRaw, err := os.ReadFile(in.OnlinePath)
	if err != nil {
		return Result{Refused: fmt.Sprintf("online cpus: %v", err)}, nil
	}
	online, err := model.ParseCPUList(string(onlineRaw))
	if err != nil || len(online) == 0 {
		return Result{Refused: "online cpus missing or unparseable"}, nil
	}
	taken := map[uint32]struct{}{}
	stateRaw, err := os.ReadFile(in.StatePath)
	if err != nil && in.Reserved == "" {
		return Result{Refused: fmt.Sprintf("cpu_manager_state: %v", err)}, nil
	}
	if err == nil {
		var st cpuManagerState
		if jerr := json.Unmarshal(stateRaw, &st); jerr != nil {
			return Result{Refused: fmt.Sprintf("cpu_manager_state: %v", jerr)}, nil
		}
		if in.Reserved == "" {
			add(taken, st.DefaultCPUSet)
		}
		for _, containers := range st.Entries {
			for _, set := range containers {
				add(taken, set)
			}
		}
	}
	if in.Reserved != "" {
		add(taken, in.Reserved)
	}
	free := make([]uint32, 0, len(online))
	for _, cpu := range online {
		if _, ok := taken[cpu]; ok {
			continue
		}
		free = append(free, cpu)
	}
	sort.Slice(free, func(i, j int) bool { return free[i] < free[j] })
	return Result{CPUs: free, List: format(free)}, nil
}

// LabelPatch publishes or clears the pinnable label. owned is true when
// this agent already set AnnSource. An operator label (present, not owned)
// is not overwritten. nil means no patch.
func LabelPatch(current, source, list string, owned bool, refused bool) map[string]any {
	if current != "" && !owned {
		return nil
	}
	if refused || list == "" {
		if current == "" && source == "" {
			return nil
		}
		return map[string]any{"metadata": map[string]any{
			"labels":      map[string]any{model.PinnableCPUsLabel: nil},
			"annotations": map[string]any{AnnSource: nil},
		}}
	}
	if current == list && source == SourceDiscovered {
		return nil
	}
	return map[string]any{"metadata": map[string]any{
		"labels":      map[string]any{model.PinnableCPUsLabel: list},
		"annotations": map[string]any{AnnSource: SourceDiscovered},
	}}
}

func add(dst map[uint32]struct{}, raw string) {
	cpus, err := model.ParseCPUList(raw)
	if err != nil {
		return
	}
	for _, c := range cpus {
		dst[c] = struct{}{}
	}
}

func format(cpus []uint32) string {
	if len(cpus) == 0 {
		return ""
	}
	var b strings.Builder
	start, prev := cpus[0], cpus[0]
	flush := func(end uint32) {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		if start == end {
			b.WriteString(strconv.FormatUint(uint64(start), 10))
			return
		}
		b.WriteString(strconv.FormatUint(uint64(start), 10))
		b.WriteByte('-')
		b.WriteString(strconv.FormatUint(uint64(end), 10))
	}
	for _, cpu := range cpus[1:] {
		if cpu == prev+1 {
			prev = cpu
			continue
		}
		flush(prev)
		start, prev = cpu, cpu
	}
	flush(prev)
	return b.String()
}
