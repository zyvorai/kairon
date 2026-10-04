// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kaironctl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"time"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

// cmdClaim creates a MachineClaim against POOL and, unless --no-wait,
// waits for it to bind and prints the Machine name.
func cmdClaim(ctx context.Context, kc *kube.Client, args []string) {
	ns, args := nsFlag(args)
	if len(args) < 1 || args[0] == "" || args[0][0] == '-' {
		fatal(fmt.Errorf("usage: kaironctl claim POOL [NAME] [--label k=v] [--retain] [--wait 60s | --no-wait]"))
	}
	pool := args[0]
	args = args[1:]
	name := ""
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		name, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("claim", flag.ExitOnError)
	var labels stringSliceFlag
	fs.Var(&labels, "label", "label key=value added to the claimed Machine (repeatable)")
	retain := fs.Bool("retain", false, "keep the Machine when the claim is deleted (default: delete it)")
	ttl := fs.Duration("ttl", 0, "delete the claim (and release its Machine) this long after it binds")
	wait := fs.Duration("wait", 60*time.Second, "how long to wait for the claim to bind")
	noWait := fs.Bool("no-wait", false, "create the claim and return immediately")
	_ = fs.Parse(args)
	labelMap, err := parseKeyValues(labels)
	if err != nil {
		fatal(err)
	}
	if name == "" {
		buf := make([]byte, 3)
		_, _ = rand.Read(buf)
		name = pool + "-claim-" + hex.EncodeToString(buf)
	}
	claim := newMachineClaim(ns, name, pool, labelMap, *retain)
	claim.Spec.TTLSeconds = int64(ttl.Seconds())
	if _, err := kc.CreateMachineClaim(ctx, ns, claim); err != nil {
		fatal(err)
	}
	if *noWait {
		okf("machineclaim/%s created", name)
		return
	}
	got, err := waitForClaim(ctx, kc, ns, name, *wait)
	if err != nil {
		fatal(err)
	}
	okf("machineclaim/%s bound to machine/%s in %dms", name, got.Status.MachineName, got.Status.BindMillis)
}

func newMachineClaim(ns, name, pool string, labels map[string]string, retain bool) model.MachineClaim {
	policy := ""
	if retain {
		policy = model.ReclaimRetain
	}
	return model.MachineClaim{
		TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachineClaim},
		Metadata: model.ObjectMeta{Name: name, Namespace: ns},
		Spec:     model.MachineClaimSpec{PoolName: pool, Labels: labels, ReclaimPolicy: policy},
	}
}

func waitForClaim(ctx context.Context, kc *kube.Client, ns, name string, timeout time.Duration) (model.MachineClaim, error) {
	deadline := time.Now().Add(timeout)
	for {
		got, err := kc.GetMachineClaim(ctx, ns, name)
		if err == nil && got.Status.Phase == model.ClaimBound && got.Status.MachineName != "" {
			return got, nil
		}
		if time.Now().After(deadline) {
			msg := got.Status.Message
			if err != nil {
				msg = err.Error()
			}
			return got, fmt.Errorf("machineclaim/%s not bound after %s: %s", name, timeout, dash(msg))
		}
		select {
		case <-ctx.Done():
			return got, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
