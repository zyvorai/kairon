// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package model

// ConfigMap is the minimal core/v1 ConfigMap shape Kairon needs -- not a
// general-purpose client. Used by internal/uiapi.Server to share session
// revocation, login-lockout, and console-ticket state across kairon-ui
// replicas (see internal/uiapi/sharedstate.go); Data is a plain string
// key/value map, merge-patched one key at a time so concurrent writers
// touching different keys never conflict.
type ConfigMap struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta        `json:"metadata"`
	Data     map[string]string `json:"data,omitempty"`
}

// Secret is the minimal core/v1 Secret shape Kairon needs to read back --
// not a general-purpose client. Data values arrive base64-encoded exactly
// as the Kubernetes API stores them; encoding/json decodes a []byte field
// from a base64 JSON string automatically, so callers never handle that
// themselves. Used by internal/uiapi.Server to notice a password change
// (or account list change) made by a different kairon-ui replica.
type Secret struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta        `json:"metadata"`
	Data     map[string][]byte `json:"data,omitempty"`
}

// Lease is the minimal coordination.k8s.io/v1 Lease shape Kairon needs for
// kairon-controller's own leader election (see internal/leaderelection) --
// not a general-purpose client. Pointer fields mirror the real API exactly
// (an absent field there is a meaningful "never held"/"unset", not a zero
// value) and round-trip through encoding/json's omitempty cleanly.
type Lease struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta `json:"metadata"`
	Spec     LeaseSpec  `json:"spec,omitempty"`
}

type LeaseSpec struct {
	HolderIdentity       *string    `json:"holderIdentity,omitempty"`
	LeaseDurationSeconds *int32     `json:"leaseDurationSeconds,omitempty"`
	AcquireTime          *MicroTime `json:"acquireTime,omitempty"`
	RenewTime            *MicroTime `json:"renewTime,omitempty"`
	LeaseTransitions     *int32     `json:"leaseTransitions,omitempty"`
}
