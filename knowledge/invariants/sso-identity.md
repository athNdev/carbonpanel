---
type: Invariant
title: SSO identity and entitlement origin
description: OIDC identity is issuer plus subject; no auto-link or implicit privilege, and OIDC roles are reconciled transactionally before session issue.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/auth/oidc.go
  - internal/auth/oidc_link.go
  - internal/auth/manager.go
  - internal/db/models.go
  - internal/db/oidc_link.go
  - internal/db/store.go
  - internal/rpc/services/user.go
sources:
  - title: ADR 0024
    resource: ../../docs/adr/0024-sso-identity-and-privilege-origin.md
  - title: ADR 0025
    resource: ../../docs/adr/0025-sso-entitlement-reconciliation.md
  - title: ADR 0051
    resource: ../../docs/adr/0051-explicit-local-admin-oidc-linking.md
verified: { by: agent:codex, at: 2026-11-20 }
review_after: 2027-02-18
---

# Rule

An OIDC subject is unique only within its issuer. Resolve returning users by
`(oidc_issuer, oidc_subject)`; never link by email, display name or a locally
unique subject with unknown issuer. A legacy issuer-less row yields
`link_required`, never an automatic binding. An existing local admin may bind
their own verified identity only through the short-lived single-use intent from
`UserService.BeginOIDCLink`. The initiating browser and exact local session
must still qualify at the callback. The verified issuer and subject bind in
the same transaction that reconciles OIDC grants. Linking keeps the local
session and does not issue an SSO session.

OIDC group claims are advisory entitlements. Resolve them through an explicit
mapping; without one, raw claim values require `allow_raw_claim_roles` and
privileged names are always refused. An absent claim revokes OIDC-sourced roles
(`deny` by default). Reconcile only `source=oidc` grants before issuing a session.
A role can have separate grants for a linked local account. Each request
reloads the grants allowed by the session origin. Local sessions use locally
owned grants and linked OIDC sessions use OIDC-sourced grants. Privileged
overlap with a local grant is allowed for the explicitly linked account.
Other privileged provenance conflicts refuse sign-in.

# How to check

- Confirm identity resolution requires the issuer, including for legacy rows.
- Confirm link intents come from an active local admin and require their
  browser and live initiating session at start and commit.
- Confirm explicit binding and reconciliation roll back together and normal
  sign-in uses the same source-scoped reconciliation.
- Run the owned OIDC fixtures for multi-issuer same-subject, absent claims,
  raw privileged names and atomic reconciliation failure.

See [Fail-closed authorization](authz-fail-closed.md).
