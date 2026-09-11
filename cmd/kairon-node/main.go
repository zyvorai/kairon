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

	"github.com/zyvorai/kairon/internal/agent"
	"github.com/zyvorai/kairon/internal/fluxvm"
	"github.com/zyvorai/kairon/internal/health"
	"github.com/zyvorai/kairon/internal/kube"
)

var version = "dev"

func main() {
	interval := flag.Duration("interval", 3*time.Second, "reconciliation interval")
	healthAddr := flag.String("health-addr", ":8081", "health server address")
	fluxURL := flag.String("fluxvm-url", env("FLUXVM_URL", "http://127.0.0.1:7788"), "node-local FluxVM URL")
	backend := flag.String("default-backend", env("KAIRON_DEFAULT_BACKEND", "qemu"), "backend used when Machine runtime.backend is auto")
	imageRoot := flag.String("image-root", env("KAIRON_IMAGE_ROOT", "/var/lib/fluxvm/images"), "allowed root for Machine image paths")
	vfioAllowlistRaw := flag.String("vfio-allowlist", env("KAIRON_VFIO_ALLOWLIST", ""), "comma-separated PCI BDFs this node permits for DRA-backed VFIO passthrough")
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
	a := &agent.Agent{NodeName: node, Kube: kc, Flux: fc, DefaultBackend: *backend, ImageRoot: *imageRoot, VFIOAllowlist: vfioAllowlist, Log: log}
	if err := a.Run(ctx, *interval); err != nil && ctx.Err() == nil {
		log.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
