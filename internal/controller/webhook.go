// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/zyvorai/kairon/internal/admission"
	"github.com/zyvorai/kairon/internal/model"
	"github.com/zyvorai/kairon/internal/tlsreload"
)

// WebhookHandler returns the mux for kairon-controller's validating
// admission webhook: closes the two gaps documented on
// MachineQuota/MachineDisruptionBudget (see internal/model/quota.go,
// internal/model/disruption.go) by rejecting the writes that used to just
// slip through -- a Machine create that would immediately push its
// namespace over quota, and a MachineMigration create that would violate a
// MachineDisruptionBudget. Both handlers reuse exactly the same decision
// functions the reconcile loop (admitQuota) and `kaironctl evacuate`
// (AdmitDisruption) already use -- this file is only the HTTP/admission
// wiring around them, no new enforcement logic.
func (c *Controller) WebhookHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /validate-machine", admission.Handler(c.Log, c.validateMachine))
	mux.HandleFunc("POST /validate-machinemigration", admission.Handler(c.Log, c.validateMachineMigration))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

// RunWebhook serves the admission webhook over TLS on addr until ctx is
// canceled. The API server requires HTTPS for webhook calls, so unlike
// internal/health.Server this always needs a certificate -- tlsConfig is
// built once at startup by WebhookTLSConfig (see
// cmd/kairon-controller/main.go), the same "fail fast on a bad cert before
// starting anything" posture as kairon-node's migration TLS setup, rather
// than only discovering a bad cert/key pair on the first admission
// request. certWatcher (WebhookTLSConfig's other return value) is run
// alongside the server so a renewed certificate is picked up without a
// restart -- see internal/tlsreload.
func (c *Controller) RunWebhook(ctx context.Context, addr string, tlsConfig *tls.Config, certWatcher *tlsreload.Watcher, tlsReloadInterval time.Duration) error {
	go certWatcher.Run(ctx, tlsReloadInterval)
	srv := &http.Server{Addr: addr, Handler: c.WebhookHandler(), TLSConfig: tlsConfig}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	err := srv.ListenAndServeTLS("", "")
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// validateMachine only applies to CREATE, matching the reconcile loop's
// own quota semantics exactly: admitQuota (quota.go) is only ever called
// once per Machine, at the moment the scheduler first picks a node for it
// -- an already-scheduled Machine growing via hotplug is never re-checked
// against quota by the reconcile loop either (see quota.go's own doc
// comment: "quota blocks *new* scheduling, it doesn't evict or
// retroactively un-admit anything"). Extending this webhook to also
// enforce quota on UPDATE would make it *stricter* than the reconcile loop
// it's meant to backstop -- a new inconsistency, not a fix -- so it
// deliberately doesn't.
func (c *Controller) validateMachine(r *http.Request, req *admission.Request) admission.Decision {
	if req.Resource.Resource != "machines" || req.Operation != admission.OperationCreate {
		return admission.Allow()
	}
	var m model.Machine
	if err := json.Unmarshal(req.Object, &m); err != nil {
		return admission.Deny(fmt.Sprintf("decode Machine: %v", err))
	}
	ctx := r.Context()
	quotas, err := c.Kube.ListMachineQuotasNamespace(ctx, req.Namespace)
	if err != nil {
		return admission.Deny(fmt.Sprintf("list MachineQuotas: %v", err))
	}
	if len(quotas) == 0 {
		return admission.Allow()
	}
	machines, err := c.Kube.ListMachinesNamespace(ctx, req.Namespace)
	if err != nil {
		return admission.Deny(fmt.Sprintf("list Machines: %v", err))
	}
	trackers, err := buildQuotaTrackers(quotas, machines)
	if err != nil {
		return admission.Deny(err.Error())
	}
	if blocker := admitQuota(trackers, m); blocker != "" {
		return admission.Deny(blocker)
	}
	return admission.Allow()
}

// validateMachineMigration only applies to CREATE: a MachineMigration is
// never mutated in a way that changes which Machine it disrupts after
// creation (see model.MachineMigrationSpec), so there's nothing for
// UPDATE to re-check.
func (c *Controller) validateMachineMigration(r *http.Request, req *admission.Request) admission.Decision {
	if req.Resource.Resource != "machinemigrations" || req.Operation != admission.OperationCreate {
		return admission.Allow()
	}
	var mig model.MachineMigration
	if err := json.Unmarshal(req.Object, &mig); err != nil {
		return admission.Deny(fmt.Sprintf("decode MachineMigration: %v", err))
	}
	ctx := r.Context()
	budgets, err := c.Kube.ListMachineDisruptionBudgetsNamespace(ctx, req.Namespace)
	if err != nil {
		return admission.Deny(fmt.Sprintf("list MachineDisruptionBudgets: %v", err))
	}
	if len(budgets) == 0 {
		return admission.Allow()
	}
	machines, err := c.Kube.ListMachinesNamespace(ctx, req.Namespace)
	if err != nil {
		return admission.Deny(fmt.Sprintf("list Machines: %v", err))
	}
	var target model.Machine
	found := false
	for _, m := range machines {
		if m.Metadata.Name == mig.Spec.MachineName {
			target, found = m, true
			break
		}
	}
	if !found {
		// No matching Machine to check a budget's Selector against --
		// consistent with AdmitDisruption's own "no matching budget,
		// always allowed" behavior for an unmanaged Machine, not a new
		// leniency invented here.
		return admission.Allow()
	}
	migrations, err := c.Kube.ListMachineMigrationsNamespace(ctx, req.Namespace)
	if err != nil {
		return admission.Deny(fmt.Sprintf("list MachineMigrations: %v", err))
	}
	states, err := LoadBudgetStates(budgets, machines, migrations)
	if err != nil {
		return admission.Deny(err.Error())
	}
	if blocker := AdmitDisruption(states, target); blocker != "" {
		return admission.Deny(blocker)
	}
	return admission.Allow()
}

// WebhookTLSConfig loads the webhook's serving certificate into a
// tlsreload.Watcher and builds a *tls.Config around it. Called once by
// cmd/kairon-controller/main.go at startup, before RunWebhook, so a bad
// cert/key pair is a startup failure, not a surprise on the first
// admission request. The returned watcher must be passed to RunWebhook so
// a renewed certificate is picked up without a restart -- see
// internal/tlsreload.
func WebhookTLSConfig(log *slog.Logger, certFile, keyFile string) (*tls.Config, *tlsreload.Watcher, error) {
	watcher, err := tlsreload.New(log, certFile, keyFile)
	if err != nil {
		return nil, nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: watcher.GetCertificate}, watcher, nil
}
