# Kairon migration adapter contract

The adapter isolates hypervisor-specific migration mechanics from Kairon's Kubernetes API. It is a local HTTP service on a root/admin-controlled Unix socket. Kairon never exposes this socket to workload Pods.

Current FluxVM does **not** implement this contract. It is defined by Kairon v0.3 as the integration target for a future FluxVM migration backend or another trusted host component.

## Destination API

`POST /v1/destination/prepare`

Input is `migration.PrepareRequest` containing the immutable session identity. Response is:

```json
{
  "transferSupported": true,
  "endpoint": "opaque-backend-endpoint",
  "backend": "qemu"
}
```

The endpoint is opaque to Kubernetes and is sent only from the target peer to the authenticated source peer, then to the local source adapter.

`POST /v1/destination/{sessionID}/commit`

Makes the transferred target runtime authoritative/runnable according to backend semantics. It must be idempotent.

`POST /v1/destination/{sessionID}/abort`

Destroys or invalidates the prepared target. It must be idempotent before commit.

## Source API

`POST /v1/source/start`

Input is `migration.SourceRequest`: immutable session identity, FluxVM runtime ID, opaque target endpoint, and pre-copy/post-copy/bandwidth/downtime/multifd hints. Response is `migration.TransferStatus` and must contain an opaque `transferID` for asynchronous transfers.

`GET /v1/source/{sessionID}/{transferID}`

Returns transfer progress. Terminal phases understood by Kairon are `completed`/`transferred`, `failed`, `cancelled`/`canceled`, and `aborted`.

`POST /v1/source/{sessionID}/{transferID}/abort`

Cancels the source transfer where safe.

## Trust boundary

- Socket ownership/permissions are an administrator responsibility.
- Never accept shell commands or command strings in the adapter API.
- The adapter must validate session/runtime identity independently before destructive operations.
- Target `commit` is the point after which Kairon treats rollback as potentially ambiguous and uses `NeedsRecovery` on commit errors.
- Authentication between nodes is mTLS; the Unix adapter is intentionally node-local only.
