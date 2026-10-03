// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kaironctl

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/zyvorai/kairon/internal/ebpfedge"
	"github.com/zyvorai/kairon/internal/kaironctl/style"
)

func newNetworkEdgeCmds(opts *Options) []*cobra.Command {
	return []*cobra.Command{
		newNetworkIdentityCmd(opts),
		newNetworkCaptureCmd(opts),
	}
}

func newNetworkIdentityCmd(opts *Options) *cobra.Command {
	var machine string
	cmd := &cobra.Command{
		Use:   "identity",
		Short: "Print the stable eBPF identity for a Machine",
		Long: `Identity is FNV-1a of namespace and name. It does not change when
the guest IP changes, so MachineNetworkPolicy survives live migration.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				machine = args[0]
			}
			if machine == "" {
				return fmt.Errorf("machine name required")
			}
			ns := opts.Namespace
			if ns == "" {
				ns = "default"
			}
			id := ebpfedge.StableIdentity(ns, machine)
			style.Log(style.EmojiOK, "identity %d for %s/%s", id, ns, machine)
			fmt.Fprintf(cmd.OutOrStdout(), "%d\n", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&machine, "machine", "", "Machine name")
	return cmd
}

func newNetworkCaptureCmd(opts *Options) *cobra.Command {
	var seconds int
	var filter string
	cmd := &cobra.Command{
		Use:   "capture MACHINE",
		Short: "Build a bounded eBPF capture request (max 30s)",
		Long: `Prints the capture session Kairon would hand to FluxVM. The ringbuf
is opened by the dataplane, not by this process. Seconds above 30 are rejected.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ns := opts.Namespace
			if ns == "" {
				ns = "default"
			}
			session, err := ebpfedge.NewCapture(ns, args[0], filter, seconds, time.Now().UTC())
			if err != nil {
				return err
			}
			style.Log(style.EmojiOK, "capture %s/%s for %ds token=%s", session.Namespace, session.Machine, session.Seconds, session.Token)
			fmt.Fprintf(cmd.OutOrStdout(), "expires %s\n", session.ExpiresAt.Format(time.RFC3339))
			if session.Filter != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "filter %s\n", session.Filter)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&seconds, "seconds", 15, "capture length, 1-30")
	cmd.Flags().StringVar(&filter, "filter", "", "optional tcpdump-style filter passed to FluxVM")
	return cmd
}
