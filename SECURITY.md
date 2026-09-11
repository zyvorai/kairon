# Security policy

Report suspected vulnerabilities privately to Zyvor maintainers before opening a public issue. Include affected version, deployment mode, reproduction steps, and impact.

## v0.2 security boundaries

- Kubernetes namespace is used as the FluxVM tenant boundary.
- Node agents only reconcile Machines assigned to their own node.
- Image paths can be constrained to an administrator-selected root; traversal outside that root is rejected before FluxVM is called.
- Runtime cleanup uses a Kubernetes finalizer.
- Live migration accepts only validated `tcp:host:port` destinations. `exec:`, `unix:`, malformed addresses, and invalid ports are rejected by Kairon.
- Live cutover sets `kairon.zyvor.dev/adopt-only=true`. If the expected migrated runtime is absent, the target node agent refuses to create a new VM.
- A DRA `ResourceClaim` must have an allocation. PCI BDFs are syntax-checked and must be present in the node-local `KAIRON_VFIO_ALLOWLIST`; the default empty allowlist denies all passthrough.
- ResourceClaim annotations are mapping hints, not authorization. The node-local allowlist is the host-device authorization boundary.

## Operational warnings

Cold migration does not copy host-local disks. Operators must ensure the target has coherent access to all required state.

Live migration transport preparation, encryption/authentication, storage coherency, and network-state handoff are not automated in v0.2. Do not expose QEMU migration listeners publicly; use a trusted private network or an appropriately protected tunnel.

MachineSnapshot delegates data consistency to the CSI driver and workload. v0.2 does not freeze the guest filesystem before snapshot creation.

Kairon is pre-GA. Hostile multi-tenant production use additionally requires admission policy, quotas, audit guarantees, image provenance, runtime hardening, fencing, and hardware qualification.
