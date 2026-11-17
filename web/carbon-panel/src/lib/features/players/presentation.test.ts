// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { create } from '@bufbuild/protobuf';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import {
	ObservationSchema,
	PlayerAliasSchema,
	PlayerCapability,
	PlayerConfidence,
	PlayerDataSource,
	PlayerFieldSchema,
	PlayerPresence,
	PlayerRefSchema,
	PlayerSummarySchema,
	PlayerSupportState,
	type PlayerSummary,
	type PlayerAlias,
	type PlayerRef
} from '$lib/proto/carbonpanel/v1/player_pb';
import type { Timestamp } from '@bufbuild/protobuf/wkt';

interface SummaryOverrides {
	ref?: PlayerRef;
	serverId?: string;
	serverName?: string;
	presence?: PlayerPresence;
	operator?: boolean;
	banned?: boolean;
	whitelisted?: boolean;
	lastSeen?: Timestamp;
	aliases?: PlayerAlias[];
}
import {
	DEFAULT_ROSTER_FILTER,
	bulkKindFor,
	canUndoResult,
	capabilityDetail,
	capabilityLabel,
	fieldValue,
	filterAndSortPlayers,
	nameHistoryNote,
	newRequestId,
	previewTargets,
	provenanceView,
	serverOptionsFromRoster,
	supportStateLabel,
	undoUnavailableReason
} from './presentation';

function summary(
	key: string,
	name: string,
	overrides: SummaryOverrides = {}
): PlayerSummary {
	return create(PlayerSummarySchema, {
		ref: create(PlayerRefSchema, { identityKey: key, name }),
		serverId: 'survival',
		serverName: 'Survival',
		presence: PlayerPresence.OFFLINE,
		...overrides
	});
}

test('roster sorts out-of-order inputs deterministically by name', () => {
	const players = [
		summary('z', 'Zara'),
		summary('a', 'Alice'),
		summary('m', 'Morgan')
	];
	const ascending = filterAndSortPlayers(players, {
		...DEFAULT_ROSTER_FILTER,
		sortBy: 'name'
	}).map((p) => p.ref?.name);
	assert.deepEqual(ascending, ['Alice', 'Morgan', 'Zara']);

	const shuffled = [players[2], players[0], players[1]];
	assert.deepEqual(
		filterAndSortPlayers(shuffled, { ...DEFAULT_ROSTER_FILTER, sortBy: 'name' }).map(
			(p) => p.ref?.name
		),
		ascending
	);

	const descending = filterAndSortPlayers(players, {
		...DEFAULT_ROSTER_FILTER,
		sortBy: 'name',
		sortDescending: true
	}).map((p) => p.ref?.name);
	assert.deepEqual(descending, ['Zara', 'Morgan', 'Alice']);
});

test('roster search spans name, uuid, alias and server, case-insensitively', () => {
	const players = [
		summary('a', 'Alice', {
			ref: create(PlayerRefSchema, { identityKey: 'a', name: 'Alice', uuid: 'uuid-alice' }),
			aliases: [
				create(PlayerAliasSchema, {
					name: 'Ally',
					sourceServerId: 'survival',
					source: PlayerDataSource.PRESENCE
				})
			]
		}),
		summary('b', 'Bob'),
		summary('c', 'Carol', { serverName: 'Creative', serverId: 'creative' })
	];
	const byName = filterAndSortPlayers(players, {
		...DEFAULT_ROSTER_FILTER,
		query: '  ALI '
	});
	assert.deepEqual(byName.map((p) => p.ref?.name), ['Alice']);
	const byUuid = filterAndSortPlayers(players, { ...DEFAULT_ROSTER_FILTER, query: 'uuid-alice' });
	assert.deepEqual(byUuid.map((p) => p.ref?.name), ['Alice']);
	const byServer = filterAndSortPlayers(players, { ...DEFAULT_ROSTER_FILTER, query: 'creative' });
	assert.deepEqual(byServer.map((p) => p.ref?.name), ['Carol']);
});

test('roster state and access filters combine with server scope', () => {
	const players = [
		summary('a', 'Alice', { presence: PlayerPresence.ONLINE, operator: true }),
		summary('b', 'Bob', { presence: PlayerPresence.OFFLINE, banned: true }),
		summary('c', 'Carol', {
			presence: PlayerPresence.ONLINE,
			whitelisted: true,
			serverId: 'creative',
			serverName: 'Creative'
		})
	];
	assert.deepEqual(
		filterAndSortPlayers(players, {
			...DEFAULT_ROSTER_FILTER,
			stateFilter: 'online'
		}).map((p) => p.ref?.name),
		['Alice', 'Carol']
	);
	assert.deepEqual(
		filterAndSortPlayers(players, {
			...DEFAULT_ROSTER_FILTER,
			stateFilter: 'online',
			accessFilter: 'operator',
			serverId: 'survival'
		}).map((p) => p.ref?.name),
		['Alice']
	);
	assert.deepEqual(
		filterAndSortPlayers(players, { ...DEFAULT_ROSTER_FILTER, accessFilter: 'banned' }).map(
			(p) => p.ref?.name
		),
		['Bob']
	);
});

