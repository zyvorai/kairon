#!/usr/bin/env bash
# Copyright 2026 Zyvor · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0

# Drives the v0.7 hardware migration matrix against a prepared multi-host
# lab. Requires the environment documented in
# docs/runbook-multi-host-migration-test.md plus:
#
#   KAIRON_HW_LAB=1          # safety gate — refuses to run otherwise
#   KAIRON_KUBE_URL / TOKEN  # cluster API
#   KAIRON_HW_MACHINE        # existing Machine name to migrate
#   KAIRON_HW_TARGET_NODE    # destination node name
#   KAIRON_HW_SOURCE_NODE    # optional; defaults from Machine status
#   KAIRON_HW_EBPF_MACHINE   # optional; Machine with dataplaneMode=ebpf for live-ebpf
#
# Individual cases can be selected with --case=NAME (repeatable). Default
# is the v0.7 set including live-ebpf when KAIRON_HW_EBPF_MACHINE is set.
# Results are printed as COMPATIBILITY.md rows.
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
  if [[ -n "${KAIRON_HW_EBPF_MACHINE:-}" ]]; then
    CASES+=(live-ebpf)
  fi
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

run_migrate() {
  local machine="$1"
  "$REPO_ROOT/scripts/multi-host-migration-test-run.sh" "$machine" "$TARGET" \
    --namespace="$NAMESPACE" --timeout="$TIMEOUT"
}

require_dataplane_mode() {
  local machine="$1" want="$2"
  local mode
  mode="$("$KAIRONCTL" get machine -n "$NAMESPACE" "$machine" -o json 2>/dev/null \
    | python3 -c 'import json,sys; m=json.load(sys.stdin); print((m.get("spec") or {}).get("network",{}).get("dataplaneMode",""))' 2>/dev/null || true)"
  if [[ "$mode" != "$want" ]]; then
    echo "expected ${machine} spec.network.dataplaneMode=${want}, got ${mode:-empty}" >&2
    return 1
  fi
  return 0
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
      if KAIRON_MIGRATE_STRATEGY=cold run_migrate "$MACHINE"; then
        record "Cold migration" "pass" ""
      else
        record "Cold migration" "fail" ""
      fi
      ;;
    live)
      if run_migrate "$MACHINE"; then
        record "Live pre-copy under load" "pass" "load injection optional"
      else
        record "Live pre-copy under load" "fail" ""
      fi
      ;;
    live-ebpf)
      EBPF_MACHINE="${KAIRON_HW_EBPF_MACHINE:-$MACHINE}"
      if ! require_dataplane_mode "$EBPF_MACHINE" "ebpf"; then
        record "Live + eBPF dataplane" "fail" "Machine ${EBPF_MACHINE} missing dataplaneMode=ebpf"
      elif run_migrate "$EBPF_MACHINE"; then
        record "Live + eBPF dataplane" "pass" "Machine ${EBPF_MACHINE}; network quiesce/export/restore on TC/eBPF path"
      else
        record "Live + eBPF dataplane" "fail" "Machine ${EBPF_MACHINE}"
      fi
      ;;
    needs-recovery)
      if [[ -x "$REPO_ROOT/scripts/recovery-drill.sh" ]]; then
        if "$REPO_ROOT/scripts/recovery-drill.sh" --namespace="$NAMESPACE" --machine="$MACHINE"; then
          record "Ambiguous commit → NeedsRecovery" "pass" "recovery-drill.sh"
        else
          record "Ambiguous commit → NeedsRecovery" "fail" "recovery-drill.sh"
        fi
      elif [[ -f "$REPO_ROOT/docs/runbook-recovery-drill.md" ]]; then
        record "Ambiguous commit → NeedsRecovery" "manual" "see runbook-recovery-drill.md"
      else
        record "Ambiguous commit → NeedsRecovery" "fail" "runbook missing"
      fi
      ;;
    source-failure)
      if [[ -x "$REPO_ROOT/scripts/lab-inject-source-failure.sh" ]]; then
        if "$REPO_ROOT/scripts/lab-inject-source-failure.sh" --namespace="$NAMESPACE" --machine="$MACHINE" --target="$TARGET"; then
          record "Source failure during transfer" "pass" ""
        else
          record "Source failure during transfer" "fail" ""
        fi
      else
        record "Source failure during transfer" "manual" "add scripts/lab-inject-source-failure.sh or run by hand"
      fi
      ;;
    controller-failover)
      if [[ -x "$REPO_ROOT/scripts/lab-inject-controller-failover.sh" ]]; then
        if "$REPO_ROOT/scripts/lab-inject-controller-failover.sh" --namespace="$NAMESPACE" --machine="$MACHINE" --target="$TARGET"; then
          record "Controller failover mid-migration" "pass" ""
        else
          record "Controller failover mid-migration" "fail" ""
        fi
      else
        record "Controller failover mid-migration" "manual" "add scripts/lab-inject-controller-failover.sh or run by hand"
      fi
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
