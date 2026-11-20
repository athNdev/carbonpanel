# ADR 0051: Explicit local-admin OIDC linking with retained role ownership

Status: accepted

ADR 0024 requires an authenticated local admin to authorize binding an OIDC
identity to their existing account. The account must retain local access
independently of the provider and its group policy.

## Decision

1. An active admin with a current local session and a locally sourced
   `admin` grant selects **Link SSO account** in Settings. The self-service
   `UserService.BeginOIDCLink` RPC requires `users/update` and accepts no
   target user ID or identity claims.
2. The controller creates a five-minute, single-use intent bound to the
   exact local session and an independent HttpOnly browser cookie.
   Login start consumes the intent. The IdP receives state, nonce, an
   S256 PKCE challenge and a request for fresh authentication.
3. The callback verifies state, nonce, signature, audience and issuer. The
   verified issuer and subject bind only while the browser and exact
   initiating session still qualify. The database checks the session and
   local admin grant in the same write transaction that binds the identity
   and reconciles OIDC grants. A failure rolls back both writes. An existing
   binding or a subject already known under another or unknown issuer
   requires operator review.
4. A successful link returns to Settings in the initiating local session.
   A subsequent ordinary OIDC sign-in resolves the same account by issuer
   and subject. The account keeps its local credentials and profile.
5. Local and OIDC role grants have independent provenance, including
   overlaps on a linked account. Each request reloads current grants.
   A linked OIDC session receives OIDC-sourced roles. Local sign-in and
   existing local API credentials receive locally owned roles. Creating an
   API token for the linked local account requires local access. Legacy
   untyped sessions on a linked account require a fresh sign-in.

## Consequences

- An existing local admin can sign in through both providers. IdP group
  removal revokes the mapped SSO grant while preserving local access.
- Restarting the controller, changing browser sessions, or expiring the
  original local session cancels a pending link. Start again from Settings.
- OIDC role mappings still require the controls in ADR 0025. The link provides
  a verified identity and does not grant a role by itself.

## Verification

The authenticated Connect fixture exercises login, RBAC, link eligibility
and the start request with a browser cookie jar. Callback fixtures exercise
exact-session and browser checks, replay and expiry, account conflicts,
separate sign-ins and group removal. Storage tests cover upgrade inputs,
competing link attempts and rollback. Provider-specific results belong in
release acceptance evidence for the tested build.
