# ADR 0032: Security audit events and redaction

Status: accepted (partial implementation).

The immutable activity log already records successful login, logout, server
lifecycle, exports and privileged Docker overrides
(`internal/activity/activity.go`, `docs/issues/017-SEC-3-...`). The gap was
that failed authentication and break-glass recovery had no durable trail.

New events: `auth.login_failed`, `auth.denied`, `auth.recovery_used` and
`auth.token_revoked`. Failed logins are recorded after the existing per-user and
per-IP throttles, with a lowercased username and source IP but never the
submitted password. Invalid and successful recovery-key use are recorded; a
successful reset destroys every session, API token, invite and user, so it is
the highest-consequence unauthenticated operation in the product. Auditing
failures are logged and never break the operation being audited.

Log redaction is unified in `pkg/logger.SanitizeLogMessage`: in addition to the
existing password/secret/RCON-URI patterns it now masks `Bearer ...`,
`token=...`, `x-api-key:` and related key/value forms. Operation-journal
redaction remains a separate helper with the same intent.

API-token deletion now emits `auth.token_revoked` with the token id and no
plaintext or hash. Not yet implemented: a general HTTP 401/403 denial event for
every route (which needs a request ID and careful volume control). That remains
a follow-up, not claimed coverage.
