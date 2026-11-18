# ADR 0027: Fail-closed policy for legacy HTTP adapters

Status: accepted (implemented for metrics, streaming upload, mod browse, staged
config and Packwiz management routes).

Connect-RPC already denies unmapped procedures and nil enforcers. Legacy REST
adapters mounted on the same mux did not share that guarantee: several skipped
authorization when no auth provider was configured or when the enforcer was
nil, and checked the global `"*"` object even when the path named a server.

`internal/rpc/handlers/policy.go` now owns one `requireRequestObject` helper:
it authenticates from the `Authorization` header or `cp_session` cookie, then
enforces a resource/action against a concrete object. Missing auth manager,
missing enforcer and enforcement errors all deny. Every non-public legacy
adapter delegates to it; the genuinely public set (health, OpenAPI, loader
reference data, OIDC callbacks, container bootstrap pack files) stays an
explicit, reviewed list.

Object identity comes from the request, not from a caller claim:

- metrics: `/servers/{id}` is scoped to that server; host-wide metrics use `"*"`.
- mod browse: the `{serverID}` path segment is the object (`"none"` maps to `"*"`).
- staged config: list/stage use the path server; apply/rollback/discard resolve
  the staged record's owning server before authorizing, so a caller cannot use
  a server they control to mutate another server's staged change.
- Packwiz management keeps the global object used by the generated RPC mapping
  (ADR 0012 parity), but no longer skips authorization on missing dependencies.

Compatibility: ADR 0012 compatibility routes stay for the full deprecation
window; an explicit `allow_no_auth` installation still works because the auth
manager supplies the synthetic admin. Misconfigured nil dependencies now
return 401/403 instead of silently allowing the request.

Negative fixtures in `internal/rpc/handlers/fail_closed_policy_test.go` cover
nil enforcer, wrong-object grants, the staged-ID owner case and the explicit
no-auth path. New legacy routes must use the shared helper; a route registered
without it is a defect.
