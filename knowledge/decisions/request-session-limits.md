---
type: Decision
title: Request and session limits
description: Upload sessions bind to their authenticated owner, the panel listener pins a header bound, and one command sender enforces length and rate limits.
status: stable
owners: [oss-runtime]
code_refs:
  - pkg/upload
  - internal/rpc/services/upload.go
  - internal/rpc/handlers/upload.go
  - internal/command
  - cmd/carbon-panel/http_limits.go
sources:
  - title: Contributor guide
    resource: ../../AGENTS.md
---

# Choice

Upload sessions created with a real user context record `OwnerUserID`; the RPC
service and the raw streaming handler enforce it on status, chunk, stream and
cancel, and every consuming import/save endpoint uses the owner-aware temp-path
and cleanup methods. The panel `http.Server` pins `MaxHeaderBytes` through one helper shared
with loopback fixtures. Console and RCON commands share `command.Sender`, which
caps command length and applies a sliding-window limit keyed by authenticated
user, or by server for background jobs without a user.

# Alternatives

A session-ID-only capability is simpler but lets any authenticated user with a
leaked UUID finish another user's upload. Per-handler limits let the WebSocket
and generated RPC consoles diverge. A persistent command lease would add TTL
and crash-recovery uncertainty for no defensive gain inside one process.

# Consequence

New user-facing upload operations, including consumers of a completed session,
must use the owner-aware manager methods and pass the identity from the request
context. New command entry points go through
`command.Sender` to inherit both bounds. Legacy unowned sessions and internal
callers without a user context keep their existing behavior; the limits are
in-process and are not a cross-process quota.
