---
type: Architecture
title: "Runtime model — Runtime profile"
description: "Before the daemon is asked to create a container, docker.PreflightRuntime"
status: stable
owners: [oss-runtime]
code_refs: [cmd/carbon-panel, internal/rpc, internal/proxy, internal/db, internal/docker, internal/docker/resource_enforcement.go, pkg/runtimeprofile, web/carbon-panel/src/lib/utils/resource-enforcement.ts, pkg/files, internal/scheduler, internal/operation, internal/reconciler, web/carbon-panel]
sources:
  - title: Parent concept
    resource: runtime.md
---
# Runtime profile

Before the daemon is asked to create a container, `docker.PreflightRuntime`
describes the request that was actually resolved: the user the container runs
as, whether the root is read-only, the writable paths, the capability set, the
tmpfs mounts and the network mode. A fatal finding refuses the create with
`ErrRuntimeIncompatible` and a named reason, so an unsupported image, mount or
override fails before it replaces a working container. A warning is logged and
does not block. A moving Minecraft image tag is a warning (`image_unpinned`),
not a boot refusal; pin `name@sha256:<digest>` to keep recreates stable. The
profile records `ImageDigest` and `ImagePinned` only when the reference is
pinned. On a rootless daemon, the default root-then-drop bootstrap is refused
when the world directory is still owned by host uid 0 (`rootless_host_root_data`);
chown the data to the game user first. Daemon rootless mode is read from Engine
`GET /info` during create preflight. The rules and their remedies live in `pkg/runtimeprofile`. Panel's
`CreateContainer` path builds a `RuntimeSpec` after overrides and calls that
package through `docker.DescribeRuntime` / `docker.PreflightRuntime`. Cloud
imports the same package; it does not copy the checks. Supported and unsupported combinations
are encoded as a table against the same resolved request `CreateContainer`
builds (`DescribeRuntime` / `PreflightRuntime`): root-then-drop versus an
explicit non-root uid, a read-only root with and without the nsswitch skip,
`CapDrop ALL` with bootstrap capabilities removed, a missing writable `/data`,
rootless plus host-root-owned data, and an unpinned image warning. Existing
`/data` that already belongs to the game uid gets `SKIP_CHOWN=TRUE` so a
restart does not recursively chown; a fresh root-owned directory keeps
bootstrap chown. Root-then-drop with a uid/gid other than the image's
1000:1000 is refused on a read-only root (`read_only_root_blocks_usermod`)
because usermod/groupmod cannot rewrite `/etc`. Start as that numeric user
instead, or keep 1000:1000. Extra override mounts that shadow `/etc`, `/proc`,
`/sys`, `/dev`, or the Docker socket are fatal (`sensitive_mount_target`); a
bind that replaces `/tmp` is a warning (`tmpfs_replaced`). The last `/data`
mount is the effective world bind. Preflight
must not enable privileged mode, disable no-new-privileges, or make the root
writable to recover. Live game-readiness on an isolated daemon is OSS-247;
the in-repo matrix still runs without that daemon.

