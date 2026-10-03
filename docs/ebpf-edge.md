# VM-edge eBPF

Kairon does not own BPF programs. FluxVM attaches `fluxvm_tc.bpf.o` /
`fluxvm_direct.bpf.o`. This document is the control-plane contract those
programs consume.

## What Kairon projects

| Feature | CRD | FluxVM endpoint |
| --- | --- | --- |
| Stable identity | derived from namespace/name, projected on `status.network.edge.identity` | `POST /v1/vms/{id}/network/edge` |
| Anti-spoof | `spec.network.antiSpoof` | same |
| Learn IP (ARP/DHCP/ND) | `spec.network.learnIP` | tap observer, no guest agent |
| QoS | `spec.network.qos` | token bucket in the edge map |
| SNI / DNS allow | `spec.policy.allowSNI`, `allowDNS` on MachineNetworkPolicy | L4 parser only, no Envoy |
| Attributed drops | `kaironctl network drops` | `GET /v1/vms/{id}/network/drops` |
| Conntrack move | migration session `conntrackSnapshot` | `GET/POST /v1/vms/{id}/network/conntrack` |
| Capture | `kaironctl network capture` | `POST /v1/vms/{id}/network/capture` (max 30s) |

Identity is FNV-1a of `namespace` + name, masked to 24 bits, and never
below 256. It does not change when the guest IP changes, so a live
migration does not flap `MachineNetworkPolicy`.

Conntrack restore fails closed when the snapshot identity does not match
the destination Machine. `status.network.edge.conntrackRestored` and
`blackholeWindowMs` record what moved and how long the guest was dark.

## Non-goals

- A Hubble UI inside kairon-ui (that is Paqtra).
- Kairon-owned `.bpf.c`.
- HTTP body inspection. SNI and DNS qname only.


## Reconcile

`kairon-node` posts the compiled document on each tick when
`dataplaneMode: ebpf`, `antiSpoof`, `learnIP`, or `qos` is set.
`dataplaneRequired` fails the Machine closed if that post fails.
Otherwise a FluxVM that does not have the endpoint yet is a warning.

On live migration start the source exports `/network/conntrack`, stamps
the stable identity, and puts the blob on `session.conntrackSnapshot`.
The destination restore from the previous change then matches.

## Policy and restore projection

The selecting MachineNetworkPolicy is copied into the edge document (name, CIDRs, SNI, DNS, QoS when the Machine did not set one). A successful destination conntrack restore is recorded in the node store and projected as status.network.edge.conntrackRestored and blackholeWindowMs on the next tick.

## Capture and learn-IP

`kaironctl network capture` posts the session when `KAIRON_UI_URL` is set (max 30s). The node relays it to `POST /v1/vms/{id}/network/capture`. `learnIP` with no guest address reads `GET /v1/vms/{id}/network/learned-ip` and records `guestIPSource`.

## FluxVM support

FluxVM serves these routes from the commit that adds
`docs/vm-edge-contract.md`. An older FluxVM returns 404, which is a
warning unless `dataplaneRequired` is set.

What FluxVM does today:

| Feature | State |
| --- | --- |
| Edge spec, conntrack restore, capture | Stored and validated. Identity mismatch and captures over 30s are rejected. |
| Anti-spoof, SNI/DNS allow, QoS | Not enforced. The spec is not loaded into the BPF maps yet. |
| Attributed drops | Placeholder events derived from the spec, not from the datapath. |
| Learned IP | Echoes `assignedIP`. No ARP, DHCP or ND observation yet. |
| Conntrack export | 400 unless the VM received a restore, so the source migrates without it. |

FluxVM keeps this state in memory, so it is lost when FluxVM restarts.

## Installing the CRDs

`deploy/crd.yaml` is generated from `charts/kairon/crds/` by `make crds`,
and `make validate` fails if the two differ. Either can be applied.
