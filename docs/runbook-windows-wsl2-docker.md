# Runbook: panel on Windows against a Docker daemon in WSL2

A supported deployment shape for development and single-operator use: the
Carbon Panel process runs on Windows, the Docker daemon runs inside a WSL2
distro. The panel's filesystem and the daemon's are different machines, so
the deployment must make both sides agree on where server data lives.

<!-- SPDX-License-Identifier: MPL-2.0 -->

## 1. Daemon endpoint

The panel's daemon detection probes TCP 2375/2376 on loopback and every up
interface. Run dockerd in the distro with a TCP listener (systemd drop-in
or `dockerd -H tcp://127.0.0.1:2375`), reachable from Windows either
through WSL2 localhost forwarding or a small loopback TCP forwarder that
resolves the distro's current NAT address. `tcp://` endpoints are
unauthenticated: bind to loopback only.

## 2. Storage layout

Server data must be one directory tree that both sides see:

- Keep the tree on the distro's native filesystem (fast world I/O, real
  POSIX ownership for the game user).
- Point `storage.data_dir`, `storage.backup_dir` and `storage.temp_dir` at
  it through the `\\wsl.localhost\<distro>\...` UNC path.
- Set `CARBONPANEL_HOST_DATA_PATH` to the same tree as the daemon sees it
  (for example `/root/carbon-panel/data`). The panel translates every bind
  source under the data root at container create; see
  `internal/hostmount` and the startup log line "maps to ... on the Docker
  host".

Never place the panel's SQLite database on a 9P/network path: keep
`database.path` on the Windows filesystem.

## 3. Container network egress

Docker installs MASQUERADE and per-bridge FORWARD rules when a network is
created. If containers cannot reach the internet while the daemon host can
(test with a container probe, not only from the host), the bridge's filter
rules are incomplete. `docker network inspect` the network's bridge id,
then compare all four chains — `DOCKER-FORWARD` (`-i` accept),
`DOCKER-CT` (`-o` established), `DOCKER-BRIDGE` (`-o` → `DOCKER`) and the
NAT `POSTROUTING` MASQUERADE — against a freshly created test network, and
restore the missing rules in the same shape. Recreating an empty network
also reinstalls them; a network with active endpoints cannot be removed.

## 4. Verification

Create a server through the panel and require: the create journal shows a
successful create and start, the container reaches running, and the boot
log shows the itzg init completing on the bound world. A mount error
mentioning backslashes or "mount path must be absolute" means the path
mapping from section 2 is missing or inverted.
