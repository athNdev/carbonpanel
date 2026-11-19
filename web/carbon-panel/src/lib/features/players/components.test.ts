// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

function source(name: string): string {
	return readFileSync(new URL(`./${name}`, import.meta.url), 'utf8');
}

test('PlayerProvenance renders source, confidence, timestamp and conflict as tagged text', () => {
	const s = source('PlayerProvenance.svelte');
	assert.match(s, /<CarbonTag/, 'must use the shared tag primitive');
	assert.match(s, /mono-label-accent|mono-label/, 'must use the shared mono-label utility');
	assert.match(s, /view\.source/, 'renders the observation source');
	assert.match(s, /view\.confidence/, 'renders the observation confidence');
	assert.match(s, /view\.observedAt/, 'renders the observation time');
	assert.match(s, /view\.conflictNote/, 'renders conflict notes');
	assert.match(s, /No provenance reported/, 'honest fallback when no observation exists');
	assert.doesNotMatch(
		s,
		/#[0-9a-fA-F]{6}(?:[0-9a-fA-F]{2})?\b/,
		'no page-local arbitrary hex colors'
	);
});

test('player detail keeps Character and Progress deep fields in separate state', () => {
	const s = readFileSync(
		new URL('../../../routes/players/[key]/+page.svelte', import.meta.url),
		'utf8'
	);
	assert.match(s, /deepFieldsByTab/, 'deep fields are keyed by tab');
	assert.match(s, /withTabFields\(deepFieldsByTab, tab/, 'each section writes its own tab key');
	assert.match(s, /tabFields\(deepFieldsByTab, activeTab\)/, 'render reads the active tab fields');
	assert.doesNotMatch(
		s,
		/\bdeepFields\s*=/,
		'no shared deepFields array that one tab can overwrite for another'
	);
});

test('PlayerRoster derives server options without an effect write loop', () => {
	const s = source('PlayerRoster.svelte');
	assert.match(
		s,
		/const options = \$derived\(projectServerOptions\(/,
		'server options are derived purely from scoped roster rows, inventory and scope'
	);
	assert.doesNotMatch(
		s,
		/knownServers/,
		'no effect-owned server map that is read and rewritten in the same cycle'
	);
	// Any remaining effect must not assign to a map keyed by server id.
	assert.doesNotMatch(
		s,
		/\$effect\([\s\S]{0,400}(knownServers|serverNames)\s*=/,
		'no effect writes a server-name map'
	);
});

test('PlayerRoster has accessible sortable headers, keyboard map and scoped states', () => {
	const s = source('PlayerRoster.svelte');
	assert.match(s, /aria-sort=/, 'sortable headers expose aria-sort');
	assert.match(s, /Select all visible players/, 'bulk selection has a group label');
	assert.match(s, /event\.key === '\/'/, '/ focuses the search field');
	assert.match(s, /event\.key === 'j'/, 'j moves focus down');
	assert.match(s, /event\.key === 'k'/, 'k moves focus up');
	assert.match(s, /event\.key === 'x'/, 'x toggles selection');
	assert.match(s, /event\.key === 'Enter'/, 'Enter opens the detail view');
	assert.match(s, /aria-live="polite"/, 'updates are announced politely');
	assert.match(s, /CarbonErrorState/, 'failed loads surface an error state');
	assert.match(s, /CarbonEmptyState/, 'empty results surface an empty state');
	assert.match(s, /CarbonInlineLoading/, 'initial loading is announced');
	assert.match(s, /Stale results/, 'a failed refresh marks rows stale');
	assert.match(s, /resolve\('\/players\/\[key\]'/, 'navigation uses typed resolve()');
	assert.doesNotMatch(
		s,
		/#[0-9a-fA-F]{6}(?:[0-9a-fA-F]{2})?\b/,
		'no page-local arbitrary hex colors'
	);
});

test('PlayerActions confirms with reason, targets and side effects, and gates by permission', () => {
	const s = source('PlayerActions.svelte');
	assert.match(s, /<CarbonConfirm/, 'destructive actions use the shared confirm primitive');
	assert.match(s, /confirming=\{busy\}/, 'confirm button reflects in-flight state');
	assert.match(s, /Reason \(optional\)/, 'confirmation captures a reason');
	assert.match(s, /previewTargets\(targets\)/, 'confirmation lists the affected targets');
	assert.match(s, /actionSideEffectNote\(action\)/, 'confirmation explains side effects');
	assert.match(
		s,
		/authStore\.hasPermission\('players', permissionActionFor\(action\), serverId\)/,
		'UI gating maps the UI action to the RBAC action that authorizes the RPC'
	);
	assert.match(s, /newRequestId\(\)/, 'every action carries a request id');
	assert.match(s, /bulkPlayerAction/, 'bulk actions use the typed RPC');
	assert.match(s, /undoPlayerAction/, 'undo uses the typed RPC');
	assert.doesNotMatch(s, /sendCommand|SendCommandRequestSchema/, 'no legacy command fallback');
	assert.doesNotMatch(
		s,
		/#[0-9a-fA-F]{6}(?:[0-9a-fA-F]{2})?\b/,
		'no page-local arbitrary hex colors'
	);
});

test('undo is disabled unless the result supports it and shows a reason otherwise', () => {
	const s = source('PlayerActions.svelte');
	assert.match(
		s,
		/canUndoResult\(result\)/,
		'undo eligibility comes from the result flag and token'
	);
	assert.match(s, /Automatic undo isn't available here\./, 'a disabled undo is explained in text');
	assert.match(s, /undoUnavailableReason\(action\)/, 'the reason names the action');
	assert.match(
		s,
		/if \(busy \|\| undoable\.length === 0\) return;/,
		'undo handler also refuses ineligible undo'
	);
});

test('skin reads are scoped to the player server for server-scoped moderators', () => {
	const avatar = readFileSync(
		new URL('../../components/player-avatar.svelte', import.meta.url),
		'utf8'
	);
	assert.match(avatar, /getPlayerSkin/, 'skins come from the same-origin RPC');
	assert.match(avatar, /serverId: scope/, 'the skin read carries the known server scope');
	assert.match(avatar, /kind: 'face'/, 'the face asset is requested');

	const roster = source('PlayerRoster.svelte');
	assert.match(
		roster,
		/serverId=\{player\.serverId\}/,
		'each row passes its own server id, not the (possibly empty) roster scope'
	);
	assert.doesNotMatch(roster, /skinRef=\{player\.skinRef\}\s*\n\s*\{serverId\}/);
});

test('PlayerLookup discloses every unresolved state and never invents an identity', () => {
	const s = source('PlayerLookup.svelte');
	assert.match(s, /lookupPlayer\(/, 'uses the exact-identity lookup RPC');
	assert.match(s, /LookupPlayerRequestSchema/, 'builds the typed request');
	assert.match(s, /PlayerLookupStatus\.FOUND/, 'handles found');
	assert.match(s, /PlayerLookupStatus\.NOT_FOUND/, 'handles not found');
	assert.match(s, /PlayerLookupStatus\.AMBIGUOUS/, 'handles ambiguous');
	assert.match(s, /PlayerLookupStatus\.UNAVAILABLE/, 'handles unavailable');
	assert.match(s, /No identity was created/, 'unresolved states state that nothing was invented');
	assert.match(
		s,
		/creates no identity, roster entry or presence claim/,
		'discloses the read-only contract'
	);
	assert.match(s, /no name history is queried or shown/, 'discloses no name history');
	assert.match(s, /outage or timeout/, 'outage is distinct from authoritative not found');
	assert.match(s, /mine !== generation/, 'stale provider replies are discarded');
	assert.match(s, /hasPermission\('players', 'read', '\*'\)/, 'gated by the global read grant');
	assert.match(s, /aria-live="polite"/, 'status changes are announced');
	assert.match(s, /onsubmit=/, 'Enter submits the form for keyboard users');
	assert.doesNotMatch(
		s,
		/#[0-9a-fA-F]{6}(?:[0-9a-fA-F]{2})?\b/,
		'no page-local arbitrary hex colors'
	);
});

test('AddAllowlistEntry uses the server-scoped typed path with journal and undo', () => {
	const s = source('AddAllowlistEntry.svelte');
	assert.match(s, /playerQuery: value/, 'sends the exact name/UUID typed by the operator');
	assert.match(s, /serverId/, 'scopes the add to this server');
	assert.match(
		s,
		/SetWhitelistEntryRequestSchema/,
		'reuses the moderation RPC, not a command path'
	);
	assert.match(s, /undoPlayerAction/, 'offers undo through the existing journal');
	assert.match(
		s,
		/hasPermission\('players', 'access:write', serverId\)/,
		'gated by server-scoped access:write'
	);
	assert.match(s, /Bedrock XUID allowlists are not supported yet/, 'edition boundary is disclosed');
	assert.match(s, /aria-live="polite"/, 'outcomes are announced');
	assert.doesNotMatch(s, /sendCommand|SendCommandRequestSchema/, 'no legacy command fallback');
});

test('permissionActionFor maps UI actions to real RBAC actions', async () => {
	const { permissionActionFor } = await import('./presentation');
	assert.equal(permissionActionFor('whitelist_add'), 'access:write');
	assert.equal(permissionActionFor('whitelist_remove'), 'access:write');
	assert.equal(permissionActionFor('op'), 'moderate:op');
	assert.equal(permissionActionFor('kick'), 'moderate');
});

test('capability diagnostics show actionable text instead of fake controls', () => {
	const s = source('../../../routes/players/[key]/+page.svelte');
	assert.match(s, /capabilityDetail\(/, 'renders the actionable capability explanation');
	assert.match(s, /supportStateLabel\(capability\.state\)/, 'renders readable support state');
	assert.match(
		s,
		/Actions are shown only when your permissions allow them/,
		'action visibility is explained in plain language'
	);
	assert.doesNotMatch(
		s,
		/#[0-9a-fA-F]{6}(?:[0-9a-fA-F]{2})?\b/,
		'no page-local arbitrary hex colors'
	);
});
