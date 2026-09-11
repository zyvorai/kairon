# Security policy

Kairon is pre-1.0 software. Do not expose FluxVM directly to untrusted networks. Keep the FluxVM API node-local and use FluxVM authentication when available.

## Reporting

Please report security issues privately to security@zyvor.dev rather than opening a public issue.

## Threat boundaries

- Kubernetes RBAC controls who can create/update/delete `Machine` resources.
- The controller only assigns nodes; it never executes guest workloads.
- The node agent only reconciles machines whose `spec.nodeName` equals its own `NODE_NAME`.
- FluxVM is the VM execution security boundary; Kairon does not weaken its jail/cgroup/network configuration.
- The node agent rejects image paths outside `KAIRON_IMAGE_ROOT` (default `/var/lib/fluxvm/images`), but image trust/signature policy still requires roadmap work.
- Admission policy, confidential-compute attestation and device isolation require the roadmap work before hostile multi-tenant production use.
