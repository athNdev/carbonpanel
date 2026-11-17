// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

/**
 * Production player roster store. The state machine lives in
 * `features/players/roster-store.ts` so it can be tested without the RPC
 * client; this module only supplies the real RPC adapter.
 */

import { rpcClient } from '$lib/api/rpc-client';
import { createPlayersStore, type PlayersState } from '$lib/features/players/roster-store';

export {
	createPlayersStore,
	INITIAL_PLAYERS_STATE,
	type ListPlayers,
	type PlayersStore,
	type PlayersState
} from '$lib/features/players/roster-store';

/** Production singleton. */
export const playersStore = createPlayersStore((request) => rpcClient.player.listPlayers(request));
