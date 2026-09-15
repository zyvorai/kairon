// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/zyvorai/kairon/internal/admission"
	"github.com/zyvorai/kairon/internal/conversion"
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
	mux.HandleFunc("POST /validate-machine", admission.Handler(c.Log, c.validateMachine, c.observeWebhookDecision))
	mux.HandleFunc("POST /validate-machinemigration", admission.Handler(c.Log, c.validateMachineMigration, c.observeWebhookDecision))
	// /convert/machinequotas is a scaffold, not live enforcement: no
	// MachineQuota CRD registers a second version yet, so the API server
	// never actually calls this route today. It exists, and is tested,
	// so cutting a real kairon.zyvor.dev/v1beta1 later is "wire the CRD's
	// spec.conversion at that version," not "build a conversion webhook
	// from scratch" -- see docs/guides/crd-versioning.md and
	// internal/conversion's package doc comment.
	mux.HandleFunc("POST /convert/machinequotas", conversion.Handler(c.Log, model.KindMachineQuota, conversion.ConvertMachineQuota))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

// observeWebhookDecision is the admission.Handler observe callback for
// both routes above -- one choke point for kairon_webhook_decisions_total
// regardless of which resource/validator produced the decision.
func (c *Controller) observeWebhookDecision(resource, operation string, allowed bool) {
	if c.Metrics != nil {
		c.Metrics.ObserveWebhookDecision(resource, operation, allowed)
	}
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

// validateMachine enforces MachineQuota on a Machine CREATE (admitQuota,
// the exact same check the reconcile loop's own scheduling-time admission
// uses) and, separately, on an UPDATE that grows an already-scheduled
// Machine's spec.resources (admitQuotaResize) -- closing the gap
// documented until now as a real, accepted limitation: "an
// already-scheduled Machine growing past quota via hotplug isn't caught
// by either the webhook or the reconcile loop."
//
// The two cases aren't symmetric the way CREATE-vs-nothing might suggest.
// For CREATE, this webhook is defense-in-depth: the reconcile loop's own
// scheduling-time admitQuota call already backstops the exact same
// decision, so the webhook can never become *stricter* than what it
// backstops. For a resize UPDATE, there is no reconcile-loop equivalent to
// backstop at all -- hotplug is entirely kairon-node's agent's own
// concern (internal/agent/hotplug.go), reconciled per-node with no
// cluster-wide MachineQuota visibility, and kairon-controller's reconcile
// loop only ever calls admitQuota once, at initial scheduling. So unlike
// the CREATE case, this IS new enforcement, not just closing a bypass
// around something already enforced elsewhere -- with webhook.enabled
// false (the default), a resize past quota still isn't caught anywhere,
// same as before this existed.
func (c *Controller) validateMachine(r *http.Request, req *admission.Request) admission.Decision {
	if req.Resource.Resource != "machines" {
		return admission.Allow()
	}
	switch req.Operation {
	case admission.OperationCreate:
		return c.validateMachineCreate(r, req)
	case admission.OperationUpdate:
		return c.validateMachineResize(r, req)
	default:
		return admission.Allow()
	}
}

// imageSourceDigestPrefix/validateImageSource duplicate
// internal/agent's own digestPrefix/validateImageSource exactly -- see
// this function's call site for why this is a deliberate duplication,
// not a shared import.
const imageSourceDigestPrefix = "sha256:"

func validateImageSource(img model.ImageSpec) error {
	if img.Source == nil {
		return nil
	}
	if strings.TrimSpace(img.Source.HTTPURL) == "" {
		return fmt.Errorf("spec.image.source.httpURL is required when spec.image.source is set")
	}
	if !strings.HasPrefix(img.Source.HTTPURL, "http://") && !strings.HasPrefix(img.Source.HTTPURL, "https://") {
		return fmt.Errorf("spec.image.source.httpURL %q must be an http:// or https:// URL", img.Source.HTTPURL)
	}
	hexDigest, ok := strings.CutPrefix(img.Digest, imageSourceDigestPrefix)
	if !ok || len(hexDigest) != sha256.Size*2 {
		return fmt.Errorf("spec.image.digest must be set as %q plus a 64-character hex digest when spec.image.source is set", imageSourceDigestPrefix)
	}
	return nil
}

func (c *Controller) validateMachineCreate(r *http.Request, req *admission.Request) admission.Decision {
	var m model.Machine
	if err := json.Unmarshal(req.Object, &m); err != nil {
		return admission.Deny(fmt.Sprintf("decode Machine: %v", err))
	}
	// Defense in depth: kairon-node's own reconcile also rejects a
	// malformed spec.image.source (internal/agent's own
	// validateImageSource, the same logic duplicated here rather than
	// cross-imported -- kairon-controller and kairon-node are separate
	// binaries with deliberately separate dependency footprints, the
	// same small-helper-duplication convention cmd/kairon-csi-node and
	// cmd/kairon-csi-controller's own unixSocketPath already follows),
	// but that only surfaces as a stuck Machine status, not an
	// immediate, actionable API error.
	if err := validateImageSource(m.Spec.Image); err != nil {
		return admission.Deny(err.Error())
	}
	trackers, ok, err := QuotaTrackersForNamespace(r.Context(), c.Kube, req.Namespace)
	if err != nil {
		return admission.Deny(err.Error())
	}
	if !ok {
		return admission.Allow()
	}
	if blocker := admitQuota(trackers, m); blocker != "" {
		return admission.Deny(blocker)
	}
	return admission.Allow()
}

// validateMachineResize only ever denies a *growing* resize of an
// already-scheduled Machine -- see validateMachine's own doc comment for
// why this is the one quota check with no reconcile-loop backstop. A
// Machine that isn't yet scheduled falls through to Allow(): the
// reconcile loop's own scheduling-time admitQuota call covers that case
// exactly like a CREATE would (machineCountsTowardQuota mirrors
// buildQuotaTrackers' own seed-pass predicate exactly, so "not yet
// scheduled" here means the same thing it means there). A shrink or
// no-change UPDATE is also always allowed, without even listing quotas:
// it can only ever lower usage below what was already accepted.
func (c *Controller) validateMachineResize(r *http.Request, req *admission.Request) admission.Decision {
	var oldM, newM model.Machine
	if err := json.Unmarshal(req.OldObject, &oldM); err != nil {
		return admission.Deny(fmt.Sprintf("decode old Machine: %v", err))
	}
	if err := json.Unmarshal(req.Object, &newM); err != nil {
		return admission.Deny(fmt.Sprintf("decode Machine: %v", err))
	}
	if !MachineCountsTowardQuota(oldM) {
		return admission.Allow()
	}
	oldCPU, oldMem := MachineFootprint(oldM)
	newCPU, newMem := MachineFootprint(newM)
	if newCPU <= oldCPU && newMem <= oldMem {
		return admission.Allow()
	}
	trackers, ok, err := QuotaTrackersForNamespace(r.Context(), c.Kube, req.Namespace)
	if err != nil {
		return admission.Deny(err.Error())
	}
	if !ok {
		return admission.Allow()
	}
	if blocker := AdmitQuotaResize(trackers, req.Namespace, oldCPU, newCPU, oldMem, newMem); blocker != "" {
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
