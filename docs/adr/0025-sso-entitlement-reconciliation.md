# ADR 0025: Source-scoped entitlement reconciliation before session issue

Status: accepted

OIDC groups are advisory entitlements, never a source of permanent local
privilege. On every verified sign-in, desired roles are resolved through an
explicit `role_mapping`. Without a mapping, raw claim values are used only with
the explicit `allow_raw_claim_roles` opt-in, and privileged names (`admin`,
`owner`, `root`, `superadmin`, `system`) are always refused. An explicit mapping
entry is the reviewed decision that may name a privileged role. An absent or
empty group claim defaults to `deny`: all OIDC-sourced roles are revoked, with
`defaults_only` as the sole alternative. There is no `keep_previous` behaviour
and OIDC grants expire with each verified sign-in.

The source-scoped reconciliation selects, adds and deletes rows with
`source='oidc'` in one transaction and returns the delta. A desired privileged
role already held from a different source generally causes sign-in to be
refused and audited. The explicitly linked account in ADR 0051 may hold
independent grants from `local` and `oidc` for the same privileged role, so
provider group removal revokes the OIDC grant while preserving local
break-glass access.

Reconciliation precedes session issuance in the same request. A successful
ordinary OIDC sign-in rotates prior sessions. When `reject_unmapped` denies
a verified sign-in for an existing identity, Carbon Panel clears OIDC grants
and invalidates sessions from that provider on linked accounts. A
reconciliation or verification failure creates no session.

Role reconciliation and session issuance are adjacent but separate database
operations, not one transaction. The security boundary after that split is
`auth.Manager.ValidateSession`: on **every** authenticated request it re-reads
the user's current roles from the database and returns them in the
`AuthenticatedUser`, so a JWT minted just before a role change cannot carry
stale privilege into a later request. The embedded JWT roles are advisory;
authorization uses the freshly persisted set allowed by the credential
origin. On linked local accounts, local credentials use locally owned
grants and OIDC sessions use OIDC-sourced grants.
