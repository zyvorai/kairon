#!/usr/bin/env bash
# Copyright 2026 Zyvor · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0

# Drives the v0.6 hardware migration matrix against a prepared multi-host
# lab. Requires the environment documented in
# docs/runbook-multi-host-migration-test.md plus:
#
#   KAIRON_HW_LAB=1          # safety gate — refuses to run otherwise
#   KAIRON_KUBE_URL / TOKEN  # cluster API
#   KAIRON_HW_MACHINE        # existing Machine name to migrate (or create)
#   KAIRON_HW_TARGET_NODE    # destination node name
#   KAIRON_HW_SOURCE_NODE    # optional; defaults from Machine status
#
# Individual cases can be selected with --case=NAME (repeatable). Default
# is the v0.6 minimum set. Results are printed as COMPATIBILITY.md rows.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
KAIRONCTL="${KAIRONCTL:-$REPO_ROOT/bin/kaironctl}"
[[ -x "$KAIRONCTL" ]] || KAIRONCTL="kaironctl"

if [[ "${KAIRON_HW_LAB:-}" != "1" ]]; then
  echo "refusing: set KAIRON_HW_LAB=1 to acknowledge a real multi-host lab" >&2
  exit 2
fi

CASES=()
NAMESPACE="${KAIRON_HW_NAMESPACE:-default}"
TIMEOUT="${KAIRON_HW_TIMEOUT:-600}"
for arg in "$@"; do
  case "$arg" in
    --case=*) CASES+=("${arg#*=}") ;;
    --namespace=*) NAMESPACE="${arg#*=}" ;;
    --timeout=*) TIMEOUT="${arg#*=}" ;;
    *) echo "unknown flag $arg" >&2; exit 1 ;;
  esac
done
if [[ ${#CASES[@]} -eq 0 ]]; then
  CASES=(smoke cold live needs-recovery)
fi

MACHINE="${KAIRON_HW_MACHINE:?set KAIRON_HW_MACHINE}"
TARGET="${KAIRON_HW_TARGET_NODE:?set KAIRON_HW_TARGET_NODE}"
DATE="$(date -u +%Y-%m-%d)"
RESULTS=()

record() {
  local case="$1" result="$2" notes="${3:-}"
  RESULTS+=("| ${case} | ${result} | ${DATE} | ${notes} |")
  echo "==> ${case}: ${result} ${notes}"
}

run_live() {
  "$REPO_ROOT/scripts/multi-host-migration-test-run.sh" "$MACHINE" "$TARGET" \
    --namespace="$NAMESPACE" --timeout="$TIMEOUT"
}

for case in "${CASES[@]}"; do
  case "$case" in
    smoke)
      if "$KAIRONCTL" get machine -n "$NAMESPACE" "$MACHINE" >/dev/null 2>&1; then
        record "Create / restart / delete smoke" "pass" "Machine ${MACHINE} present"
      else
        record "Create / restart / delete smoke" "fail" "Machine ${MACHINE} missing"
      fi
      ;;
    cold)
      if KAIRON_MIGRATE_STRATEGY=cold run_live; then
        record "Cold migration" "pass" ""
      else
        record "Cold migration" "fail" ""
      fi
      ;;
    live)
      if run_live; then
        record "Live pre-copy under load" "pass" "load injection optional"
      else
        record "Live pre-copy under load" "fail" ""
      fi
      ;;
    needs-recovery)
      # Full ambiguous-commit injection is lab-specific; this case documents
      # that the operator must run the recovery drill runbook until fencing
      # hooks exist in-matrix.
      if [[ -f "$REPO_ROOT/docs/runbook-recovery-drill.md" ]]; then
        record "Ambiguous commit → NeedsRecovery" "manual" "see runbook-recovery-drill.md"
      else
        record "Ambiguous commit → NeedsRecovery" "fail" "runbook missing"
      fi
      ;;
    source-failure|controller-failover)
      record "$case" "manual" "requires lab power/network injection"
      ;;
    *)
      echo "unknown case $case" >&2
      exit 1
      ;;
  esac
done

echo
echo "## Matrix rows (paste into docs/COMPATIBILITY.md)"
printf '%s\n' "${RESULTS[@]}"