test('roster sort treats a missing last_seen as unavailable, never an epoch', () => {
	const players = [
		summary('none', 'Never', { lastSeen: undefined }),
		summary('old', 'Old', { lastSeen: timestampFromDate(new Date('2020-01-01T00:00:00Z')) }),
		summary('new', 'New', { lastSeen: timestampFromDate(new Date('2025-01-01T00:00:00Z')) })
	];
	// Ascending: unavailable first, then oldest, then newest.
	assert.deepEqual(
		filterAndSortPlayers(players, { ...DEFAULT_ROSTER_FILTER, sortBy: 'lastSeen' }).map(
			(p) => p.ref?.name
		),
		['Never', 'Old', 'New']
	);
});

test('server options are distinct and labelled, falling back to the id', () => {
	const options = serverOptionsFromRoster([
		summary('a', 'A', { serverId: 's1', serverName: 'Zeta' }),
		summary('b', 'B', { serverId: 's1', serverName: 'Zeta' }),
		summary('c', 'C', { serverId: 's2', serverName: '' })
	]);
	assert.deepEqual(options, [
		{ id: 's2', name: 's2' },
		{ id: 's1', name: 'Zeta' }
	]);
});

test('capability unsupported and decision-pending states render actionable text', () => {
	assert.match(
		capabilityDetail(PlayerCapability.BRIDGE_VITALS, PlayerSupportState.UNSUPPORTED, ''),
		/verified Paper\/Fabric\/NeoForge artifact and licence review — not shipped/
	);
	assert.match(
		capabilityDetail(PlayerCapability.BEDROCK_XUID, PlayerSupportState.DECISION_PENDING, ''),
		/Bedrock transport/
	);
	// Backend-provided detail is authoritative text and wins.
	assert.equal(
		capabilityDetail(PlayerCapability.SKIN_PROXY, PlayerSupportState.DEGRADED, 'Cached skin only'),
		'Cached skin only'
	);
	assert.equal(capabilityLabel(PlayerCapability.ON_DISK_READ), 'On-disk read');
	assert.equal(supportStateLabel(PlayerSupportState.DECISION_PENDING), 'Decision pending');
});

test('provenance renders source, confidence, timestamp and conflict note honestly', () => {
	const observation = create(ObservationSchema, {
		source: PlayerDataSource.ON_DISK_NBT,
		observedAt: timestampFromDate(new Date('2024-06-01T12:00:00Z')),
		confidence: PlayerConfidence.CONFLICT,
		applicability: 'Java edition only',
		conflictNote: 'Two NBT files disagreed'
	});
	const view = provenanceView(observation);
	assert.ok(view);
	assert.equal(view.source, 'On-disk NBT');
	assert.equal(view.confidence, 'Conflict');
	assert.equal(view.authoritative, false);
	assert.equal(view.applicability, 'Java edition only');
	assert.equal(view.conflictNote, 'Two NBT files disagreed');
	assert.notEqual(view.observedAt, 'unavailable');

	const missing = provenanceView(undefined);
	assert.equal(missing, null);
	const stale = provenanceView(
		create(ObservationSchema, { confidence: PlayerConfidence.STALE, source: 0 })
	);
	assert.equal(stale?.observedAt, 'unavailable');
	assert.equal(stale?.authoritative, false);
});

test('field values keep units and render missing values as an em dash', () => {
	const withUnit = create(PlayerFieldSchema, { name: 'health', value: '20', unit: 'HP' });
	assert.equal(fieldValue(withUnit), '20 HP');
	assert.equal(fieldValue(create(PlayerFieldSchema, { name: 'unknown' })), '—');
});

test('name history note explains the 2022 Mojang removal when backend omits it', () => {
	assert.match(nameHistoryNote(undefined), /removed public name history \(2022-09-13\)/);
	assert.equal(nameHistoryNote(' Custom note '), 'Custom note');
});

test('action confirm metadata, side effects and bulk kinds are explicit', () => {
	assert.equal(bulkKindFor('kick'), 'kick');
	assert.equal(bulkKindFor('whitelist_add'), 'allowlist_add');
	assert.equal(bulkKindFor('op'), null, 'operator grant has no bulk kind');
	assert.match(
		undoUnavailableReason('kick'),
		/kick cannot be undone — player already left/
	);
});

test('undo is enabled only with both a supported flag and a token', () => {
	assert.equal(canUndoResult({ undoSupported: true, undoToken: 'tok-1' }), true);
	assert.equal(canUndoResult({ undoSupported: true, undoToken: '' }), false);
	assert.equal(canUndoResult({ undoSupported: false, undoToken: 'tok-1' }), false);
});

test('bulk preview is capped and request ids are present', () => {
	const preview = previewTargets(
		Array.from({ length: 8 }, (_, i) => ({ name: `P${i}` })),
		3
	);
	assert.equal(preview, 'P0, P1, P2 +5 more');
	assert.match(newRequestId(), /\S+/);
});
