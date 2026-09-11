// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/zyvorai/kairon/internal/agent"
	"github.com/zyvorai/kairon/internal/fluxvm"
	"github.com/zyvorai/kairon/internal/health"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/migration"
)

var version = "dev"

func main() {
	interval := flag.Duration("interval", 3*time.Second, "reconciliation interval")
	healthAddr := flag.String("health-addr", ":8081", "health server address")
	fluxURL := flag.String("fluxvm-url", env("FLUXVM_URL", "http://127.0.0.1:7788"), "node-local FluxVM URL")
	backend := flag.String("default-backend", env("KAIRON_DEFAULT_BACKEND", "qemu"), "backend used when Machine runtime.backend is auto")
	imageRoot := flag.String("image-root", env("KAIRON_IMAGE_ROOT", "/var/lib/fluxvm/images"), "allowed root for Machine image paths")
	vfioAllowlistRaw := flag.String("vfio-allowlist", env("KAIRON_VFIO_ALLOWLIST", ""), "comma-separated PCI BDFs this node permits for DRA-backed VFIO passthrough")
	migrationAddr := flag.String("migration-addr", env("KAIRON_MIGRATION_ADDR", ":9443"), "mTLS migration peer listen address")
	migrationCA := flag.String("migration-ca", env("KAIRON_MIGRATION_CA", ""), "CA PEM used to verify Kairon node peers")
	migrationCert := flag.String("migration-cert", env("KAIRON_MIGRATION_CERT", ""), "node migration TLS certificate PEM")
	migrationKey := flag.String("migration-key", env("KAIRON_MIGRATION_KEY", ""), "node migration TLS private key PEM")
	migrationServerName := flag.String("migration-server-name", env("KAIRON_MIGRATION_SERVER_NAME", "kairon-node"), "TLS server name expected from migration peers; empty verifies the peer IP from the URL")
	migrationStateDir := flag.String("migration-state-dir", env("KAIRON_MIGRATION_STATE_DIR", "/var/run/kairon/migrations"), "destination migration session journal")
	migrationAdapterSocket := flag.String("migration-adapter-socket", env("KAIRON_MIGRATION_ADAPTER_SOCKET", ""), "optional Kairon migration adapter Unix socket")
	migrationPort := flag.Int("migration-port", 9443, "peer migration TCP port advertised through node InternalIP")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	node := os.Getenv("NODE_NAME")
	if node == "" {
		fmt.Fprintln(os.Stderr, "NODE_NAME is required")
		os.Exit(2)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	kc, err := kube.FromEnvironment()
	if err != nil {
		log.Error("kubernetes client", "error", err)
		os.Exit(1)
	}
	fc := fluxvm.New(*fluxURL, os.Getenv("FLUXVM_TOKEN"))
	vfioAllowlist, err := agent.ParseVFIOAllowlist(*vfioAllowlistRaw)
	if err != nil {
		log.Error("invalid VFIO allowlist", "error", err)
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	hs := &health.Server{}
	go func() {
		if err := hs.Run(ctx, *healthAddr); err != nil && err != http.ErrServerClosed {
			log.Error("health server", "error", err)
		}
	}()
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			err := fc.Ready(ctx)
			hs.SetReady(err == nil)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	peer, source, err := configureMigration(ctx, log, cancel, node, *migrationAddr, *migrationCA, *migrationCert, *migrationKey, *migrationServerName, *migrationStateDir, *migrationAdapterSocket)
	if err != nil {
		log.Error("migration control plane", "error", err)
		os.Exit(2)
	}

	a := &agent.Agent{
		NodeName:       node,
		Kube:           kc,
		Flux:           fc,
		DefaultBackend: *backend,
		ImageRoot:      *imageRoot,
		VFIOAllowlist:  vfioAllowlist,
		MigrationPeer:  peer,
		SourceMigrator: source,
		MigrationPort:  *migrationPort,
		Log:            log,
	}
	if err := a.Run(ctx, *interval); err != nil && ctx.Err() == nil {
		log.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}

func configureMigration(ctx context.Context, log *slog.Logger, cancel context.CancelFunc, nodeName, addr, caPath, certPath, keyPath, serverName, stateDir, adapterSocket string) (*migration.Client, migration.SourceDriver, error) {
	configured := 0
	for _, v := range []string{caPath, certPath, keyPath} {
		if strings.TrimSpace(v) != "" {
			configured++
		}
	}
	if configured == 0 {
		log.Warn("live migration peer control plane disabled; cold migration remains available")
		return nil, nil, nil
	}
	if configured != 3 {
		return nil, nil, fmt.Errorf("migration mTLS is fail-closed: --migration-ca, --migration-cert and --migration-key must be configured together")
	}
	serverTLS, err := migration.ServerTLSConfig(caPath, certPath, keyPath)
	if err != nil {
		return nil, nil, err
	}
	clientTLS, err := migration.ClientTLSConfig(caPath, certPath, keyPath, serverName)
	if err != nil {
		return nil, nil, err
	}

	var destination migration.DestinationDriver = migration.UnsupportedDestinationDriver{Reason: "no Kairon migration adapter is configured; current FluxVM does not expose a verified live-migration API"}
	var source migration.SourceDriver = migration.UnsupportedSourceDriver{Reason: "no Kairon migration adapter is configured; current FluxVM does not expose a verified live-migration API"}
	if strings.TrimSpace(adapterSocket) != "" {
		adapter := migration.NewAdapter(adapterSocket)
		destination = adapter
		source = migration.SourceAdapter{Adapter: adapter}
	}

	server := &http.Server{
		Addr:              addr,
		Handler:           (&migration.Server{Store: migration.NewFileStore(stateDir), Driver: destination, NodeName: nodeName}).Handler(),
		TLSConfig:         serverTLS,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen for migration peers: %w", err)
	}
	tlsListener := tls.NewListener(listener, serverTLS)
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		log.Info("migration mTLS peer listening", "address", addr, "adapterConfigured", strings.TrimSpace(adapterSocket) != "")
		if err := server.Serve(tlsListener); err != nil && err != http.ErrServerClosed {
			log.Error("migration peer server stopped", "error", err)
			cancel()
		}
	}()

	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}, Timeout: 20 * time.Second}
	return migration.NewClient(httpClient), source, nil
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
