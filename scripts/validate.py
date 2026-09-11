#!/usr/bin/env python3
# Copyright 2026 Zyvor · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0

from pathlib import Path
import sys
import yaml

root = Path(__file__).resolve().parents[1]
errors = []

def fail(msg):
    errors.append(msg)

required = [
    "go.mod", "LICENSE", "README.md", "VERSION", "RELEASE_NOTES.md",
    "deploy/crd.yaml", "deploy/rbac.yaml", "deploy/controller.yaml", "deploy/node.yaml", "deploy/ui.yaml",
    "charts/kairon/Chart.yaml", "charts/kairon/values.yaml", ".github/workflows/ci.yml",
]
for f in required:
    if not (root / f).exists():
        fail(f"missing {f}")

raw_yaml = [
    root / "deploy/crd.yaml", root / "deploy/rbac.yaml", root / "deploy/controller.yaml", root / "deploy/node.yaml", root / "deploy/ui.yaml",
] + sorted((root / "examples").glob("*.yaml")) + sorted((root / "charts/kairon/crds").glob("*.yaml"))
for f in raw_yaml:
    try:
        docs = list(yaml.safe_load_all(f.read_text()))
        if not docs or any(d is None for d in docs):
            fail(f"empty YAML document in {f.relative_to(root)}")
    except Exception as e:
        fail(f"invalid YAML {f.relative_to(root)}: {e}")

try:
    crds = list(yaml.safe_load_all((root / "deploy/crd.yaml").read_text()))
    names = {d["metadata"]["name"] for d in crds}
    expected = {
        "machines.kairon.zyvor.dev",
        "machinemigrations.kairon.zyvor.dev",
        "machinesnapshots.kairon.zyvor.dev",
        "machinenetworkpolicies.kairon.zyvor.dev",
        "networksecuritygroups.kairon.zyvor.dev",
    }
    if names != expected:
        fail(f"unexpected CRD set: {sorted(names)}")
    for d in crds:
        if d["spec"]["group"] != "kairon.zyvor.dev":
            fail(f"unexpected group for {d['metadata']['name']}")
        v = d["spec"]["versions"][0]
        if v["name"] != "v1alpha1" or "status" not in v.get("subresources", {}):
            fail(f"{d['metadata']['name']} must expose v1alpha1 + status")
    migration = next(d for d in crds if d["metadata"]["name"] == "machinemigrations.kairon.zyvor.dev")
    props = migration["spec"]["versions"][0]["schema"]["openAPIV3Schema"]["properties"]
    if "destination" in props["spec"]["properties"]:
        fail("MachineMigration.spec.destination must not exist in v0.3")
    migration_status = props["status"]["properties"]
    for field in ["sessionID", "transferID", "transferPhase", "backend"]:
        if field not in migration_status:
            fail(f"MachineMigration.status missing {field}")
except Exception as e:
    fail(f"CRD semantic check failed: {e}")

try:
    chart = yaml.safe_load((root / "charts/kairon/Chart.yaml").read_text())
    if chart.get("version") != "0.3.0" or chart.get("appVersion") != "0.3.0":
        fail("Helm chart version/appVersion must be 0.3.0")
except Exception as e:
    fail(f"Chart semantic check failed: {e}")

if (root / "VERSION").read_text().strip() != "v0.3.0":
    fail("VERSION must be v0.3.0")

readme = (root / "README.md").read_text()
for needle in [
    "without KubeVirt", "without libvirt", "FluxVM", "Apache-2.0", "Production gaps",
    "MachineMigration", "MachineSnapshot", "adopt-only", "ResourceClaim", "vfio_devices",
    "MachineNetworkPolicy", "Network Fabric",
]:
    if needle not in readme:
        fail(f"README missing {needle!r}")

for f in [
    "docs/network-fabric.md",
    "docs/tutorials/network-fabric.md",
    "docs/guides/machine-network.md",
    "docs/guides/network-policy.md",
    "examples/network-fabric-machine.yaml",
]:
    if not (root / f).exists():
        fail(f"missing {f}")

if errors:
    print("VALIDATION FAILED")
    for e in errors:
        print(" -", e)
    sys.exit(1)
print("validation: OK")
