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
    "charts/kairon/Chart.yaml", "charts/kairon/values.yaml", "charts/kairon/values-production.yaml", ".github/workflows/ci.yml", ".github/workflows/ci-extra.yml",
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
        "machinedisruptionbudgets.kairon.zyvor.dev",
        "machinequotas.kairon.zyvor.dev",
        "machinesnapshotrestores.kairon.zyvor.dev",
        "machinesets.kairon.zyvor.dev",
        "machinepools.kairon.zyvor.dev",
        "machineclaims.kairon.zyvor.dev",
        "machineinstancetypes.kairon.zyvor.dev",
        "migrationpolicies.kairon.zyvor.dev",
        "machinesnapshotschedules.kairon.zyvor.dev",
    }
    if names != expected:
        fail(f"unexpected CRD set: {sorted(names)}")
    # MachineInstanceType is pure reference/config data (a named
    # cpu/memory shape a Machine looks up, nothing kairon-controller
    # itself ever observes or reports back) -- no status subresource, the
    # same real-world precedent a StorageClass/PriorityClass already sets
    # among Kubernetes' own built-in CRD-shaped resources.
    no_status = {"machineinstancetypes.kairon.zyvor.dev"}
    for d in crds:
        if d["spec"]["group"] != "kairon.zyvor.dev":
            fail(f"unexpected group for {d['metadata']['name']}")
        versions = {v["name"]: v for v in d["spec"]["versions"]}
        v = d["spec"]["versions"][0]
        if v["name"] != "v1beta1" or not v.get("storage"):
            fail(f"{d['metadata']['name']} must store v1beta1 (first version)")
        elif "v1alpha1" not in versions or not versions["v1alpha1"].get("served") or versions["v1alpha1"].get("storage"):
            fail(f"{d['metadata']['name']} must still serve v1alpha1 (not as storage)")
        elif versions["v1alpha1"].get("schema") != v.get("schema"):
            fail(f"{d['metadata']['name']} v1alpha1 and v1beta1 schemas must match (conversion: None)")
        elif d["metadata"]["name"] not in no_status and "status" not in v.get("subresources", {}):
            fail(f"{d['metadata']['name']} must expose v1beta1 + status")
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

chart_crds = {
    d["metadata"]["name"]: d
    for p in sorted((root / "charts/kairon/crds").glob("*.yaml"))
    for d in yaml.safe_load_all(p.read_text())
}
bundle_crds = {d["metadata"]["name"]: d for d in yaml.safe_load_all((root / "deploy/crd.yaml").read_text())}
if chart_crds != bundle_crds:
    drift = sorted(n for n in chart_crds.keys() | bundle_crds.keys() if chart_crds.get(n) != bundle_crds.get(n))
    fail(f"deploy/crd.yaml differs from charts/kairon/crds ({', '.join(drift)}); run `make crds`")

try:
    chart = yaml.safe_load((root / "charts/kairon/Chart.yaml").read_text())
    if chart.get("version") != "0.6.0" or chart.get("appVersion") != "0.6.0":
        fail("Helm chart version/appVersion must be 0.6.0")
except Exception as e:
    fail(f"Chart semantic check failed: {e}")

if (root / "VERSION").read_text().strip() != "v0.6.0":
    fail("VERSION must be v0.6.0")

# README is a landing page; detail lives in the docs it links to.
readme_docs = ["README.md", "ARCHITECTURE.md", "docs/README.md", "docs/WHAT_SHIPS.md", "docs/STATUS.md"]
readme = "\n".join((root / p).read_text() for p in readme_docs)
for needle in [
    "without KubeVirt", "no libvirt", "FluxVM", "Apache-2.0", "Production gaps",
    "MachineMigration", "MachineSnapshot", "adopt-only", "ResourceClaim", "vfio_devices",
    "MachineNetworkPolicy", "Network Fabric", "kaironctl install", "Cilium",
]:
    if needle not in readme:
        fail(f"README and linked docs ({', '.join(readme_docs)}) missing {needle!r}")

machine_crd = next(d for d in crds if d["metadata"]["name"] == "machines.kairon.zyvor.dev")
net_props = machine_crd["spec"]["versions"][0]["schema"]["openAPIV3Schema"]["properties"]["spec"]["properties"]["network"]["properties"]
for field in ("dataplaneMode", "ciliumAttach"):
    if field not in net_props:
        fail(f"Machine.spec.network missing {field}")
mnp = next(d for d in crds if d["metadata"]["name"] == "machinenetworkpolicies.kairon.zyvor.dev")
mnp_spec = mnp["spec"]["versions"][0]["schema"]["openAPIV3Schema"]["properties"]["spec"]["properties"]
if "cilium" not in mnp_spec:
    fail("MachineNetworkPolicy.spec missing cilium")

for path, needles in {
    "docs/CLI.md": ["kaironctl network status", "embedded Helm"],
    "docs/network-fabric.md": ["CiliumExternalWorkload", "cilium.sync"],
    "docs/DEPENDENCIES.md": ["raw REST", "helm.sh/helm/v3"],
    "docs/getting-started.md": ["network.ciliumAttach.enabled"],
}.items():
    text = (root / path).read_text()
    for needle in needles:
        if needle not in text:
            fail(f"{path} missing {needle!r}")

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
