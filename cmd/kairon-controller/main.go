// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/tls"
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
	"github.com/zyvorai/kairon/internal/leaderelection"
	"github.com/zyvorai/kairon/internal/metrics"
	"github.com/zyvorai/kairon/internal/scheduler"
	"github.com/zyvorai/kairon/internal/tlsreload"
)

var version = "dev"

// tlsReloadInterval bounds how often the webhook's TLS certificate/key
// files are polled for a change -- see internal/tlsreload.
const tlsReloadInterval = 30 * time.Second

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
	webhookAddr := flag.String("webhook-addr", ":8443", "validating admission webhook listen address (see -webhook-tls-cert/-key)")
	webhookTLSCert := flag.String("webhook-tls-cert", "", "TLS certificate PEM for the admission webhook; must be set together with -webhook-tls-key. Empty (the default) disables the webhook -- MachineQuota/MachineDisruptionBudget enforcement stays reconcile-loop/kaironctl-only, same as before this flag existed")
	webhookTLSKey := flag.String("webhook-tls-key", "", "TLS private key PEM for the admission webhook; must be set together with -webhook-tls-cert")
	leaderElect := flag.Bool("leader-elect", false, "coordinate multiple kairon-controller replicas via a coordination.k8s.io/v1 Lease (see internal/leaderelection) so only the elected leader reconciles -- the admission webhook and health/metrics server are unaffected and always serve from every replica. Off by default for backward compatibility: turning it on requires the ServiceAccount to be granted the small, namespaced leases RBAC this needs, and -leader-elect-namespace (or KAIRON_CONTROLLER_NAMESPACE) to be set -- the Helm chart's controller.leaderElection.enabled turns both on together. Safe to enable even with a single replica; required once controller.replicaCount > 1")
	leaderElectNamespace := flag.String("leader-elect-namespace", os.Getenv("KAIRON_CONTROLLER_NAMESPACE"), "namespace holding the leader-election Lease object; required when -leader-elect is set (defaults to KAIRON_CONTROLLER_NAMESPACE)")
	leaderElectLeaseName := flag.String("leader-elect-lease-name", "kairon-controller", "name of the leader-election Lease object")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return 0
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if (*webhookTLSCert == "") != (*webhookTLSKey == "") {
		log.Error("secure startup refused", "reason", "-webhook-tls-cert and -webhook-tls-key must be set together")
		return 1
	}
	if *leaderElect && *leaderElectNamespace == "" {
		log.Error("secure startup refused", "reason", "-leader-elect requires -leader-elect-namespace (or KAIRON_CONTROLLER_NAMESPACE) to be set")
		return 1
	}
	var webhookTLSConfig *tls.Config
	var webhookCertWatcher *tlsreload.Watcher
	if *webhookTLSCert != "" {
		var err error
		webhookTLSConfig, webhookCertWatcher, err = controller.WebhookTLSConfig(log, *webhookTLSCert, *webhookTLSKey)
		if err != nil {
			log.Error("webhook TLS", "error", err)
			return 1
		}
	}
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
	if webhookTLSConfig != nil {
		go func() {
			if err := ctl.RunWebhook(ctx, *webhookAddr, webhookTLSConfig, webhookCertWatcher, tlsReloadInterval); err != nil && ctx.Err() == nil {
				log.Error("webhook server", "error", err)
			}
		}()
		log.Info("admission webhook listening", "address", *webhookAddr)
	}
	hs.SetReady(true)
	runReconcile := func(runCtx context.Context) {
		if err := ctl.Run(runCtx, *interval); err != nil && runCtx.Err() == nil {
			log.Error("controller stopped", "error", err)
		}
	}
	if !*leaderElect {
		runReconcile(ctx)
		return 0
	}
	identity, err := os.Hostname()
	if err != nil || identity == "" {
		identity = "unknown"
	}
	elector := &leaderelection.Elector{
		Kube:      kc,
		Namespace: *leaderElectNamespace,
		Name:      *leaderElectLeaseName,
		Identity:  fmt.Sprintf("%s_%d", identity, os.Getpid()),
		Log:       log,
	}
	elector.Run(ctx, runReconcile)
	return 0
}
