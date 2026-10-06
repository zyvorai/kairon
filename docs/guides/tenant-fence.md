# Tenant fence

`spec.tenant` stays optional. Set `kairon.zyvor.dev/tenant-fence: "true"` on a Machine when that tenant must not reach guest addresses observed on other tenants in the same namespace.

What the controller does:

- Projects `kairon.zyvor.dev/tenant` from `spec.tenant`. Admission rejects a rename or a clear.
- Owns `NetworkSecurityGroup/tenant-fence-<tenant>` in that namespace. `spec.policy.denyCidrs` is the other tenants' `status.guestIP` and `status.guestIPs` values, as `/32` or `/128`.
- Deletes that group when no non-deleting Machine in the namespace still opts in for that tenant.

What kairon-node does:

- Upserts the group to FluxVM, as it already does for every `NetworkSecurityGroup`.
- Merges the group's deny list into the `MachineNetworkPolicy` it posts. A user policy is not replaced.
- If no policy selects the Machine, posts `defaultAllow: true` plus those denies. Node-wide default-deny still wins on `defaultAllow` when it is enabled.

This is not a VRF. An address Kubernetes has not observed, or an address outside this namespace, is not denied. An address that also appears on a same-tenant Machine is not denied.

A `MachineNetworkPolicy` whose selector sets `kairon.zyvor.dev/tenant` must carry the same label. That stops a tenant-scoped object from selecting another tenant.

```yaml
metadata:
  annotations:
    kairon.zyvor.dev/tenant-fence: "true"
spec:
  tenant: acme
```

See `examples/tenant-fence.yaml`.
