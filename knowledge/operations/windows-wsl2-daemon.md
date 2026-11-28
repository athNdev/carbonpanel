---
type: Runbook
title: Windows panel with a WSL2 Docker daemon
description: "The split-filesystem deployment: path mapping, storage layout and container egress."
status: stable
owners: [oss-runtime]
code_refs:
  - internal/hostmount
  - internal/docker/work_temp.go
  - docs/runbook-windows-wsl2-docker.md
sources:
  - title: Runbook
    resource: ../../docs/runbook-windows-wsl2-docker.md
---

# Panel on Windows, daemon in WSL2

- The panel's filesystem and the daemon's are different machines. The
  `CARBONPANEL_HOST_DATA_PATH` override (or a container mount) is what maps
  the panel's data root onto the daemon's view; without it, container
  creation passes a Windows path to a Linux daemon and the create journal
  fails with "mount path must be absolute".
- Keep server data on the distro's native filesystem and set
  `CARBONPANEL_HOST_DATA_PATH` to that Linux path; the panel reads and
  writes the same tree over the `\\wsl.localhost` UNC. The SQLite database
  stays on the Windows filesystem — never on a 9P path.
- Bind sources are daemon-side paths: never join or manipulate them with
  the panel OS's `filepath` helpers (the work-temp mount regression is the
  reference bug).
- If containers have no internet while the daemon host does, compare the
  network bridge's rules in `DOCKER-FORWARD`, `DOCKER-CT`,
  `DOCKER-BRIDGE` and NAT `POSTROUTING` against a freshly created test
  network; dockerd installs all four at network creation.

# Read next

- [Runbook](../../docs/runbook-windows-wsl2-docker.md)
- [Server data ownership](../architecture/server-data-ownership.md)
