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
	"github.com/zyvorai/kairon/internal/consoleproxy"
	"github.com/zyvorai/kairon/internal/fluxvm"
	"github.com/zyvorai/kairon/internal/health"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/metrics"
	"github.com/zyvorai/kairon/internal/migration"
	"github.com/zyvorai/kairon/internal/tlsreload"
)

var version = "dev"

// tlsReloadInterval bounds how often every TLS hop this binary owns
// (migration mTLS, the VNC console relay) polls its certificate/key files
// for a change -- see internal/tlsreload. Frequent enough to notice a
// rotation well within a typical short-lived certificate's remaining
// lifetime, cheap enough (a stat() call) to not matter at this cadence.
const tlsReloadInterval = 30 * time.Second

func main() {
	os.Exit(run())
}

// run returns the process exit code rather than calling os.Exit directly,
// so every deferred cleanup (e.g. cancel()) actually runs before exit.
func run() int {
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
	consoleAddr := flag.String("console-addr", env("KAIRON_NODE_CONSOLE_ADDR", ":8090"), "VNC console relay listen address")
	consoleToken := flag.String("console-token", os.Getenv("KAIRON_NODE_CONSOLE_TOKEN"), "shared bearer token kairon-ui must present for VNC console relay (default: $KAIRON_NODE_CONSOLE_TOKEN); empty disables the console listener")
	consoleTLSCert := flag.String("console-tls-cert", env("KAIRON_NODE_CONSOLE_TLS_CERT", ""), "optional TLS certificate PEM for the console relay listener (server-only TLS -- the shared token already authenticates the caller, so no client cert is needed); must be set together with --console-tls-key")
	consoleTLSKey := flag.String("console-tls-key", env("KAIRON_NODE_CONSOLE_TLS_KEY", ""), "optional TLS private key PEM for the console relay listener; must be set together with --console-tls-cert")
	csiSocket := flag.String("csi-socket", env("KAIRON_CSI_SOCKET", ""), "kairon-csi-node's local Unix socket path (default: $KAIRON_CSI_SOCKET); empty refuses any CSI-backed (network-block) Machine volume with a clear error rather than silently failing -- see docs/guides/machine-storage-csi.md")
	csiStagingDir := flag.String("csi-staging-dir", env("KAIRON_CSI_STAGING_DIR", "/var/lib/kairon/csi/staging"), "per-node directory kairon-node asks kairon-csi-node to stage CSI volumes under")
	csiPublishDir := flag.String("csi-publish-dir", env("KAIRON_CSI_PUBLISH_DIR", "/var/lib/kairon/csi/publish"), "per-node directory kairon-node asks kairon-csi-node to publish (bind-mount) CSI volumes under")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return 0
	}
	node := os.Getenv("NODE_NAME")
	if node == "" {
		fmt.Fprintln(os.Stderr, "NODE_NAME is required")
		return 2
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	kc, err := kube.FromEnvironment()
	if err != nil {
		log.Error("kubernetes client", "error", err)
		return 1
	}
	rec := metrics.NewNodeRecorder()
	kc.Observe = rec.ObserveAPIRequest
	fc := fluxvm.New(*fluxURL, os.Getenv("FLUXVM_TOKEN"))
	vfioAllowlist, err := agent.ParseVFIOAllowlist(*vfioAllowlistRaw)
	if err != nil {
		log.Error("invalid VFIO allowlist", "error", err)
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	hs := &health.Server{Metrics: rec.Handler()}
	go func() {
		if err := hs.Run(ctx, *healthAddr); err != nil && err != http.ErrServerClosed {
			log.Error("health server", "error", err)
		}
	}()
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		// FluxVM's own /readyz already fail-closes when sandbox.dataplane.required
		// is set, so the node agent's readiness only needs to track it directly.
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

	peer, source, err := configureMigration(ctx, log, cancel, node, fc, *migrationAddr, *migrationCA, *migrationCert, *migrationKey, *migrationServerName, *migrationStateDir, *migrationAdapterSocket)
	if err != nil {
		log.Error("migration control plane", "error", err)
		return 2
	}

	configureConsole(ctx, log, fc, *consoleAddr, *consoleToken, *consoleTLSCert, *consoleTLSKey)

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
		CSISocketPath:  *csiSocket,
		CSIStagingDir:  *csiStagingDir,
		CSIPublishDir:  *csiPublishDir,
		Log:            log,
		Metrics:        rec,
	}
	if err := a.Run(ctx, *interval); err != nil && ctx.Err() == nil {
		log.Error("agent stopped", "error", err)
		return 1
	}
	return 0
}

