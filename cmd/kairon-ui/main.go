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

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/uiapi"
)

var version = "dev"

func main() {
	os.Exit(run())
}

// run returns the process exit code rather than calling os.Exit directly,
// so every deferred cleanup (e.g. cancel()) actually runs before exit.
func run() int {
	listenAddr := flag.String("listen", env("KAIRON_UI_LISTEN", ":8082"), "HTTP listen address (serves both /api/v1/... and the built web UI)")
	webDir := flag.String("web-dir", env("KAIRON_UI_WEB_DIR", ""), "directory containing the built web/dist SPA; empty serves API-only")
	token := flag.String("token", os.Getenv("KAIRON_UI_TOKEN"), "static bearer token required on every /api/v1/... request (default: $KAIRON_UI_TOKEN)")
	allowUnauthenticated := flag.Bool("allow-unauthenticated", env("KAIRON_UI_ALLOW_UNAUTHENTICATED", "false") == "true", "start without a token -- local development only, refused by default")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return 0
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// Secure startup refusal, same posture as every other kairon
	// component's fail-closed validation (e.g. -migration-data-tls
	// requiring all three cert flags together): kairon-ui is a
	// human-facing dashboard capable of creating/deleting Machines and
	// forcing migration recovery actions, so it must not silently come up
	// wide open just because an operator forgot to set a token.
	if *token == "" && !*allowUnauthenticated {
		log.Error("secure startup refused", "reason", "-token (or $KAIRON_UI_TOKEN) is required unless -allow-unauthenticated is set")
		return 1
	}
	if *token == "" {
		log.Warn("unauthenticated mode enabled -- every /api/v1/... route is open to anyone who can reach this server")
	}

	kc, err := kube.FromEnvironment()
	if err != nil {
		log.Error("kubernetes client", "error", err)
		return 1
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	srv := &uiapi.Server{Kube: kc, Log: log, Token: *token, WebDir: *webDir}
	httpServer := &http.Server{
		Addr:              *listenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	log.Info("kairon-ui listening", "address", *listenAddr, "webDir", *webDir, "authenticated", *token != "")
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("http server", "error", err)
		return 1
	}
	return 0
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
