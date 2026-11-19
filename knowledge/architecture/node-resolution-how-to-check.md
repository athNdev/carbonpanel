---
type: Architecture
title: "Node resolution — How to check + Read next"
description: "For any new operation, follow the path from the server record to the Docker"
status: stable
owners: [oss-runtime]
code_refs: [internal/docker, internal/docker/locator.go, internal/module, internal/snapshot, internal/docker/freezer.go, internal/metrics, internal/nodestorage]
sources:
  - title: Parent concept
    resource: node-resolution.md
---
# How to check

For any new operation, follow the path from the server record to the Docker
client. Add a test where the node cannot be resolved and assert an error.

# Read next

- [Failure modes](../failures/remote-node-fallback.md)
- [Module system](module-system.md)
