// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kaironctl

import (
	"context"
	"fmt"

	"github.com/zyvorai/kairon/internal/kube"
)

func cmdPower(ctx context.Context, kc *kube.Client, args []string, state string) {
	ns, args := nsFlag(args)
	if len(args) != 1 {
		fatal(fmt.Errorf("command requires NAME"))
	}
	if err := setPowerState(ctx, kc, ns, args[0], state); err != nil {
		fatal(err)
	}
	okf("machine/%s -> %s", args[0], state)
}

func setPowerState(ctx context.Context, kc *kube.Client, namespace, name, state string) error {
	return kc.PatchMachine(ctx, namespace, name, map[string]any{"spec": map[string]any{"powerState": state}})
}
