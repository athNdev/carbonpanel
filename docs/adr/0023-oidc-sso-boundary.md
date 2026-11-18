# ADR 0023: Standards-based OIDC is the SSO boundary

Status: accepted

Carbon Panel remains a confidential-client Relying Party using server-side
OpenID Connect Authorization Code with S256 PKCE. The inherited implementation
exchanged an authorization code with only a state cookie and no PKCE or nonce;
that is short of RFC 9700 and OIDC Core. `internal/auth/oidc.go` now binds
`state`, `nonce` and the PKCE code verifier into one in-memory transaction that
is consumed with a single locked compare-and-delete **before** the code is
exchanged. A replayed or concurrent callback therefore cannot redeem a second
token, and refused callbacks never call the IdP token endpoint.

The issuer is pinned in configuration and verified by go-oidc discovery and ID
token validation; ID token signatures use a local algorithm allowlist
(RS256/ES256 by default, HS256 only by explicit operator choice because
authentik uses it without a published JWKS). Signature, `iss`, `aud`, `exp` and
nonce must all validate before any account or session is created.

The session token is never placed in a URL. The callback issues a one-time,
short-lived exchange code and the SPA redeems it through
`POST /api/v1/auth/oidc/exchange`. The OIDC state cookie is HttpOnly,
SameSite=Lax, Path=/, MaxAge=300 and Secure when TLS is in effect or explicitly
configured behind a terminating proxy; forwarded headers are never trusted as
proof of TLS (Track B owns the proxy contract).

authentik and Okta are supported through configuration, not provider forks.
No live tenant interoperability is claimed; verification uses owned local OIDC
fixtures only. See ADR 0024 for identity rules and ADR 0025 for entitlements.