func configureMigration(ctx context.Context, log *slog.Logger, cancel context.CancelFunc, nodeName string, fc *fluxvm.Client, addr, caPath, certPath, keyPath, serverName, stateDir, adapterSocket string) (*migration.Client, migration.SourceDriver, error) {
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
	certWatcher, err := migration.NewCertWatcher(log, caPath, certPath, keyPath)
	if err != nil {
		return nil, nil, err
	}
	go certWatcher.Run(ctx, tlsReloadInterval)
	serverTLS, err := migration.ServerTLSConfig(caPath, certWatcher)
	if err != nil {
		return nil, nil, err
	}
	clientTLS, err := migration.ClientTLSConfig(caPath, certWatcher, serverName)
	if err != nil {
		return nil, nil, err
	}

	var destination migration.DestinationDriver = migration.UnsupportedDestinationDriver{Reason: "no Kairon migration adapter is configured; current FluxVM does not expose a verified live-migration API"}
	var source migration.SourceDriver = migration.UnsupportedSourceDriver{Reason: "no Kairon migration adapter is configured; current FluxVM does not expose a verified live-migration API"}
	if strings.TrimSpace(adapterSocket) != "" {
		adapter := migration.NewAdapter(adapterSocket)
		destination = migration.NetworkAwareDestination{Inner: adapter, Flux: fc}
		source = migration.SourceAdapter{Adapter: adapter}
	} else {
		destination = migration.NetworkAwareDestination{Inner: destination, Flux: fc}
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

// configureConsole starts the VNC console relay listener (see
// internal/consoleproxy) unless consoleToken is empty, in which case the
// feature is simply off -- same opt-in-via-configuration posture as
// migration, no separate --console-enabled flag needed.
//
// Unlike the migration listener (a startup-time fail-closed check, since
// misconfigured mTLS material should stop the node before it reconciles
// anything), a console bind failure must NOT take the whole node agent
// down with it -- console is a purely optional, add-on capability, and
// e.g. a port already in use on a shared host should just mean "no
// console today," not "no Machine reconciliation either."
func configureConsole(ctx context.Context, log *slog.Logger, fc *fluxvm.Client, addr, token, tlsCertPath, tlsKeyPath string) {
	if strings.TrimSpace(token) == "" {
		log.Warn("VNC console relay disabled; set --console-token/$KAIRON_NODE_CONSOLE_TOKEN to enable")
		return
	}
	var tlsConfig *tls.Config
	certSet, keySet := strings.TrimSpace(tlsCertPath) != "", strings.TrimSpace(tlsKeyPath) != ""
	switch {
	case certSet != keySet:
		log.Error("VNC console relay disabled: --console-tls-cert and --console-tls-key must be set together")
		return
	case certSet && keySet:
		watcher, err := tlsreload.New(log, tlsCertPath, tlsKeyPath)
		if err != nil {
			log.Error("VNC console relay disabled: failed to load TLS keypair", "error", err)
			return
		}
		go watcher.Run(ctx, tlsReloadInterval)
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: watcher.GetCertificate}
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("VNC console relay disabled: failed to bind", "address", addr, "error", err)
		return
	}
	server := &http.Server{
		Handler:           (&consoleproxy.Server{Flux: fc, Token: token}).Handler(),
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 5 * time.Second,
		// No WriteTimeout/IdleTimeout: a VNC session is a long-lived
		// streaming connection, not a short request/response cycle.
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		log.Info("VNC console relay listening", "address", addr, "tls", tlsConfig != nil)
		var serveErr error
		if tlsConfig != nil {
			// certFile/keyFile are intentionally empty: the certificate is
			// already loaded into server.TLSConfig above.
			serveErr = server.ServeTLS(listener, "", "")
		} else {
			serveErr = server.Serve(listener)
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			log.Error("VNC console relay stopped", "error", serveErr)
		}
	}()
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
