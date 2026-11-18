# ADR 0024: SSO identity is issuer plus subject; privilege has an explicit origin

Status: accepted

The sole OIDC identity key is `(OIDCIssuer, OIDCSubject)`, matching the existing
composite unique index on `users`. A subject is unique only within an issuer.
A locally unique subject string with no recorded issuer therefore proves
nothing about ownership. The previous subject-only lookup and the implicit
"first user becomes admin" / "role-less returning user becomes admin" grants
are removed.

A returning identity resolves only through
`Store.GetUserByOIDCIssuerAndSubject`. A legacy row with the same subject but an
empty issuer is never auto-bound from the configured issuer: sign-in returns
the canonical `link_required` state. Binding is possible only through an
authenticated local admin-mediated link/recovery, or an explicit migration pin
whose historical issuer is verified against known deployment configuration.
Unknown or ambiguous history stays unlinked. Email, name and preferred username
are display fields and never link accounts. Local break-glass admin and the
recovery key remain the recoverable bootstrap path; no IdP claim can create an
admin.
