// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const KindMachineDisruptionBudget = "MachineDisruptionBudget"

// MachineDisruptionBudget caps how many Machines matching Selector
// `kaironctl evacuate` is willing to disrupt at once -- a client-side
// courtesy check, not a server-side admission guarantee. Nothing reconciles
// this object or enforces it if a MachineMigration is created directly
// through the API instead of via `kaironctl evacuate`; see
// docs/guides/machine-disruption-budgets.md.
type MachineDisruptionBudget struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta                  `json:"metadata"`
	Spec     MachineDisruptionBudgetSpec `json:"spec"`
}

func (b MachineDisruptionBudget) Namespace() string {
	return b.Metadata.Namespace
}

type MachineDisruptionBudgetList struct {
	TypeMeta `json:",inline"`
	Items    []MachineDisruptionBudget `json:"items"`
}

type MachineDisruptionBudgetSpec struct {
	Selector map[string]string `json:"selector"`
	// Exactly one of MinAvailable/MaxUnavailable is set. Each is either a
	// plain integer or a "N%" string, evaluated against the number of
	// Machines currently matching Selector -- same shape and rounding
	// rules as a real Kubernetes PodDisruptionBudget.
	MinAvailable   string `json:"minAvailable,omitempty"`
	MaxUnavailable string `json:"maxUnavailable,omitempty"`
}

// ParseIntOrPercent resolves a plain integer or "N%" string against total,
// rounding a percentage up -- matching Kubernetes' own PodDisruptionBudget
// rounding rule for minAvailable/maxUnavailable.
func ParseIntOrPercent(v string, total int) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, fmt.Errorf("value is empty")
	}
	if digits, ok := strings.CutSuffix(v, "%"); ok {
		pct, err := strconv.Atoi(digits)
		if err != nil || pct < 0 || pct > 100 {
			return 0, fmt.Errorf("invalid percentage %q", v)
		}
		return int(math.Ceil(float64(pct) / 100 * float64(total))), nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid integer %q", v)
	}
	return n, nil
}

// DesiredHealthy returns how many Machines matching this budget's Selector
// must remain healthy, given total currently match it.
func (s MachineDisruptionBudgetSpec) DesiredHealthy(total int) (int, error) {
	switch {
	case s.MinAvailable != "" && s.MaxUnavailable != "":
		return 0, fmt.Errorf("exactly one of minAvailable/maxUnavailable must be set, not both")
	case s.MinAvailable != "":
		return ParseIntOrPercent(s.MinAvailable, total)
	case s.MaxUnavailable != "":
		maxUnavailable, err := ParseIntOrPercent(s.MaxUnavailable, total)
		if err != nil {
			return 0, err
		}
		return total - maxUnavailable, nil
	default:
		return 0, fmt.Errorf("one of minAvailable/maxUnavailable must be set")
	}
}
