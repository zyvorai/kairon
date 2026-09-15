# User guide: Windows guests

What's actually possible today, and what's genuinely blocked upstream in
FluxVM -- read this before assuming Kairon can't run Windows at all, or
that it can run *any* Windows edition.

## What works today: legacy-BIOS Windows with a pre-built image

Kairon never runs an interactive OS installer for any guest OS, Windows
included -- exactly like a Linux Machine, you boot a **pre-built qcow2/raw
image**, not an installer ISO (see [`machine-storage.md`](machine-storage.md)/
[`machine-image-import.md`](machine-image-import.md)). For Windows, that
image needs two things baked in *before* it ever reaches Kairon:

1. **VirtIO drivers already installed** (disk and network) -- Windows has
   no built-in virtio support, and Kairon has no interactive-setup flow to
   inject them via a second attached driver ISO mid-install. The
   standard, well-supported way to get this is to install Windows once
   (on any hypervisor, or using the official
   [`virtio-win`](https://github.com/virtio-win/virtio-win-pkg-scripts)
   driver ISO during that one-time setup), then export the resulting disk
   as your golden qcow2/raw image.
2. **[cloudbase-init](https://cloudbase-init.readthedocs.io/) installed**
   -- the Windows-native equivalent of cloud-init. It consumes the exact
   same NoCloud datasource format FluxVM's own cloud-init support already
   generates (a labeled ISO/vfat volume with `meta-data`/`user-data`
   files), so **`spec.cloudInit` works completely unchanged** for a
   Windows guest with cloudbase-init pre-installed -- no Kairon-side code
   exists or is needed specifically for Windows here.

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: Machine
metadata:
  name: win-app-1
spec:
  image:
    path: /var/lib/fluxvm/images/windows-server-2022-cloudbase-init.qcow2
  resources: {cpu: "4", memory: "8Gi"}
  runtime: {backend: qemu}
  cloudInit:
    hostname: win-app-1
    runCmd: ["powershell -Command \"...\""]
  powerState: Running
```

This covers Windows Server (2019/2022) and Windows 10 -- any edition that
doesn't *require* UEFI Secure Boot + TPM 2.0 to boot at all.

## What's blocked: Windows 11 and anything requiring Secure Boot/TPM

`spec.security.secureBoot`/`spec.security.tpm` exist in the `Machine` CRD,
but **Kairon refuses to create a Machine that sets either to `true`** --
confirmed against FluxVM's own source, not assumed: no backend Kairon
talks to (QEMU, Cloud Hypervisor, Firecracker) implements UEFI/OVMF
firmware or a vTPM device today. This used to be a silent gap (the fields
existed and did nothing); it's now an explicit, immediate error instead:

```
spec.security.secureBoot/tpm are not yet supported -- no FluxVM backend
implements UEFI/OVMF firmware or a vTPM device today
```

**Windows 11 hard-requires both** to boot at all, so it isn't supported by
Kairon today, full stop -- this is a real FluxVM-side capability gap
(OVMF firmware integration + a vTPM device, both genuine QEMU features
FluxVM simply doesn't wire up yet), tracked in `ROADMAP.md` as an upstream
dependency, the same way VFIO-through-live-migration names FluxVM's
missing hot-unplug/hot-plug API as its own blocker.

## Real limits today (first cut)

- No UEFI/Secure Boot/vTPM -- see above. Legacy BIOS only.
- No driver-ISO-attach for an interactive Windows Setup flow -- bring a
  pre-built image with virtio drivers already installed, the same
  expectation Kairon already has for every other guest OS.
- `spec.guestAgent` (real `qemu-guest-agent`-reported `status.guestIP`,
  see [`machine-guest-agent.md`](machine-guest-agent.md)) needs the
  Windows `qemu-guest-agent` service installed in your image too --
  Kairon's own side is guest-OS-agnostic, but hasn't been verified
  end-to-end against a real Windows guest in this repo's own CI.
- The graphical VNC console (`console.enabled`,
  [SECURITY.md](../../SECURITY.md)) is QEMU-backend-only and
  guest-OS-agnostic -- it should work against a Windows guest exactly
  like a Linux one, but likewise hasn't been specifically verified here.
