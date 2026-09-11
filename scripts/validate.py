#!/usr/bin/env python3
from pathlib import Path
import sys, yaml
root=Path(__file__).resolve().parents[1]
errors=[]

def fail(msg): errors.append(msg)

required=["go.mod","LICENSE","README.md","deploy/crd.yaml","deploy/rbac.yaml","deploy/controller.yaml","deploy/node.yaml","charts/kairon/Chart.yaml","charts/kairon/values.yaml",".github/workflows/ci.yml"]
for f in required:
    if not (root/f).exists(): fail(f"missing {f}")

for f in [root/'deploy/crd.yaml',root/'deploy/rbac.yaml',root/'deploy/controller.yaml',root/'deploy/node.yaml',root/'examples/linux-machine.yaml',root/'examples/firecracker-machine.yaml',root/'examples/storage-machine.yaml',root/'examples/availability.yaml']:
    try:
        docs=list(yaml.safe_load_all(f.read_text()))
        if not docs or any(d is None for d in docs): fail(f"empty YAML document in {f.relative_to(root)}")
    except Exception as e: fail(f"invalid YAML {f.relative_to(root)}: {e}")

try:
    crds=list(yaml.safe_load_all((root/'deploy/crd.yaml').read_text()))
    names={c['metadata']['name'] for c in crds}
    expect={'machines.kairon.zyvor.dev','machineimages.kairon.zyvor.dev','virtualdisks.kairon.zyvor.dev','machinesnapshots.kairon.zyvor.dev','machinedisruptionbudgets.kairon.zyvor.dev','machinemigrations.kairon.zyvor.dev'}
    if names!=expect: fail(f'unexpected CRD set: {sorted(names)}')
    for crd in crds:
        if crd['spec']['group']!='kairon.zyvor.dev': fail('unexpected CRD group')
        v=crd['spec']['versions'][0]
        if v['name']!='v1alpha1' or 'status' not in v['subresources']: fail(f"{crd['metadata']['name']} must expose v1alpha1 + status")
except Exception as e: fail(f'CRD semantic check failed: {e}')

readme=(root/'README.md').read_text()
for needle in ['without KubeVirt or libvirt','FluxVM','Apache-2.0','Production gaps']:
    if needle not in readme: fail(f'README missing {needle!r}')

if errors:
    print('VALIDATION FAILED')
    for e in errors: print(' -',e)
    sys.exit(1)
print('validation: OK')
