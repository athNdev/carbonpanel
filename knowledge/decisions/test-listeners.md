---
type: Decision
title: Instance owned proxy test sockets
description: Proxy tests inject instance-owned socket factories and reject non-loopback binds before reaching the operating system.
status: stable
owners: [oss-runtime]
code_refs:
  - internal/proxy
  - internal/module/manager_test.go
  - docs/testing/windows-verification.md
sources:
  - title: Native Windows verification
    resource: ../../docs/testing/windows-verification.md
---

# Choice

Proxy constructors accept instance-owned socket factories. Tests reject public
or unspecified bind addresses before creating sockets, and retain real loopback
TCP/UDP coverage. Production defaults keep public ingress and independent UDP
backend source-address selection.

# Alternatives

Mutable package-global hooks let concurrent fixtures change each other's socket
policy. Firewall exceptions for temporary test binaries depend on workstation
state and change on each rebuild. Skipping native tests loses OS coverage.

# Consequence

Every new listener path must pass through the owning instance's factory. Ingress
and backend UDP sockets have separate purposes and both need explicit fixture
policy. Normal tests do not change Windows security settings or request elevation.
