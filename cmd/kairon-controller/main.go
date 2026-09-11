package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zyvorai/kairon/internal/controller"
	"github.com/zyvorai/kairon/internal/health"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/scheduler"
)

var version = "dev"

func main() {
	interval := flag.Duration("interval", 5*time.Second, "reconciliation interval")
	healthAddr := flag.String("health-addr", ":8080", "health server address")
	requireLabel := flag.Bool("require-capable-label", true, "only schedule onto nodes labeled kairon.zyvor.dev/capable=true")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		println(version)
		return
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	kc, err := kube.FromEnvironment()
	if err != nil {
		log.Error("kubernetes client", "error", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	hs := &health.Server{}
	go func() {
		if err := hs.Run(ctx, *healthAddr); err != nil && err != http.ErrServerClosed {
			log.Error("health server", "error", err)
		}
	}()
	ctl := &controller.Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: *requireLabel}, Log: log}
	hs.SetReady(true)
	if err := ctl.Run(ctx, *interval); err != nil && ctx.Err() == nil {
		log.Error("controller stopped", "error", err)
		os.Exit(1)
	}
}
