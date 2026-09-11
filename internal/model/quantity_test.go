// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package model

import "testing"

func TestParseVCPUs(t *testing.T) {
	cases := map[string]uint32{"2": 2, "1500m": 2, "0.5": 1, "8": 8}
	for in, want := range cases {
		got, err := ParseVCPUs(in)
		if err != nil || got != want {
			t.Fatalf("ParseVCPUs(%q)=%d,%v want %d,nil", in, got, err, want)
		}
	}
}

func TestParseMemoryMiB(t *testing.T) {
	cases := map[string]uint64{"2Gi": 2048, "512Mi": 512, "1G": 954, "1048576": 1}
	for in, want := range cases {
		got, err := ParseMemoryMiB(in)
		if err != nil || got != want {
			t.Fatalf("ParseMemoryMiB(%q)=%d,%v want %d,nil", in, got, err, want)
		}
	}
}
