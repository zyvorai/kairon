// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// PinnableCPUsLabel is an opt-in, operator-asserted node label naming the
// real host CPU numbers a node makes available for exclusive
// spec.resources.cpuPinning allocation -- the same "operator asserts a
// fact Kairon has no other way to know" shape StorageDomainLabel/
// NetworkDomainLabel/VFIODevicesLabel already established
// (internal/controller/preflight.go). Absent or empty on a node means
// "no pinnable cores here" -- fail-closed, the same posture an empty
// KAIRON_VFIO_ALLOWLIST already has for VFIO passthrough, not "assume
// every core is free."
//
// Value syntax deliberately matches Linux's own cpuset.cpus list format
// (e.g. "2-15" or "2,3,4-8,20") -- an operator can often paste the output
// of `cat /sys/fs/cgroup/cpuset.cpus.effective` (minus whatever they want
// reserved for the OS/kairon-node itself) directly.
const PinnableCPUsLabel = "kairon.zyvor.dev/pinnable-cpus"

// ParseCPUList parses a Linux cpuset.cpus-style list ("2-15" or
// "2,3,4-8,20") into an explicit, deduplicated, ascending slice of CPU
// numbers. An empty string returns an empty (not nil) slice, matching
// "asserted, but pins nothing" rather than an error.
func ParseCPUList(raw string) ([]uint32, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []uint32{}, nil
	}
	seen := map[uint32]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if lo, hi, ok := strings.Cut(part, "-"); ok {
			start, err := strconv.ParseUint(lo, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("invalid CPU range %q: %w", part, err)
			}
			end, err := strconv.ParseUint(hi, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("invalid CPU range %q: %w", part, err)
			}
			if end < start {
				return nil, fmt.Errorf("invalid CPU range %q: end before start", part)
			}
			for v := start; v <= end; v++ {
				seen[uint32(v)] = struct{}{}
			}
		} else {
			v, err := strconv.ParseUint(part, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("invalid CPU number %q: %w", part, err)
			}
			seen[uint32(v)] = struct{}{}
		}
	}
	out := make([]uint32, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}
