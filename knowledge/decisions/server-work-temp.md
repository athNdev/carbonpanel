---
type: Decision
title: Server work temp is on the data volume
description: Modpack downloads, extracts, and native libraries use /data/.tmp, not the 64 MiB noexec /tmp tmpfs.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/docker/work_temp.go
  - internal/docker/client.go
  - internal/scheduler/backup.go
sources:
  - title: Runtime model
    resource: ../architecture/runtime.md
---

# Decision

Minecraft server work files go to `/data/.tmp` on the world bind. `/tmp` stays
a 64 MiB noexec tmpfs.

# Alternatives

- A larger `/tmp` tmpfs still caps the archive and consumes RAM. A modpack
  bigger than the cap fails while the world disk has space.
- Making `/tmp` executable would let native libraries load and would also let
  anything written to the world-writable tmpfs run. The noexec mount stays.
- A workstation path or the panel's `storage.temp_dir` is not the disk the
  game container can see on every node.

# Consequence

`TMPDIR` and `-Djava.io.tmpdir` point at `/data/.tmp`. Do not send modpack
downloads, extracts, or native-library work back to `/tmp`. When the panel
can see the data path, the directory is mode 01777 so the game user can
write it without a hard-coded uid. On a remote daemon the directory is a
bind mount created with the container. World backups omit `.tmp`.
