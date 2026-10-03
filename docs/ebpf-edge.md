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

FluxVM needs dataplane schema 12 and its eBPF dataplane (`ebpf` or
`cilium` mode). What it does:

| Feature | State |
| --- | --- |
| Anti-spoof | Enforced in the VM's TC program. Forged source IPs drop as `spoof_ip`; forged MACs drop as `spoof_mac` on a bridged tap. |
| SNI / DNS allow | Enforced on TLS ClientHello (TCP 443) and DNS queries (port 53). Other names drop as `sni_deny` / `dns_deny`. |
| QoS | Egress by a token bucket in the TC program, ingress by a `tbf` qdisc and a police action on the host interface. |
| Attributed drops | Read from the datapath's per-reason counters, with the 5-tuple of the last packet. |
| Learned IP | From guest ARP and IPv6 neighbor advertisements, else the DHCP lease. |
| Conntrack | Export dumps the live table; restore is applied on attach if the VM is not yet attached. |
| Persistence | The edge spec and a pending restore survive a FluxVM restart. |

A netns Machine is hooked on the host side of its namespace router, so
MAC anti-spoof and ARP learning do not apply there; IP anti-spoof, the
allow lists and QoS do. A netns Machine must set `spec.network.mac`.

FluxVM's side is documented in
[fluxvm docs/vm-edge-contract.md](https://github.com/zyvorai/fluxvm/blob/main/docs/vm-edge-contract.md).

## Installing the CRDs

`deploy/crd.yaml` is generated from `charts/kairon/crds/` by `make crds`,
and `make validate` fails if the two differ. Either can be applied.
