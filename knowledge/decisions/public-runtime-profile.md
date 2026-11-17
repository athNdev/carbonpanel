---
type: Decision
title: Public runtime profile contract
description: Effective Docker runtime and storage checks are a public package so Panel and Cloud share one implementation.
status: stable
owners: [oss-runtime]
code_refs:
  - pkg/runtimeprofile
  - internal/docker/runtime_profile.go
  - internal/docker/client.go
sources:
  - title: Runtime model
    resource: ../architecture/runtime.md
  - title: Container privileges
    resource: ../invariants/container-privileges.md
---

# Decision

`pkg/runtimeprofile` is the supported contract for effective Docker config,
image bootstrap, world ownership, and storage admission. Panel's create path
is one caller. Cloud's node agent is the other. The package takes a resolved
container request and daemon capability flags. It does not import the panel
database, RPC handlers, sessions, or transport protobuf.

# Alternatives

- Leaving the checks in `internal/docker` blocks Cloud, because Go cannot
  import another module's internal packages.
- Copying the file into Cloud would fork the issue codes and the
  layer-cap versus admission distinction.
- Exporting all of `internal/docker` would publish single-tenant create,
  override, and daemon client behavior that Cloud must not take.

# Consequence

A new finding is added in `pkg/runtimeprofile` and covered by `RunContract`.
Panel keeps operator overrides, permissions, and the Docker client. Admission
numbers, tenant ownership, and commercial limits stay with the caller. The
runtime model states which caps the daemon actually applies. Loader and content
validation stay in `internal/minecraft`.
