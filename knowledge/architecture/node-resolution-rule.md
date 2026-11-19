---
type: Architecture
title: "Node resolution — Rule + Why"
description: "A server belongs to a node. An operation that targets a remote node must obtain"
status: stable
owners: [oss-runtime]
code_refs: [internal/docker, internal/docker/locator.go, internal/module, internal/snapshot, internal/docker/freezer.go, internal/metrics, internal/nodestorage]
sources:
  - title: Parent concept
    resource: node-resolution.md
---
# Rule

A server belongs to a node. An operation that targets a remote node must obtain
that node's Docker client or return an error. It must never fall back to the
controller's local Docker socket.

# Why

A silent fallback can run a module, snapshot or sleep operation against the
wrong host. That is a data-safety bug, not a degraded mode.
