// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package native implements the HOST-NATIVE LINUX PRIVATE DAEMON deployment
// mode of the managed node runtime (OSS-376, parent OSS-368).
//
// This package is mode-specific. It does NOT define or fork the shared
// enrollment/control contract: identity, enrollment messages, capabilities,
// health and operation envelopes are the proto types in
// proto/carbonpanel/v1/node_control.proto (ADR 0019, PR516/517/518). The
// enrollment boundary in this package implements that shared scheme against
// those shared types; nothing here changes the wire contract or the shared
// database columns.
//
// Mode invariants (all enforced as named, fail-closed gates):
//
//   - a dedicated daemon process owned by a node agent, never a preinstalled
//     or shared Docker daemon and never a fallback to one;
//   - a PRIVATE Unix socket, PRIVATE data/exec roots and a PRIVATE bridge /
//     network namespace, with no public raw Docker API listener;
//   - rootless where the host is qualified, with exact refusals when a
//     prerequisite is absent; a hardened dedicated rootful service is the
//     explicit compatibility path, never a silent privilege upgrade;
//   - the ADR 0019 enrollment scheme: node-generated secret, REQUIRED approved
//     public key, CSR_PRE as registration material, a challenge-bound fresh
//     SIG_CLAIM, single-use atomic consume, TLS topology support/refusal and
//     three non-crossing authorization domains. No secret appears in a read
//     response or in a stored fixture.
//
// No code in this package starts a daemon, changes host configuration, mounts
// a real socket or provisions a live node. Preflight consumes injected facts,
// and the enrollment authority uses an owned in-memory store with synthetic
// keys. A real host probe and the agent entrypoint live in cmd/carbon-node.
package native
