---
type: Architecture
title: Server data ownership
description: The panel and the game container share a bind mount but not a uid, so anything the panel writes must be handed to the game container's user. Existing /data matching the game uid sets SKIP_CHOWN so a restart does not recursively chown.
status: stable
owners: [oss-runtime]
code_refs:
  - pkg/files/ownership.go
  - internal/rpc/services/file.go
  - internal/hostmount
  - internal/docker/runtime_profile.go
sources:
  - title: Contributor guide
    resource: ../../AGENTS.md
---

# Shape

A server's data directory is a bind mount shared by two processes that
usually run as different users: the panel runs as root, the game container
runs unprivileged (uid 1000 in the itzg images). The directory itself is
created by the game on first start and belongs to the game's user.

`files.AdoptOwnership(reference, target, recursive)` copies the data
directory's uid/gid onto a path the panel just wrote. It is applied after
archive extraction, file upload, file edit, and after a restore swaps the
tree (which captures the previous owner *before* the swap).

# Rules

- A file the panel writes into a server's data directory is not finished
  until its owner matches the directory's owner. A root-owned
  `server.properties` makes the game crash-loop on boot while looking like a
  mod failure.
- Adopt from the directory, never from a hard-coded uid: the game image, an
  operator override or a rootless daemon can all change who runs the game.
- Adoption is best-effort and never fails the write: it is a portability
  detail, and a platform without uid/gid (Windows) is a no-op, not an error.
- Do not "fix" a permission problem by making the root filesystem writable or
  by running the game container privileged. The ownership is the contract.
- When the world directory already belongs to the game uid, create sets
  `SKIP_CHOWN=TRUE` so the itzg entrypoint does not recursively chown `/data`
  on every restart. A fresh directory still owned by host root leaves
  `SKIP_CHOWN` unset so first boot can prepare ownership once.

# Read next

- [Runtime model](runtime.md)
- [Data safety](../invariants/data-safety.md)
