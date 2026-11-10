// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import type { Server } from '$lib/proto/carbonpanel/v1/common_pb';

/** Fresh successful SLP/RCON only. Last-good join is not current playable state. */
export function joinIsPlayable(server: Pick<Server, 'joinObservationKind'>): boolean {
	return server.joinObservationKind === 'playable';
}

export function joinKindLabel(kind: string | undefined): string {
	switch (kind) {
		case 'playable':
			return 'playable';
		case 'unjoinable':
			return 'unjoinable';
		case 'unobserved':
			return 'unobserved';
		case 'stale':
			return 'stale';
		case 'timed_out':
			return 'timed out';
		default:
			return kind || 'unknown';
	}
}
