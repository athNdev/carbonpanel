# ADR 0051: Explicit local-admin OIDC linking with retained role ownership

Status: accepted

ADR 0024 requires an authenticated local-admin-mediated link before an OIDC
identity can bind to an existing privileged account. That path was documented
but missing at `e8245faa`; an unchanged v1.0.40 local admin would otherwise get
a new role-less OIDC identity on first sign-in. This ADR defines the smallest
implementation that closes that gap without weakening issuer+subject binding.

## Decision

1. An active local admin with a `local`-sourced `admin` grant calls
   `UserService.BeginOIDCLink`. The interceptor requires `users/update`; the
   service additionally verifies the caller is authenticated, active,
   `auth_provider='local'`, and locally admin. The RPC takes no issuer, subject,
   email or target user ID: intent is always self-service.
2. The OIDC handler owns a bounded, TTL'd, single-use in-memory intent store.
   `BeginOIDCLink` returns an opaque token that names only that intent. It is
   not a session credential, is never logged, and contains no identity claim.
3. The admin opens `/api/v1/auth/oidc/login?link_token=<token>`. The token is
   consumed once at login start; the IdP redirect still receives only state,
   nonce and S256 PKCE challenge. An invalid, expired, replayed or ineligible
   token produces `link_invalid` and no state transaction.
4. Only after the callback has consumed state, verified nonce and signature,
   and pinned issuer can the verified `(issuer, subject)` pair bind to the
   intent's local admin. Binding refuses if the account is already linked, if
   the issuer+subject pair already exists, if only the subject is already known,
   or if the target stopped being an active local admin. Email, name and
   preferred username are never link keys. `auth_provider` remains `local`.
5. A mapped privileged role already held from `local` on an explicitly linked
   account is accepted for that sign-in but is **not** reconciled into the OIDC
   source. It remains a single local-owned break-glass grant; OIDC does not
   reattribute or revoke it. Every other provenance conflict still refuses the
   sign-in with `role_conflict`. Raw privileged claim values remain refused
   unless explicitly mapped, exactly as in ADR 0025.
6. Normal OIDC session rotation applies after a successful link sign-in: prior
   sessions for that user are invalidated, and `ValidateSession` keeps applying
   the persisted role set per request. Local password access remains available.

## Consequences

- Operators can migrate v1.0.40 local admins without email-based merging or a
  temporary implicit-admin grant.
- Link state is process-local and short-lived; a controller restart requires a
  new intent. This is preferable to persisting a capability that could outlive
  the admin session it represents.
- The local-admin privilege is intentionally not moved into the OIDC source;
  that would let an absent group claim revoke break-glass access.
- Live IdP interoperability is not claimed. Evidence comes from local
  discovery/token/JWKS fixtures and real REST/session/store seams only.

## Evidence

`internal/auth/oidc_admin_link_test.go` exercises the real login/callback/
exchange routes with a local IdP fixture: explicit mapping success, retained
local role ownership, stale-session rotation, local login preservation, link
token replay/expiry/ineligibility, subject collision and unchanged
`role_conflict` refusal. `internal/rpc/services/oidc_link_test.go` covers RPC
authentication/authorization and fail-closed link-store injection.
