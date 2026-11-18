# ADR 0026: Logout semantics and honest IdP limits

Status: accepted

Logging out always invalidates the local DB session and clears browser state.
When discovery advertises an end-session endpoint, an IdP logout may be
attempted with an ID token hint and an exact configured post-logout return.
authentik advertises per-application end-session and supports front-channel
and back-channel logout; Okta uses different logout parameters and may not
honour the same hint. No global single logout is claimed: the IdP session may
persist and IdP-side token revocation is not performed by default.

Session credentials are rotated on successful sign-in: previously issued
sessions for that user are invalidated before a new one is created. Session
lifetime remains bounded by the existing `auth.session_timeout` setting and
absolute expiry in the JWT. A production `auth.SessionValidator` implementation
is owned by Track A and exposed through the frozen Phase 0 hook contract so
proxy and WebSocket middleware can validate a credential without duplicating
auth logic or creating an import cycle.

Audit events are allowlisted and redacted: issuer, subject hash, user ID,
outcome and role-delta counts are recorded; state, nonce, authorization code,
PKCE verifier, tokens, client secret and raw claim values are never logged.
