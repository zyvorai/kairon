// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"fmt"

	"github.com/zyvorai/kairon/internal/model"
)

const (
	// StorageDomainLabel and NetworkDomainLabel are opt-in node labels an
	// operator sets to tell migrationPreflight which nodes actually share
	// storage/network reachability -- Kairon has no other way to know
	// this (see internal/model.Node's doc comment: no storage/network
	// capability fields exist on a Node, only generic labels/addresses/
	// conditions, the same data spec.placement.nodeSelector/affinity
	// already work with). Two nodes with the SAME value for one of these
	// labels are asserted, by the operator, to be compatible; an empty or
	// absent value on either side means "can't confirm either way," not
	// "incompatible."
	StorageDomainLabel = "kairon.zyvor.dev/storage-domain"
	NetworkDomainLabel = "kairon.zyvor.dev/network-domain"
)

// migrationPreflight closes part of the "storage/network migration
// preflight" gap: docs/runbook-multi-host-migration-test.md's own
// Prerequisites table documents that both hosts need "a shared filesystem
// mount at an identical path" and "L2/L3 network reachability" for a
// migration to actually work -- today that's pure, unchecked operator
// discipline (Kairon neither copies disk content nor guest network state
// between nodes in either cold or live migration, see
// docs/architecture.md's Cold/Live migration sections). This can only
// catch a *confirmed* mismatch (both nodes opted in with StorageDomainLabel/
// NetworkDomainLabel and they disagree) -- it cannot prove compatibility
// when the labels aren't set, since Kairon has no other source of truth
// for node storage/network topology. That's a real, current limit, not
// an oversight: see docs/guides/machine-fencing.md.
//
// Checked against the chosen migration target only, not searched across
// every candidate -- a first cut. If the winning candidate fails
// preflight, the migration is blocked outright rather than silently
// retrying a different node.
func migrationPreflight(source, target model.Node) string {
	if reason := domainMismatch(StorageDomainLabel, "storage", source, target); reason != "" {
		return reason
	}
	if reason := domainMismatch(NetworkDomainLabel, "network", source, target); reason != "" {
		return reason
	}
	return ""
}

func domainMismatch(label, kind string, source, target model.Node) string {
	sv, sok := source.Metadata.Labels[label]
	tv, tok := target.Metadata.Labels[label]
	if !sok || !tok || sv == "" || tv == "" {
		return "" // can't confirm compatibility either way -- not a new hard requirement
	}
	if sv != tv {
		return fmt.Sprintf("%s domain mismatch: source node %q has %s=%q, target node %q has %s=%q -- these nodes are not asserted to share storage/network, see docs/guides/machine-fencing.md", kind, source.Metadata.Name, label, sv, target.Metadata.Name, label, tv)
	}
	return ""
}
