---
hero:
  eyebrow: GUIDES
  title: 'User guide: guest agent (real guest IP reporting)'
---

How to opt a Machine into FluxVM's real `qemu-guest-agent` channel, and why
you'd want to.

## The problem this solves

`status.guestIP` normally comes from parsing a dnsmasq DHCP lease file --
but that only exists for `spec.network.mode: tap` with `netns: true`. Every
other network mode (in particular `user`, SLIRP -- Kairon's default) has no
lease file at all, so `status.guestIP` just stays empty forever, even
though the guest genuinely has a real, working IP address.

## How to enable it

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: Machine
metadata:
  name: web
spec:
  guestAgent:
    enabled: true
  cloudInit:
    packages: ["qemu-guest-agent"]   # the guest has to actually run it
  # ... unchanged otherwise
```

Two things have to both be true: `spec.guestAgent.enabled: true` (tells
FluxVM to add the virtio-serial channel to the VM) and the guest actually
running `qemu-guest-agent` (most cloud images don't ship it by default --
`spec.cloudInit.packages` is the easiest way to get it installed and
started at first boot).

Once both are true, `kairon-node` asks the real guest agent for its network
interfaces (`guest-network-get-interfaces`) whenever it doesn't already have
a `status.guestIP` from a DHCP lease -- the guest's own kernel-reported
address, not a host-side guess.

## How Kairon picks an address

`status.guestIP` is a single string -- Kairon has no concept of a Machine
having more than one reportable address (true of the DHCP-lease path too).
Given the real guest-network-get-interfaces response, Kairon picks the
first IPv4 address on the first non-loopback interface. If your guest has
more than one NIC or address, that pick may not be the one you expect --
there's no way to influence it today.

## Real limits today (v1 of this feature)

- Only ever resolved once: once `status.guestIP` has a value, Kairon keeps
  reporting it and stops asking the guest agent again, even if the address
  later changes. Matches the existing DHCP-lease behavior's own
  "preserve the last known value" convention.
- A guest agent that isn't installed or hasn't started yet just means
  `status.guestIP` stays empty and is retried on the next reconcile tick --
  not a hard error.
- No IPv6 support -- `BestGuestIP` only ever picks an IPv4 address.
- This is unrelated to FluxVM's own bespoke `spec.agent` (a different,
  vsock-based protocol requiring its own FluxVM-specific guest binary,
  powering `kaironctl exec`-style features Kairon doesn't expose yet) --
  `spec.guestAgent` here is specifically the real, upstream
  `qemu-guest-agent` over virtio-serial.
