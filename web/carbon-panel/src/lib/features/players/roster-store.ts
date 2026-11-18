// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

/**
 * Roster state machine, independent of the RPC client so it can be tested in
 * the repository's plain Bun runner. A generation counter keeps a slow
 * response from a previous filter from overwriting the current one, and a
 * failed refresh marks the existing rows stale rather than pretending the
 * collection is empty.
 */

import { create } from '@bufbuild/protobuf';
import { writable, type Readable } from 'svelte/store';
import { formatRpcError } from '$lib/api/rpc-error';
import {
	ListPlayersRequestSchema,
	type ListPlayersRequest,
	type ListPlayersResponse,
	type PlayerSummary
} from '$lib/proto/carbonpanel/v1/player_pb';
import type { RosterFilter } from './presentation';

export interface PlayersState {
	players: PlayerSummary[];
	loading: boolean;
	/** A previous load succeeded but the latest refresh failed. */
	stale: boolean;
	error: string | null;
	loaded: boolean;
	sourceDetail: string;
}

export const INITIAL_PLAYERS_STATE: PlayersState = {
	players: [],
	loading: false,
	stale: false,
	error: null,
	loaded: false,
	sourceDetail: ''
};

export type ListPlayers = (request: ListPlayersRequest) => Promise<ListPlayersResponse>;

export interface PlayersStore extends Readable<PlayersState> {
	load(filter: RosterFilter): Promise<void>;
	reset(): void;
}

export function createPlayersStore(list: ListPlayers): PlayersStore {
	const state = writable<PlayersState>({ ...INITIAL_PLAYERS_STATE });
	let generation = 0;

	async function load(filter: RosterFilter): Promise<void> {
		const mine = ++generation;
		state.update((current) => ({ ...current, loading: true, error: null }));
		try {
			const request = create(ListPlayersRequestSchema, {
				serverId: filter.serverId,
				query: filter.query,
				stateFilter: filter.stateFilter,
				accessFilter: filter.accessFilter,
				sortBy: filter.sortBy,
				sortDescending: filter.sortDescending,
				limit: 0
			});
			const response = await list(request);
			if (mine !== generation) return;
			state.set({
				players: response.players ?? [],
				loading: false,
				stale: false,
				error: null,
				loaded: true,
				sourceDetail: response.sourceDetail ?? ''
			});
		} catch (error) {
			if (mine !== generation) return;
			state.update((current) => ({
				...current,
				loading: false,
				stale: current.loaded,
				error: formatRpcError(error, 'Could not load players. Check the connection and retry.')
			}));
		}
	}

	function reset(): void {
		generation++;
		state.set({ ...INITIAL_PLAYERS_STATE });
	}

	return { subscribe: state.subscribe, load, reset };
}
