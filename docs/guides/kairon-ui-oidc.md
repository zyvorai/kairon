# User guide: OIDC/SSO for kairon-ui

`ui.oidc.enabled` (Helm chart, off by default) adds "Sign in with SSO" to
the dashboard's login screen -- an Authorization Code + PKCE flow against
an external OpenID Connect identity provider, alongside (not instead of)
`ui.auth.users` and the legacy shared `ui.token`.

## Why this breaks Go-stdlib-only, deliberately

This is the one place in Kairon that pulls in dependencies beyond the Go
standard library: `golang.org/x/oauth2` and `github.com/coreos/go-oidc/v3`
(which itself needs `github.com/go-jose/go-jose/v4` for JWT/JWK
handling). Every other Kairon component -- `kairon-controller`,
`kairon-node`, `kaironctl`, and `kairon-ui` itself without OIDC enabled --
stays exactly as stdlib-only as before; enabling `ui.oidc.enabled` doesn't
change what those components link against at all.

The reason this exception was made, specifically here: real JWT signature
verification against a JWK set (rotating keys, multiple algorithms, clock
skew, replay/nonce handling) is genuinely hard to get right, and hand-
rolling it would trade a well-reviewed, widely-used library for a
custom implementation nobody should trust more. That's a different
calculus than everywhere else Kairon avoids a dependency -- those are
almost always "this would be *convenient*, not *necessary*." Real OIDC
verification is closer to necessary once you've decided to support it at
all.

## Setup

Register a client with your identity provider first. You'll need:

- **Redirect URI**: `https://<your-kairon-ui-host>/api/v1/auth/oidc/callback`
  -- register this exactly; kairon-ui never guesses its own externally-
  reachable URL (proxies and ingress hostnames vary too much to infer
  safely), so `ui.oidc.redirectURL` must match what you register, exactly.
- **Client type**: confidential (with a client secret) or public (PKCE
  only, no secret) both work -- `ui.oidc.clientSecret` may be left empty
  for a public client. PKCE is always used regardless.
- **Scopes**: `openid` is required; `ui.oidc.scopes` defaults to
  `openid,profile,email`. Whatever claim `ui.oidc.usernameClaim` names
  (default `email`) must actually be returned by your provider for the
  scopes you request.

```bash
helm upgrade --install kairon ./charts/kairon -n kairon-system \
  --set ui.enabled=true \
  --set ui.oidc.enabled=true \
  --set ui.oidc.issuerURL=https://idp.example.com \
  --set ui.oidc.clientID=kairon-ui \
  --set ui.oidc.clientSecret=... \
  --set ui.oidc.redirectURL=https://kairon.example.com/api/v1/auth/oidc/callback
```

`ui.oidc.issuerURL` must serve a standard OIDC discovery document at
`{issuerURL}/.well-known/openid-configuration`. `kairon-ui` fetches this
once, at process startup -- a misconfigured or unreachable issuer fails
the container immediately (a visible `CrashLoopBackOff`), not a confusing
500 the first time someone tries to sign in.

`ui.oidc.existingSecret`/`existingSecretKey` reference a pre-created
Secret for the client secret instead, the same escape hatch every other
credential in this chart already has (`ui.existingSecret`,
`migration.tlsSecretName`, ...).

## What actually happens (the flow)

1. The browser hits `GET /api/v1/auth/oidc/login`. kairon-ui generates a
   PKCE code verifier and a nonce, signs both into the OAuth2 `state`
   parameter (HMAC-derived from `sessionSecret`, but with a distinct,
   domain-separated key -- a `state` value can never be replayed as a
   session token or vice versa), and redirects to the provider.
2. The operator authenticates with the provider (however it wants --
   password, hardware key, another SSO hop, whatever). kairon-ui never
   sees a password.
3. The provider redirects back to `GET /api/v1/auth/oidc/callback` with a
   `code` and the same `state`. kairon-ui verifies `state`'s signature and
   expiry (10 minutes), exchanges `code` for tokens (PKCE-verified), then
   verifies the returned ID token's signature, issuer, audience, expiry,
   **and nonce** against the provider's published keys.
4. `ui.oidc.usernameClaim` (default `email`) becomes the session's
   username, and kairon-ui issues a normal session token -- the exact same
   `signSession` primitive `POST /api/v1/auth/login` already uses. From
   here on, an OIDC-authenticated session is indistinguishable from a
   password one to every other route; `withAuth` never had to change.
5. kairon-ui redirects the browser to `/oidc/callback#token=...` -- a URL
   *fragment*, deliberately, never the query string: a fragment is never
   sent to any server on a subsequent request, so the token never lands in
   an access log or a `Referer` header. The SPA reads it client-side and
   stores it exactly like a password login's token (`sessionStorage`).

## Identity and authorization model

**Username collisions are intentional, not a bug to work around.**
`ui.oidc.usernameClaim` puts OIDC-authenticated sessions in the *same*
username namespace `ui.auth.users` occupies. If your identity provider
returns `alice@example.com` for the same person who also has a
`ui.auth.users` entry named `alice@example.com`, both login methods
attribute audit log entries to the identical username -- generally
desirable (one person, one identity, regardless of how they signed in).
Choose a username claim whose values won't accidentally collide across
different real people if that's a concern in your environment.

**OIDC never grants admin.** An OIDC-authenticated session can do
everything a normal operator can -- create/delete Machines, migrate,
snapshot, change its own password (wait: it can't, see below) -- but is
never eligible to reset another operator's password via
`POST /api/v1/users/{username}/password`, regardless of what groups or
roles the identity provider reports. This isn't an oversight to be fixed
later; there's nothing meaningful for an "OIDC admin" to reset in the
first place, since an OIDC identity has no `PasswordHash` in Kairon at
all. `POST /api/v1/auth/password` (changing your *own* password) is
naturally refused the same way, for the same reason -- `findUser` never
finds an OIDC-only username in `ui.auth.users`.

**Logout works normally.** `POST /api/v1/auth/logout` revokes an
OIDC-issued session token exactly like a password-issued one -- there's
no separate "OIDC logout" concept, and no round trip back to the identity
provider (a real limitation: signing out of Kairon doesn't sign the
operator out of the identity provider itself, or any other application
using it).

## Real limits today (first cut)

- No group/role claim maps to Kairon-side admin status -- see above.
- No provider-initiated (SP-initiated only) logout or single-logout (SLO)
  propagation back to the identity provider.
- No refresh-token use: a kairon-ui session is a fixed 12-hour token, same
  TTL as a password login, not renewed against the identity provider in
  the background. Re-authenticating after expiry means clicking "Sign in
  with SSO" again.
- `ui.oidc.usernameClaim` is a single, fixed claim name -- no fallback
  chain (e.g. "try `email`, then `preferred_username`") and no
  provider-specific claim transformation.
