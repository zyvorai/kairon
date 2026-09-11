package controller

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
	"github.com/zyvorai/kairon/internal/scheduler"
)

func TestReconcileSchedulesMachine(t *testing.T) {
	var patchedNode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_ = json.NewEncoder(w).Encode(model.MachineList{Items: []model.Machine{{
				Metadata: model.ObjectMeta{Name: "db", Namespace: "prod", Generation: 3},
				Spec:     model.MachineSpec{PowerState: "Running"},
			}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			var n model.Node
			n.Metadata.Name = "worker-1"
			n.Metadata.Labels = map[string]string{model.CapableLabel: "true"}
			n.Status.Conditions = []model.NodeCondition{{Type: "Ready", Status: "True"}}
			_ = json.NewEncoder(w).Encode(model.NodeList{Items: []model.Node{n}})
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/db":
			var p map[string]map[string]string
			_ = json.NewDecoder(r.Body).Decode(&p)
			patchedNode = p["spec"]["nodeName"]
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/prod/machines/db/status":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/namespaces/prod/events":
			w.WriteHeader(http.StatusCreated)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	kc, _ := kube.New(srv.URL, "", "", false)
	kc.HTTP = srv.Client()
	ctl := &Controller{Kube: kc, Scheduler: scheduler.Scheduler{RequireCapableLabel: true}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if patchedNode != "worker-1" {
		t.Fatalf("scheduled node = %q", patchedNode)
	}
}
