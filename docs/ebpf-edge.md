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
