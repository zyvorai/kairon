// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zyvorai/kairon/internal/controller"
	"github.com/zyvorai/kairon/internal/health"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/metrics"
	"github.com/zyvorai/kairon/internal/scheduler"
)

var version = "dev"

func main() {
	os.Exit(run())
}

// run returns the process exit code rather than calling os.Exit directly,
// so every deferred cleanup (e.g. cancel()) actually runs before exit.
func run() int {
	interval := flag.Duration("interval", 5*time.Second, "reconciliation interval")
	healthAddr := flag.String("health-addr", ":8080", "health server address")
	requireLabel := flag.Bool("require-capable-label", true, "only schedule onto nodes labeled kairon.zyvor.dev/capable=true")
	maxPerNode := flag.Int("migration-max-concurrent-per-node", 0, "max concurrent non-terminal migrations touching a single node (0 = unlimited)")
	maxCluster := flag.Int("migration-max-concurrent-cluster", 0, "max concurrent non-terminal migrations cluster-wide (0 = unlimited)")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return 0
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	kc, err := kube.FromEnvironment()
	if err != nil {
		log.Error("kubernetes client", "error", err)
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	rec := metrics.NewRecorder()
	hs := &health.Server{Metrics: rec.Handler()}
	go func() {
		if err := hs.Run(ctx, *healthAddr); err != nil && err != http.ErrServerClosed {
			log.Error("health server", "error", err)
		}
	}()
	ctl := &controller.Controller{
		Kube:                 kc,
		Scheduler:            scheduler.Scheduler{RequireCapableLabel: *requireLabel},
		Log:                  log,
		Metrics:              rec,
		MaxConcurrentPerNode: *maxPerNode,
		MaxConcurrentCluster: *maxCluster,
	}
	hs.SetReady(true)
	if err := ctl.Run(ctx, *interval); err != nil && ctx.Err() == nil {
		log.Error("controller stopped", "error", err)
		return 1
	}
	return 0
}
