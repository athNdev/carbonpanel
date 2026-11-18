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

Entitlements are reconciled by `Store.SetUserRolesBySource`, which selects,
adds and deletes **only** rows with `source='oidc'` inside one transaction and
returns the resulting delta. The older unscoped `UnassignRole` must not be used
here because it could delete a local or admin grant with the same role name.
Locally assigned and admin grants are never modified or reattributed. A desired
privileged role already held from a different source is a provenance conflict:
no grant is converted, the sign-in is refused and audited, and an administrator
must review the mapping.

Reconciliation precedes session issuance in the same request. If role loss or
deprovisioning removes a privileged role, every existing session for that user
is invalidated and the newly issued session carries only the reconciled roles.
A reconciliation or verification failure creates no session.
