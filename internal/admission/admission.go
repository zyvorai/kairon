// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package admission hand-rolls just the slice of the Kubernetes
// admission.k8s.io/v1 AdmissionReview wire format kairon-controller's
// webhook (internal/controller/webhook.go) actually needs -- this project
// has no client-go/k8s.io/api dependency at all (see go.mod), and pulling
// one in just for this one small, stable, well-documented JSON schema
// would be a large new dependency surface for a handful of fields. See
// https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/#request
// for the authoritative shape; this is a deliberate subset.
package admission

import "encoding/json"

const APIVersion = "admission.k8s.io/v1"

// GroupVersionResource identifies the REST resource being admitted (e.g.
// {Group: "kairon.zyvor.dev", Version: "v1alpha1", Resource: "machines"}).
type GroupVersionResource struct {
	Group    string `json:"group"`
	Version  string `json:"version"`
	Resource string `json:"resource"`
}

// Operation values the API server sends -- only the two kairon-controller's
// webhook ever needs to distinguish.
const (
	OperationCreate = "CREATE"
	OperationUpdate = "UPDATE"
)

// Request is the subset of AdmissionRequest kairon-controller's handlers
// read: which resource, in which namespace, which operation, and the
// object's raw JSON to decode into a model.* type themselves (this package
// has no opinion on what's inside Object -- that's the caller's model
// type).
type Request struct {
	UID       string               `json:"uid"`
	Resource  GroupVersionResource `json:"resource"`
	Namespace string               `json:"namespace"`
	Operation string               `json:"operation"`
	Object    json.RawMessage      `json:"object"`
}

// Response is the subset of AdmissionResponse this package writes back.
type Response struct {
	UID     string  `json:"uid"`
	Allowed bool    `json:"allowed"`
	Status  *Status `json:"status,omitempty"`
}

type Status struct {
	Message string `json:"message,omitempty"`
}

// Review is the AdmissionReview envelope both directions use -- Request
// set on the way in, Response set on the way out.
type Review struct {
	APIVersion string    `json:"apiVersion"`
	Kind       string    `json:"kind"`
	Request    *Request  `json:"request,omitempty"`
	Response   *Response `json:"response,omitempty"`
}

// Decision is what a Validator returns: Allowed, and -- only meaningful
// when Allowed is false -- a human-readable Reason the API server surfaces
// back to whoever's write got rejected (e.g. in `kubectl apply`'s own
// error output).
type Decision struct {
	Allowed bool
	Reason  string
}

func Allow() Decision { return Decision{Allowed: true} }
func Deny(reason string) Decision {
	return Decision{Allowed: false, Reason: reason}
}
