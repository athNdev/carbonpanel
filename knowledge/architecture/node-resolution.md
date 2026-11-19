---
type: Architecture
title: Node resolution
description: Every operation must resolve the Docker client for the target node and fail closed when it cannot.
status: stable
owners: [oss-runtime]
code_refs: [internal/docker, internal/docker/locator.go, internal/module, internal/snapshot, internal/docker/freezer.go, internal/metrics, internal/nodestorage]
sources:
  - title: Fail-closed node resolution change
    resource: https://github.com/athNdev/carbon-panel/pull/225
---

# Parts

- [Node resolution — Rule + Why](node-resolution-rule.md) — A server belongs to a node. An operation that targets a remote node must obtain
- [Node resolution — Current state](node-resolution-current-state.md) — Module, snapshot and deep-sleep helpers were made fail-closed and record the
- [Node resolution — Current state (part 2)](node-resolution-current-state-2.md) — before the panel row. A remote node with no reachable storage is refused,
- [Node resolution — How to check + Read next](node-resolution-how-to-check.md) — For any new operation, follow the path from the server record to the Docker
