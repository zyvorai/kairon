package controller

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
	"github.com/zyvorai/kairon/internal/scheduler"
)

func TestFenceReleasesUnhealthyNode(t *testing.T) {
	var cleared bool
	past := strconv.FormatInt(time.Now().Add(-2*time.Minute).Unix(), 10)
	machine := model.Machine{
		Metadata: model.ObjectMeta{
			Name: "db", Namespace: "prod",
			Annotations: map[string]string{model.AnnotationFenceSince: past},
		},
		Spec:   model.MachineSpec{NodeName: "dead", PowerState: "Running"},
		Status: model.MachineStatus{Phase: "Running", RuntimeID: "vm-1"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machinedisruptionbudgets":
			_ = json.NewEncoder(w).Encode(model.MachineDisruptionBudgetList{})
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{machine}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(model.NodeList{Items: []model.Node{}})
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/db":
			var p map[string]any
			_ = json.NewDecoder(r.Body).Decode(&p)
			if spec, ok := p["spec"].(map[string]any); ok {
				if _, has := spec["nodeName"]; has {
					cleared = true
					machine.Spec.NodeName = ""
				}
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/db/status":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/namespaces/prod/events":
			w.WriteHeader(http.StatusCreated)
		default:
			http.Error(w, "unexpected "+r.URL.Path, 404)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: false}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), FenceGrace: time.Second}
	if err := ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !cleared {
		t.Fatal("expected nodeName cleared after fence grace")
	}
}
