// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"testing"
	"time"

	"github.com/zyvorai/kairon/internal/ebpfedge"
)

func TestRestoreStoreRoundTrip(t *testing.T) {
	s := NewRestoreStore()
	want := ebpfedge.RestoreResult{Restored: 3, BlackholeWindowMs: 40, Identity: 9}
	s.Put("demo", "web", want)
	got, ok := s.Get("demo", "web")
	if !ok || got != want {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
	if _, ok := s.Get("demo", "other"); ok {
		t.Fatal("unexpected hit")
	}
	var nilStore *RestoreStore
	nilStore.Put("demo", "web", want)
	if _, ok := nilStore.Get("demo", "web"); ok {
		t.Fatal("nil store should miss")
	}
	_ = time.Now()
}
