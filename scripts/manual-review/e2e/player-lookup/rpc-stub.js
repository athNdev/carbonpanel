// SPDX-License-Identifier: MPL-2.0
import { Code, ConnectError } from '@connectrpc/connect';
// Scoped, fixture-only RPC stub. No live provider, server or controller is
// contacted. Responses are selected by the QA driver via globalThis.__qa.
const state = {
	lookup: 'found',
	whitelistAdd: 'success',
	calls: []
};

function found(name, uuid) {
	return {
		status: 1, // PlayerLookupStatus.FOUND
		name,
		uuid,
		observation: {
			source: 7, // IDENTITY_SERVICE
			confidence: 1, // DETECTED
			observedAt: { seconds: 1763462400, nanos: 0 },
			applicability: '',
			conflictNote: 'Ephemeral provider result; no identity was created.'
		},
		note: 'Ephemeral result: only the provider\'s current name is shown. No identity, roster entry or presence was created. Official name history is unavailable.'
	};
}

const FOUND = found(
	'UnseenPlayer',
	'069a79f4-44e9-4726-a5be-fca90e38aaf5'
);
const STALE_FOUND = found(
	'StaleResult',
	'11111111-2222-3333-4444-555555555555'
);
const CURRENT_FOUND = found(
	'CurrentResult',
	'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee'
);

const NOT_FOUND = {
	status: 2,
	name: '',
	uuid: '',
	observation: null,
	note: 'The identity service returned no profile for that exact username.'
};

const AMBIGUOUS = {
	status: 3,
	name: '',
	uuid: '',
	observation: null,
	note: 'More than one observed identity matches that name. Use the exact UUID to disambiguate.'
};

const UNAVAILABLE = {
	status: 4,
	name: '',
	uuid: '',
	observation: null,
	note: 'Identity service unavailable; try again later. identity service request failed'
};

export const rpcClient = {
	player: {
		async lookupPlayer(request) {
			state.calls.push({ method: 'lookupPlayer', query: request.query });
			switch (state.lookup) {
				case 'not_found':
					return NOT_FOUND;
				case 'ambiguous':
					return AMBIGUOUS;
				case 'unavailable':
					return UNAVAILABLE;
				case 'race':
					// First (stale) reply arrives late; the second (current)
					// reply arrives first. The UI must discard the stale one.
					if (state.calls.filter((c) => c.method === 'lookupPlayer').length === 1) {
						return new Promise((resolve) =>
							setTimeout(() => resolve(STALE_FOUND), 700)
						);
					}
					return CURRENT_FOUND;
				default:
					return FOUND;
			}
		},
		async setWhitelistEntry(request) {
			state.calls.push({
				method: 'setWhitelistEntry',
				serverId: request.serverId,
				playerQuery: request.playerQuery,
				remove: request.remove,
				requestId: request.requestId
			});
			if (state.whitelistAdd === 'denied') {
				throw new ConnectError(
					'permission_denied: no players:access:write on this server',
					Code.PermissionDenied
				);
			}
			if (state.whitelistAdd === 'bedrock_capability') {
				throw new ConnectError(
					'server "srv-1" edition "bedrock": Bedrock allowlist edits require an adapter that proves Gamertag/XUID support; none is available',
					Code.FailedPrecondition
				);
			}
			if (state.whitelistAdd === 'failure') {
				return {
					outcome: {
						results: [
							{
								identityKey: 'pl_fixture',
								success: false,
								message: 'whitelist add failed: rcon unavailable',
								errorCode: 'source_error'
							}
						]
					}
				};
			}
			return {
				outcome: {
					results: [
						{
							identityKey: 'pl_fixture',
							success: true,
							message: 'allowlist_add applied via rcon',
							undoSupported: true,
							undoToken: 'undo_fixture_1'
						}
					]
				}
			};
		},
		async undoPlayerAction(request) {
			state.calls.push({
				method: 'undoPlayerAction',
				serverId: request.serverId,
				undoToken: request.undoToken,
				requestId: request.requestId
			});
			return {
				outcome: { results: [{ identityKey: 'pl_fixture', success: true, message: 'undo applied via rcon' }] }
			};
		}
	}
};

globalThis.__qa = {
	set(patch) {
		Object.assign(state, patch);
	},
	calls() {
		return [...state.calls];
	},
	reset() {
		state.calls = [];
		state.lookup = 'found';
		state.whitelistAdd = 'success';
	}
};
