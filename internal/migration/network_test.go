// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zyvorai/kairon/internal/fluxvm"
)

type countingDest struct {
	prepareN, commitN, abortN int
}

func (c *countingDest) Prepare(context.Context, Session) (PrepareResult, error) {
	c.prepareN++
	return PrepareResult{TransferSupported: true, Endpoint: "unix:///tmp/x", Backend: "stub"}, nil
}
func (c *countingDest) Commit(context.Context, Session) error { c.commitN++; return nil }
func (c *countingDest) Abort(context.Context, Session) error  { c.abortN++; return nil }

func TestNetworkAwareDestinationRestoreResume(t *testing.T) {
	var restore, resume bool
	fs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/vms/rt-1/network/migration/restore":
			restore = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/vms/rt-1/network/migration/resume":
			resume = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			http.Error(w, "unexpected", 404)
		}
	}))
	defer fs.Close()
	fc := fluxvm.New(fs.URL, "")
	fc.HTTP = fs.Client()
	inner := &countingDest{}
	d := NetworkAwareDestination{Inner: inner, Flux: fc}
	session := Session{ID: "s1", Namespace: "ns", Machine: "m", SourceNode: "a", TargetNode: "b", RuntimeID: "rt-1", NetworkSnapshot: json.RawMessage(`{"v":1}`)}
	if _, err := d.Prepare(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if err := d.Commit(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if !restore || !resume || inner.prepareN != 1 || inner.commitN != 1 {
		t.Fatalf("restore=%v resume=%v inner=%+v", restore, resume, inner)
	}
}
