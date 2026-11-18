// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package enroll is the single mode-agnostic production enrollment and
// authorization boundary for managed nodes (OSS-385).
//
// It implements the ADR 0019 scheme against the SHARED node_control.proto
// contract: no proto fork, no per-mode signing context and no second
// certificate policy. Host-native, VM and LXC mode lanes consume this package;
// they must not reimplement proof of possession, the claim context, issuance or
// the three authorization domains.
//
// The boundary is deliberately unmounted: no production RPC handler is
// registered here and no live host, credential or deployment is touched. Keys
// and certificates are owned/synthetic, the secret S is never stored and no
// read response carries it.
package enroll
