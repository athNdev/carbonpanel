// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL-2.0 License was distributed with this
// file, You can obtain one at https://mozilla.org/MPL-2.0/.

/**
 * The server detail page publishes a fresh `Server` object on every telemetry
 * poll. An effect that simply reads `server.id` re-runs on each of those
 * refreshes, so force-closing the allowlist dialog inside such an effect makes
 * it disappear mid-interaction (and mid-capture) even though the server did
 * not change.
 *
 * A dialog only has to close when it would apply to a different server, or
 * when its tab is no longer active. Repeated observations of the same id are
 * not a reason to close it.
 *
 * `previousServerId === null` means no observation yet, so the initial effect
 * run never closes a dialog that is already open.
 */
export function allowlistDialogShouldClose(
	previousServerId: string | null,
	serverId: string,
	active: boolean
): boolean {
	return previousServerId !== null && (previousServerId !== serverId || !active);
}
