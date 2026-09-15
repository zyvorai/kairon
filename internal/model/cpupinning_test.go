// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"reflect"
	"testing"
)

func TestParseCPUListRange(t *testing.T) {
	got, err := ParseCPUList("2-5")
	if err != nil {
		t.Fatal(err)
	}
	want := []uint32{2, 3, 4, 5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseCPUListMixedRangesAndSingles(t *testing.T) {
	got, err := ParseCPUList("2,3,4-8,20")
	if err != nil {
		t.Fatal(err)
	}
	want := []uint32{2, 3, 4, 5, 6, 7, 8, 20}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseCPUListDeduplicatesAndSortsOutOfOrderInput(t *testing.T) {
	got, err := ParseCPUList("8,2,2-4")
	if err != nil {
		t.Fatal(err)
	}
	want := []uint32{2, 3, 4, 8}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseCPUListEmptyReturnsEmptyNotNil(t *testing.T) {
	got, err := ParseCPUList("")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("got %v, want an empty (non-nil) slice", got)
	}
}

func TestParseCPUListRejectsInvalidInput(t *testing.T) {
	for _, bad := range []string{"x", "2-", "-5", "5-2", "2--5", "2,x,5"} {
		if _, err := ParseCPUList(bad); err == nil {
			t.Errorf("ParseCPUList(%q): expected an error", bad)
		}
	}
}

func TestParseCPUListToleratesWhitespace(t *testing.T) {
	got, err := ParseCPUList(" 2, 3-4 , 6 ")
	if err != nil {
		t.Fatal(err)
	}
	want := []uint32{2, 3, 4, 6}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
