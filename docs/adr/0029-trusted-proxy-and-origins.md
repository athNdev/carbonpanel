# ADR 0029: Trusted proxy headers and WebSocket origins

Status: accepted (strict PROXY-protocol trust; WebSocket strict mode is opt-in).

Forwarded headers are meaningful only behind a proxy the operator declared.
`server.trust_proxy` is now the single gate for:

- HSTS emission from `X-Forwarded-Proto: https` (`internal/rpc/security_headers.go`);
- throttle client-IP inference from `X-Forwarded-For` / `X-Real-IP`
  (`internal/rpc/services/auth.go`, `AuthService.SetTrustProxy`);
- the scheme in generated Packwiz content URLs;
- the existing WebSocket `X-Forwarded-Host` origin check.

An untrusted client can no longer make a plaintext response carry HSTS or
influence the login throttle key. Direct TLS (`r.TLS != nil`) is always honoured.

Inbound Minecraft PROXY protocol v2 is gated by `proxy.trusted_proxy_cidrs`
(empty = trust nobody). `WrapConnWithProxyProtocolIfTrusted` leaves a header
from any other source in the byte stream untouched and keeps the peer address,
so a game client cannot spoof the IP used by the handshake limiter or forwarded
to the backend. An unparsable CIDR is a configuration error, not a silently
ignored entry. Operators using an upstream load balancer must add its CIDR.

WebSocket identity lifecycle: a failed `AUTH` message now clears the socket's
user and authenticated flag and drops its log subscriptions instead of keeping
the previous privileged identity. Server checks (log subscribe, console
command) use the same object-scoped rule as Connect RPC, including subuser
grants, and apply a command length cap and log-tail bound.

`server.require_ws_origin` is an opt-in strict mode (default false) for
browser-only deployments; changing the default would break CLI/native clients
that legitimately omit `Origin`. Session revocation/expiry revalidation on an
open socket remains owned by Track A's `auth.SessionValidator` contract (B-00).
