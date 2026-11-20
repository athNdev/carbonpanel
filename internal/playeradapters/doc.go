// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package playeradapters provides source-labelled adapters for Java player
// presence and a small set of typed moderation actions.
//
// This package never falls back to controller-local state, never reads server
// files, and never executes an arbitrary command string supplied by a caller:
// RCON and exec commands are built only from fixed, allowlisted skeletons with
// strictly validated names and reasons. Secrets are redacted as ******** in
// every detail and error produced here.
//
// Adapters are chosen by Negotiate in a fixed order: the native Minecraft
// Server Management Protocol (Java 1.21.9 and 1.21.10), then Java RCON, then a caller
// supplied node-scoped ExecFunc. An unavailable source is downgraded, never
// invented. Optional plugin-bridge capabilities (vitals, inventory, groups,
// mute) and Bedrock parity are reported as "capability not verified / decision
// pending" (decision gates D3/D6 and the OSS-353 gate); no fake capability is
// claimed.
//
// The v1 management client uses the Java 1.21.9 wire format. No-argument
// calls omit params. Mutations use named arrays and notifications carry
// arrays of player records. Vanilla 1.21.9 was exercised with two players,
// including join, kick, leave, allowlist, ban and operator changes.
// The same profile is selected for the 1.21.10 hotfix. Later protocol
// revisions require a separate profile. See ADR 0017.
//
// Ownership and lifetime: ManagementAdapter runs a bounded background reader
// for its WebSocket. The owner must call Close when the server or controller
// shuts down. Reader goroutines stop when the connection closes and outstanding
// calls fail with a generation error instead of hanging. Notifications are
// advisory signals carrying their own observed-at time and source; they never
// mutate the authoritative roster returned by List. Callers that need strict
// ordering compare Notification.ObservedAt with ManagementAdapter.Snapshot.
package playeradapters
