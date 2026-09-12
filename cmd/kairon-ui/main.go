// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

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
	if len(os.Args) >= 2 && os.Args[1] == "-hash-password" {
		return hashPassword(os.Args[2:])
	}
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

	users, err := loadUsers(os.Getenv("KAIRON_UI_USERS_JSON"), os.Getenv("KAIRON_UI_DEFAULT_ADMIN_PASSWORD"))
	if err != nil {
		log.Error("loading users", "error", err)
		return 1
	}
	if len(users) > 0 && os.Getenv("KAIRON_UI_SESSION_SECRET") == "" {
		log.Error("secure startup refused", "reason", "$KAIRON_UI_SESSION_SECRET is required whenever username/password login is configured")
		return 1
	}

	// Secure startup refusal, same posture as every other kairon
	// component's fail-closed validation (e.g. -migration-data-tls
	// requiring all three cert flags together): kairon-ui is a
	// human-facing dashboard capable of creating/deleting Machines and
	// forcing migration recovery actions, so it must not silently come up
	// wide open just because an operator forgot to set a token or a user.
	if *token == "" && len(users) == 0 && !*allowUnauthenticated {
		log.Error("secure startup refused", "reason", "-token (or $KAIRON_UI_TOKEN), $KAIRON_UI_USERS_JSON, or $KAIRON_UI_DEFAULT_ADMIN_PASSWORD is required unless -allow-unauthenticated is set")
		return 1
	}
	if *token == "" && len(users) == 0 {
		log.Warn("unauthenticated mode enabled -- every /api/v1/... route is open to anyone who can reach this server")
	}

	kc, err := kube.FromEnvironment()
	if err != nil {
		log.Error("kubernetes client", "error", err)
		return 1
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	srv := &uiapi.Server{Kube: kc, Log: log, Token: *token, WebDir: *webDir, Users: users, SessionSecret: []byte(os.Getenv("KAIRON_UI_SESSION_SECRET"))}
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

	log.Info("kairon-ui listening", "address", *listenAddr, "webDir", *webDir, "authenticated", *token != "" || len(users) > 0, "loginUsers", len(users))
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

// loadUsers builds the operator account list from $KAIRON_UI_USERS_JSON
// (pre-hashed accounts an operator configured directly) plus, if set, one
// additional account bcrypt-hashed right here from
// $KAIRON_UI_DEFAULT_ADMIN_PASSWORD -- the Helm-generated default admin
// password (see charts/kairon/templates/all.yaml). This is the only place
// a plaintext password ever exists in this process; every
// operator-configured account arrives already hashed.
func loadUsers(usersJSON, defaultAdminPassword string) ([]uiapi.User, error) {
	var users []uiapi.User
	if usersJSON != "" {
		if err := json.Unmarshal([]byte(usersJSON), &users); err != nil {
			return nil, fmt.Errorf("parse $KAIRON_UI_USERS_JSON: %w", err)
		}
	}
	if defaultAdminPassword != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(defaultAdminPassword), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("hash default admin password: %w", err)
		}
		users = append(users, uiapi.User{Username: "admin", PasswordHash: string(hash)})
	}
	return users, nil
}

// hashPassword implements `kairon-ui -hash-password PASSWORD`: prints a
// bcrypt hash suitable for ui.auth.users' passwordHash field and exits,
// without starting the server. A positional arg (not a stdin prompt) to
// avoid a TTY dependency; documented as better piped from a file/heredoc
// than typed, to avoid shell history for a real password.
func hashPassword(args []string) int {
	if len(args) != 1 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "usage: kairon-ui -hash-password PASSWORD")
		return 2
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(args[0]), bcrypt.DefaultCost)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println(string(hash))
	return 0
}
