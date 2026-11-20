---
type: Invariant
title: SSO identity and entitlement origin
description: OIDC identity is issuer plus subject; no auto-link or implicit privilege, and OIDC roles are reconciled transactionally before session issue.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/auth/oidc.go
  - internal/db/store.go
  - internal/rpc/services/user.go
sources:
  - title: ADR 0024
    resource: ../../docs/adr/0024-sso-identity-and-privilege-origin.md
  - title: ADR 0025
    resource: ../../docs/adr/0025-sso-entitlement-reconciliation.md
  - title: ADR 0051
    resource: ../../docs/adr/0051-explicit-local-admin-oidc-linking.md
verified: { by: agent:hy4-flash-free, at: 2026-11-20 }
review_after: 2027-02-18
---

# Rule

An OIDC subject is unique only within its issuer. Resolve returning users by
`(oidc_issuer, oidc_subject)`; never link by email, display name or a locally
unique subject with unknown issuer. A legacy issuer-less row yields
`link_required`, never an automatic binding. An existing local admin may bind
their own verified identity only through the short-lived single-use intent from
`UserService.BeginOIDCLink`; the callback binds issuer plus subject after ID-token
verification, and the local grant stays local-owned. No IdP claim creates an
admin and no first-user or role-less admin bootstrap exists on the OIDC path.

OIDC group claims are advisory entitlements. Resolve them through an explicit
mapping; without one, raw claim values require `allow_raw_claim_roles` and
privileged names are always refused. An absent claim revokes OIDC-sourced roles
(`deny` by default). Reconcile only `source=oidc` grants with
`SetUserRolesBySource` in one transaction **before** issuing a session; local
and admin grants are never touched. Privileged-role provenance conflicts refuse
sign-in and are audited.

# Why

Email and name are not identifiers, and a reused subject string across issuers
would otherwise take over another account. Accumulating IdP groups would leave
stale privilege after a group removal or account deprovisioning.

# How to check

- Confirm every identity lookup includes the issuer.
- Confirm no code path binds an issuer-less legacy row from current config.
- Confirm link intents come from an active local admin and are consumed once
  before the verified callback binds issuer plus subject.
- Confirm role changes use the source-aware helper and precede session issue.
- Run the owned OIDC fixtures for multi-issuer same-subject, absent claims,
  raw privileged names and atomic reconciliation failure.

# Read next

- [Fail-closed authorization](authz-fail-closed.md)
- [ADR 0023](../../docs/adr/0023-oidc-sso-boundary.md)
