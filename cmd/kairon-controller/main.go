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

	"github.com/zyvorai/kairon/internal/admission"
	"github.com/zyvorai/kairon/internal/controller"
	"github.com/zyvorai/kairon/internal/health"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/scheduler"
	"github.com/zyvorai/kairon/internal/storage"
)

var version = "dev"

func main() {
	interval := flag.Duration("interval", 5*time.Second, "reconciliation interval")
	healthAddr := flag.String("health-addr", ":8080", "health/metrics server address")
	webhookAddr := flag.String("webhook-addr", "", "optional validating webhook listen address (e.g. :9443)")
	requireLabel := flag.Bool("require-capable-label", true, "only schedule onto nodes labeled kairon.zyvor.dev/capable=true")
	fenceGrace := flag.Duration("fence-grace", 60*time.Second, "how long a node may be NotReady before Machines are fenced and rescheduled")
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
	metrics := &health.Metrics{}
	hs := &health.Server{Metrics: metrics}
	go func() {
		if err := hs.Run(ctx, *healthAddr); err != nil && err != http.ErrServerClosed {
			log.Error("health server", "error", err)
		}
	}()
	if *webhookAddr != "" {
		go func() {
			mux := http.NewServeMux()
			mux.Handle("/validate", admission.Handler{})
			srv := &http.Server{Addr: *webhookAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
			go func() {
				<-ctx.Done()
				c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = srv.Shutdown(c)
			}()
			log.Info("admission webhook listening", "addr", *webhookAddr)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Error("webhook server", "error", err)
			}
		}()
	}
	ctl := &controller.Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: *requireLabel}, Log: log, FenceGrace: *fenceGrace, Metrics: metrics}
	store := &storage.Reconciler{Kube: kc, Log: log}
	hs.SetReady(true)
	go func() {
		if err := store.Run(ctx, *interval); err != nil && ctx.Err() == nil {
			log.Error("storage reconciler stopped", "error", err)
		}
	}()
	if err := ctl.Run(ctx, *interval); err != nil && ctx.Err() == nil {
		log.Error("controller stopped", "error", err)
		os.Exit(1)
	}
}
