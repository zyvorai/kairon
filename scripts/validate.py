#!/usr/bin/env python3
from pathlib import Path
import sys, yaml
root=Path(__file__).resolve().parents[1]
errors=[]

def fail(msg): errors.append(msg)

required=["go.mod","LICENSE","README.md","deploy/crd.yaml","deploy/rbac.yaml","deploy/controller.yaml","deploy/node.yaml","charts/kairon/Chart.yaml","charts/kairon/values.yaml",".github/workflows/ci.yml"]
for f in required:
    if not (root/f).exists(): fail(f"missing {f}")

for f in [root/'deploy/crd.yaml',root/'deploy/rbac.yaml',root/'deploy/controller.yaml',root/'deploy/node.yaml',root/'examples/linux-machine.yaml',root/'examples/firecracker-machine.yaml']:
    try:
        docs=list(yaml.safe_load_all(f.read_text()))
        if not docs or any(d is None for d in docs): fail(f"empty YAML document in {f.relative_to(root)}")
    except Exception as e: fail(f"invalid YAML {f.relative_to(root)}: {e}")

try:
    crd=yaml.safe_load((root/'deploy/crd.yaml').read_text())
    if crd['metadata']['name']!='machines.kairon.zyvor.dev': fail('unexpected CRD name')
    if crd['spec']['group']!='kairon.zyvor.dev': fail('unexpected CRD group')
    v=crd['spec']['versions'][0]
    if v['name']!='v1alpha1' or 'status' not in v['subresources']: fail('CRD must expose v1alpha1 + status')
except Exception as e: fail(f'CRD semantic check failed: {e}')

readme=(root/'README.md').read_text()
for needle in ['without KubeVirt or libvirt','FluxVM','Apache-2.0','Production gaps']:
    if needle not in readme: fail(f'README missing {needle!r}')

if errors:
    print('VALIDATION FAILED')
    for e in errors: print(' -',e)
    sys.exit(1)
print('validation: OK')
