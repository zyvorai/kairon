// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

func ParseVCPUs(v string) (uint32, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, fmt.Errorf("cpu quantity is empty")
	}
	if strings.HasSuffix(v, "m") {
		milli, err := strconv.ParseFloat(strings.TrimSuffix(v, "m"), 64)
		if err != nil || milli <= 0 {
			return 0, fmt.Errorf("invalid cpu quantity %q", v)
		}
		return uint32(math.Ceil(milli / 1000)), nil
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid cpu quantity %q", v)
	}
	return uint32(math.Ceil(n)), nil
}

func ParseMemoryMiB(v string) (uint64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, fmt.Errorf("memory quantity is empty")
	}
	units := []struct {
		suffix string
		bytes  float64
	}{
		{"Ti", 1024 * 1024 * 1024 * 1024},
		{"Gi", 1024 * 1024 * 1024},
		{"Mi", 1024 * 1024},
		{"Ki", 1024},
		{"T", 1000 * 1000 * 1000 * 1000},
		{"G", 1000 * 1000 * 1000},
		{"M", 1000 * 1000},
		{"K", 1000},
	}
	for _, u := range units {
		if strings.HasSuffix(v, u.suffix) {
			n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(v, u.suffix)), 64)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("invalid memory quantity %q", v)
			}
			return uint64(math.Ceil(n * u.bytes / (1024 * 1024))), nil
		}
	}
	bytes, err := strconv.ParseFloat(v, 64)
	if err != nil || bytes <= 0 {
		return 0, fmt.Errorf("invalid memory quantity %q", v)
	}
	return uint64(math.Ceil(bytes / (1024 * 1024))), nil
}
