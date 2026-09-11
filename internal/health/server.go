package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

type Server struct {
	ready   atomic.Bool
	Metrics *Metrics
}

type Metrics struct {
	MachinesScheduled atomic.Int64
	MachinesFenced    atomic.Int64
	ReconcileErrors   atomic.Int64
}

func (s *Server) SetReady(v bool) { s.ready.Store(v) }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if !s.ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ready": s.ready.Load()})
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		m := s.Metrics
		if m == nil {
			m = &Metrics{}
		}
		_, _ = fmt.Fprintf(w, "# HELP kairon_machines_scheduled_total Machines successfully scheduled.\n")
		_, _ = fmt.Fprintf(w, "# TYPE kairon_machines_scheduled_total counter\n")
		_, _ = fmt.Fprintf(w, "kairon_machines_scheduled_total %d\n", m.MachinesScheduled.Load())
		_, _ = fmt.Fprintf(w, "# HELP kairon_machines_fenced_total Machines fenced from unhealthy nodes.\n")
		_, _ = fmt.Fprintf(w, "# TYPE kairon_machines_fenced_total counter\n")
		_, _ = fmt.Fprintf(w, "kairon_machines_fenced_total %d\n", m.MachinesFenced.Load())
		_, _ = fmt.Fprintf(w, "# HELP kairon_reconcile_errors_total Controller reconcile errors.\n")
		_, _ = fmt.Fprintf(w, "# TYPE kairon_reconcile_errors_total counter\n")
		_, _ = fmt.Fprintf(w, "kairon_reconcile_errors_total %d\n", m.ReconcileErrors.Load())
	})
	return mux
}

func (s *Server) Run(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	err := srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
