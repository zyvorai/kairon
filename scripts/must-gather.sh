#!/usr/bin/env bash
# Copyright 2026 Zyvor · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0

# Collect a supportability bundle for Kairon (must-gather style).
set -euo pipefail
OUT="${1:-kairon-must-gather-$(date +%Y%m%d%H%M%S)}"
NS="${KAIRON_NAMESPACE:-kairon-system}"
mkdir -p "$OUT"

{
  echo "kairon must-gather"
  echo "date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "namespace: $NS"
  kubectl version --short 2>/dev/null || kubectl version 2>/dev/null || true
} >"$OUT/version.txt"

kubectl get nodes -o wide >"$OUT/nodes.txt" 2>&1 || true
kubectl get machines,machineimages,virtualdisks,machinesnapshots,machinedisruptionbudgets,machinemigrations -A -o wide >"$OUT/machines.txt" 2>&1 || true
kubectl get machines -A -o yaml >"$OUT/machines.yaml" 2>&1 || true
kubectl -n "$NS" get all,cm,sa -o wide >"$OUT/system.txt" 2>&1 || true
kubectl -n "$NS" logs deploy/kairon-controller --all-containers --tail=500 >"$OUT/controller.log" 2>&1 || true
kubectl -n "$NS" logs daemonset/kairon-node --all-containers --tail=200 >"$OUT/node.log" 2>&1 || true
kubectl get events -A --field-selector reason=Fenced,FailedScheduling,Scheduled,MigrationStarted,MigrationFailed --sort-by=.lastTimestamp >"$OUT/events.txt" 2>&1 || true
kubectl get crd -o name | grep kairon.zyvor.dev | while read -r crd; do
  kubectl get "$crd" -A -o yaml >"$OUT/$(basename "$crd").yaml" 2>&1 || true
done

tar -czf "${OUT}.tar.gz" "$OUT"
echo "wrote ${OUT}.tar.gz"
