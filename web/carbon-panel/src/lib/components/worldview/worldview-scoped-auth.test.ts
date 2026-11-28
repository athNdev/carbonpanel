// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

// The world detail route separates read access (world_view/read) from action
// scopes (update, observe_players, servers/command). Read-only users must keep a
// usable map view, while action failures and denials stay local to that action.
const source = readFileSync(new URL('./WorldViewWorkspace.svelte', import.meta.url), 'utf8');

function blockFor(marker: string): string {
	const start = source.indexOf(marker);
	assert.ok(start >= 0, `${marker} missing`);
	const end = source.indexOf('\n\t}\n', start);
	assert.ok(end > start, `${marker} block end missing`);
	return source.slice(start, end + 3);
}

test('player visibility observation boundary does not revoke the read view', () => {
	const block = blockFor('async function togglePlayers');
	assert.ok(
		block.includes('matrix.players(canObserve).supported'),
		'observe action must be capability/permission gated'
	);
	assert.ok(
		block.includes("toast.error(formatRpcError(error, 'Could not change player visibility.'))"),
		'visibility failures must surface as an action error'
	);
	assert.ok(
		!block.includes('handleError(error)'),
		'observe_players denial must not trigger page-level denied state'
	);
});

test('overlay edit boundary does not revoke the read view', () => {
	const block = blockFor('async function saveOverlay');
	assert.ok(
		block.includes('worldView.setMapOverlay'),
		'overlay saves use the scoped world-view mutation'
	);
	assert.ok(
		block.includes("toast.error(formatRpcError(error, 'Could not save the overlay.'))"),
		'overlay save failures must surface as an action error'
	);
	assert.ok(
		!block.includes('handleError(error)'),
		'world_view/update denial must not trigger page-level denied state'
	);
});

test('read-scope loaders remain usable and still gate view access on read denials', () => {
	assert.match(
		source,
		/let canRead = \$derived\(checkPermission\(auth\.permissions, 'world_view', 'read', serverId\)\);/
	);
	assert.match(
		source,
		/async function loadSession[\s\S]*?handleError\(error\)/,
		'session/read failures must be handled as view access failures'
	);
	assert.match(
		source,
		/async function loadCapabilities[\s\S]*?handleError\(error\)/,
		'capability/read failures must be handled as view access failures'
	);
	assert.match(
		source,
		/async function loadOverlays[\s\S]*?handleError\(error\)/,
		'overlay list/read failures must be handled as view access failures'
	);
});

test('map admin actions stay separated, confirmed, and user-scoped', () => {
	assert.match(
		source,
		/let canObserve = \$derived\([\s\S]*'world_view', 'observe_players', serverId/
	);
	assert.match(source, /let canUpdate = \$derived\([\s\S]*'world_view', 'update', serverId/);
	assert.match(
		source,
		/let canCommand = \$derived\(checkPermission\(auth\.permissions, 'servers', 'command', serverId\)\);/
	);
	assert.match(source, /let adminActionList = \$derived\(adminActions\(canCommand\)\);/);
	assert.ok(source.includes('<CarbonConfirm'), 'admin actions require explicit confirmation UI');
	assert.ok(
		source.includes('requestAdmin(action)'),
		'admin actions must flow through a confirmation request path'
	);
	const confirm = blockFor('async function confirmAdmin');
	assert.ok(
		confirm.includes('if (!pendingAdmin || !canCommand || !current?.()) return;'),
		'admin execution requires command permission'
	);
	assert.ok(
		confirm.includes('rpcClient.server.sendCommand'),
		'admin actions use the existing server command RPC'
	);
	assert.ok(
		confirm.includes('confirmAdminOpen = false;'),
		'successful admin action closes confirmation'
	);
	assert.ok(
		!confirm.includes('handleError(error)'),
		'admin permission/runtime failures must not revoke the read view'
	);
	assert.match(confirm, /Permission denied:[\s\S]*cannot run commands on this server/);
	assert.ok(
		source.includes('{matrix.players(canObserve).reason}'),
		'observe boundary is explained when unavailable'
	);
	assert.ok(
		source.includes('{matrix.overlayEditing(canUpdate).reason}'),
		'overlay edit boundary is explained when unavailable'
	);
});

test('map actions preserve world and position scope', () => {
	const request = blockFor('function requestAdmin');
	assert.match(request, /x === null \|\| z === null \? null : \{ x, z \}/);
	assert.match(request, /adminScope = scopeGuard\(\)/);
	const overlay = blockFor('async function saveOverlay');
	assert.match(overlay, /parseNumber\(formX\)/);
	assert.match(overlay, /dimension: overlayDraftDimension/);
	assert.match(overlay, /if \(!current\(\)\) return/);
	const players = blockFor('async function togglePlayers');
	assert.match(players, /next && !matrix\.players/);
	assert.match(players, /if \(!current\(\)\) return/);
});
