// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

/**
 * Pure presentation and roster logic for player management.
 *
 * Every value here is derived from authoritative RPC data. Unknown, absent or
 * stale observations stay visibly unknown — nothing is defaulted to a
 * plausible number or a fabricated epoch. Keeping these functions free of
 * Svelte and RPC dependencies makes the honest rendering rules testable in the
 * repository's plain Bun runner.
 */

import type {
	PlayerField,
	PlayerObservation,
	PlayerSummary
} from '$lib/proto/carbonpanel/v1/player_pb';
import {
	PlayerCapability,
	PlayerConfidence,
	PlayerDataSource,
	PlayerPresence,
	PlayerSupportState
} from '$lib/proto/carbonpanel/v1/player_pb';
import { formatTimestamp } from '$lib/utils/format-time';

// ---------------------------------------------------------------------------
// Roster query vocabulary
// ---------------------------------------------------------------------------

export const STATE_FILTERS = ['all', 'online', 'offline', 'recent', 'unobserved'] as const;
export type StateFilter = (typeof STATE_FILTERS)[number];

export const ACCESS_FILTERS = ['all', 'operator', 'whitelisted', 'banned'] as const;
export type AccessFilter = (typeof ACCESS_FILTERS)[number];

export const SORT_OPTIONS = ['name', 'server', 'presence', 'lastSeen', 'access'] as const;
export type SortOption = (typeof SORT_OPTIONS)[number];

export interface RosterFilter {
	serverId: string;
	query: string;
	stateFilter: StateFilter;
	accessFilter: AccessFilter;
	sortBy: SortOption;
	sortDescending: boolean;
}

export const DEFAULT_ROSTER_FILTER: RosterFilter = {
	serverId: '',
	query: '',
	stateFilter: 'all',
	accessFilter: 'all',
	sortBy: 'name',
	sortDescending: false
};

export function isStateFilter(value: string | null | undefined): value is StateFilter {
	return !!value && (STATE_FILTERS as readonly string[]).includes(value);
}

export function isAccessFilter(value: string | null | undefined): value is AccessFilter {
	return !!value && (ACCESS_FILTERS as readonly string[]).includes(value);
}

export function isSortOption(value: string | null | undefined): value is SortOption {
	return !!value && (SORT_OPTIONS as readonly string[]).includes(value);
}

// ---------------------------------------------------------------------------
// Enums -> readable labels (never rely on colour alone)
// ---------------------------------------------------------------------------

export function presenceLabel(presence: PlayerPresence): string {
	switch (presence) {
		case PlayerPresence.ONLINE:
			return 'Online';
		case PlayerPresence.OFFLINE:
			return 'Offline';
		case PlayerPresence.RECENTLY_SEEN:
			return 'Recently seen';
		case PlayerPresence.UNOBSERVED:
			return 'Unobserved';
		default:
			return 'Unknown';
	}
}

/** CarbonTag palette role. Text label is always rendered alongside this. */
export function presenceTagType(
	presence: PlayerPresence
): 'green' | 'gray' | 'yellow' | 'cool-gray' {
	switch (presence) {
		case PlayerPresence.ONLINE:
			return 'green';
		case PlayerPresence.RECENTLY_SEEN:
			return 'yellow';
		case PlayerPresence.UNOBSERVED:
			return 'cool-gray';
		default:
			return 'gray';
	}
}

export function dataSourceLabel(source: PlayerDataSource): string {
	switch (source) {
		case PlayerDataSource.PRESENCE:
			return 'Presence';
		case PlayerDataSource.MANAGEMENT_PROTOCOL:
			return 'Management protocol';
		case PlayerDataSource.RCON:
			return 'RCON';
		case PlayerDataSource.EXEC:
			return 'Exec';
		case PlayerDataSource.ON_DISK_NBT:
			return 'On-disk NBT';
		case PlayerDataSource.ON_DISK_JSON:
			return 'On-disk JSON';
		case PlayerDataSource.IDENTITY_SERVICE:
			return 'Identity service';
		case PlayerDataSource.PANEL_DB:
			return 'Panel database';
		case PlayerDataSource.BRIDGE:
			return 'Bridge';
		default:
			return 'Unspecified source';
	}
}

export function confidenceLabel(confidence: PlayerConfidence): string {
	switch (confidence) {
		case PlayerConfidence.DETECTED:
			return 'Detected';
		case PlayerConfidence.INFERRED:
			return 'Inferred';
		case PlayerConfidence.UNKNOWN:
			return 'Unknown';
		case PlayerConfidence.STALE:
			return 'Stale';
		case PlayerConfidence.CONFLICT:
			return 'Conflict';
		default:
			return 'Unspecified';
	}
}

/** NOTE: a confidence of UNKNOWN/STALE/CONFLICT is never presented as authoritative. */
export function isAuthoritativeConfidence(confidence: PlayerConfidence): boolean {
	return confidence === PlayerConfidence.DETECTED;
}

export function capabilityLabel(capability: PlayerCapability): string {
	switch (capability) {
		case PlayerCapability.MANAGEMENT_PROTOCOL:
			return 'Management protocol';
		case PlayerCapability.RCON_EXEC:
			return 'RCON / exec';
		case PlayerCapability.ON_DISK_READ:
			return 'On-disk read';
		case PlayerCapability.SKIN_PROXY:
			return 'Skin proxy';
		case PlayerCapability.BRIDGE_VITALS:
			return 'Bridge: vitals';
		case PlayerCapability.BRIDGE_INVENTORY:
			return 'Bridge: inventory';
		case PlayerCapability.BRIDGE_GROUPS:
			return 'Bridge: groups';
		case PlayerCapability.BRIDGE_MUTE:
			return 'Bridge: mute';
		case PlayerCapability.BEDROCK_XUID:
			return 'Bedrock XUID';
		default:
			return 'Unspecified capability';
	}
}

export function supportStateLabel(state: PlayerSupportState): string {
	switch (state) {
		case PlayerSupportState.SUPPORTED:
			return 'Supported';
		case PlayerSupportState.DEGRADED:
			return 'Degraded';
		case PlayerSupportState.UNSUPPORTED:
			return 'Unsupported';
		case PlayerSupportState.DECISION_PENDING:
			return 'Decision pending';
		default:
			return 'Unknown';
	}
}

export function supportStateTagType(
	state: PlayerSupportState
): 'green' | 'yellow' | 'red' | 'gray' {
	switch (state) {
		case PlayerSupportState.SUPPORTED:
			return 'green';
		case PlayerSupportState.DEGRADED:
			return 'yellow';
		case PlayerSupportState.UNSUPPORTED:
			return 'red';
		default:
			return 'gray';
	}
}

/**
 * Actionable explanation for a capability. Unsupported and decision-pending
 * capabilities are rendered as honest text with a next step, never as a control
 * that would imply the feature is shipped.
 */
export function capabilityDetail(
	capability: PlayerCapability,
	state: PlayerSupportState,
	detail: string
): string {
	if (detail.trim()) return detail.trim();
	if (state === PlayerSupportState.UNSUPPORTED || state === PlayerSupportState.DECISION_PENDING) {
		switch (capability) {
			case PlayerCapability.BRIDGE_VITALS:
			case PlayerCapability.BRIDGE_INVENTORY:
			case PlayerCapability.BRIDGE_GROUPS:
			case PlayerCapability.BRIDGE_MUTE:
			case PlayerCapability.BEDROCK_XUID:
			case PlayerCapability.SKIN_PROXY:
				return "This feature isn't available on this server.";
			default:
				return "This feature isn't available on this server.";
		}
	}
	return 'No additional detail reported by the server.';
}

export const NAME_HISTORY_NOTE =
	'Mojang removed public name history (2022-09-13); only observed aliases are shown.';

export function nameHistoryNote(provided: string | undefined | null): string {
	return provided?.trim() ? provided.trim() : NAME_HISTORY_NOTE;
}

// ---------------------------------------------------------------------------
// Provenance
// ---------------------------------------------------------------------------

export interface ProvenanceView {
	source: string;
	observedAt: string;
	confidence: string;
	applicability: string;
	conflictNote: string;
	authoritative: boolean;
}

export function provenanceView(
	observation: PlayerObservation | undefined | null
): ProvenanceView | null {
	if (!observation) return null;
	return {
		source: dataSourceLabel(observation.source),
		observedAt: formatTimestamp(observation.observedAt, 'unavailable'),
		confidence: confidenceLabel(observation.confidence),
		applicability: observation.applicability.trim(),
		conflictNote: observation.conflictNote.trim(),
		authoritative: isAuthoritativeConfidence(observation.confidence)
	};
}

export function fieldValue(field: PlayerField): string {
	const value = field.value?.trim() ? field.value.trim() : '—';
	return field.unit?.trim() ? `${value} ${field.unit.trim()}` : value;
}

// ---------------------------------------------------------------------------
// Roster filtering, searching and sorting
// ---------------------------------------------------------------------------

function identityKey(summary: PlayerSummary): string {
	return summary.ref?.identityKey || summary.serverId + ':' + (summary.ref?.name || '');
}

function matchesQuery(summary: PlayerSummary, query: string): boolean {
	const q = query.trim().toLowerCase();
	if (!q) return true;
	const ref = summary.ref;
	const haystack = [
		ref?.name ?? '',
		ref?.uuid ?? '',
		ref?.xuid ?? '',
		summary.serverName ?? '',
		summary.serverId ?? '',
		...(summary.aliases ?? []).map((alias) => alias.name ?? '')
	]
		.join(' ')
		.toLowerCase();
	return haystack.includes(q);
}

function matchesState(summary: PlayerSummary, filter: StateFilter): boolean {
	switch (filter) {
		case 'all':
			return true;
		case 'online':
			return summary.presence === PlayerPresence.ONLINE;
		case 'offline':
			return summary.presence === PlayerPresence.OFFLINE;
		case 'recent':
			return summary.presence === PlayerPresence.RECENTLY_SEEN;
		case 'unobserved':
			return summary.presence === PlayerPresence.UNOBSERVED;
		default:
			return true;
	}
}

function matchesAccess(summary: PlayerSummary, filter: AccessFilter): boolean {
	switch (filter) {
		case 'all':
			return true;
		case 'operator':
			return summary.operator;
		case 'whitelisted':
			return summary.whitelisted;
		case 'banned':
			return summary.banned;
		default:
			return true;
	}
}

/** Sortable rank for the presence column; unknown sorts after observed states. */
export function presenceRank(presence: PlayerPresence): number {
	switch (presence) {
		case PlayerPresence.ONLINE:
			return 0;
		case PlayerPresence.RECENTLY_SEEN:
			return 1;
		case PlayerPresence.OFFLINE:
			return 2;
		case PlayerPresence.UNOBSERVED:
			return 3;
		default:
			return 4;
	}
}

/** Rank for the access tag column. Bans and operator rights outrank plain entries. */
export function accessRank(summary: PlayerSummary): number {
	if (summary.banned) return 3;
	if (summary.operator) return 2;
	if (summary.whitelisted) return 1;
	return 0;
}

function sortValue(summary: PlayerSummary, sortBy: SortOption): string | number {
	switch (sortBy) {
		case 'name':
			return (summary.ref?.name ?? '').toLowerCase();
		case 'server':
			return (summary.serverName ?? summary.serverId ?? '').toLowerCase();
		case 'presence':
			return presenceRank(summary.presence);
		case 'lastSeen':
			// Handled by the dedicated branch in filterAndSortPlayers: a
			// never-observed identity has no timestamp at all and must not
			// inherit a fake sentinel.
			return 0;
		case 'access':
			return accessRank(summary);
		default:
			return '';
	}
}

function compare(left: string | number, right: string | number): number {
	if (left < right) return -1;
	if (left > right) return 1;
	return 0;
}

/**
 * Deterministically filter and sort a roster. The input array is never mutated
 * and may arrive in any order: the identity key is the final tie-breaker so two
 * calls with differently-ordered but equivalent inputs produce the same order.
 */
export function filterAndSortPlayers(
	players: readonly PlayerSummary[],
	filter: RosterFilter
): PlayerSummary[] {
	const matched = players.filter(
		(summary) =>
			(!filter.serverId || summary.serverId === filter.serverId) &&
			matchesState(summary, filter.stateFilter) &&
			matchesAccess(summary, filter.accessFilter) &&
			matchesQuery(summary, filter.query)
	);

	matched.sort((a, b) => {
		let primary: number;
		if (filter.sortBy === 'lastSeen') {
			// Never-observed identities stay after observed ones regardless of
			// the sort direction; only observed rows flip with the arrow.
			const aObserved = Boolean(a.lastSeen);
			const bObserved = Boolean(b.lastSeen);
			if (aObserved !== bObserved) return aObserved ? -1 : 1;
			primary = compare(Number(a.lastSeen?.seconds ?? 0), Number(b.lastSeen?.seconds ?? 0));
			if (primary !== 0) return filter.sortDescending ? -primary : primary;
		} else {
			primary = compare(sortValue(a, filter.sortBy), sortValue(b, filter.sortBy));
			if (primary !== 0) return filter.sortDescending ? -primary : primary;
		}
		// Stable tie-break independent of input order.
		return compare(identityKey(a), identityKey(b));
	});
	return matched;
}

/** Collect the distinct servers seen in a roster for the server filter. */
export function serverOptionsFromRoster(
	players: readonly PlayerSummary[]
): { id: string; name: string }[] {
	const byId = new Map<string, string>();
	for (const player of players) {
		if (!player.serverId) continue;
		const name = player.serverName?.trim() ? player.serverName : player.serverId;
		if (!byId.has(player.serverId)) byId.set(player.serverId, name);
	}
	return [...byId.entries()]
		.map(([id, name]) => ({ id, name }))
		.sort((a, b) => compare(a.name.toLowerCase(), b.name.toLowerCase()));
}

/**
 * Tab-scoped field storage. Character and Progress retrieve different kinds of
 * read-only data; a shared field array lets the second tab overwrite the first
 * while its section is skipped on return. Storing by tab keeps each section's
 * snapshot stable.
 */
export function withTabFields<T>(
	state: Record<string, T[]>,
	tab: string,
	fields: T[]
): Record<string, T[]> {
	return { ...state, [tab]: fields };
}

export function tabFields<T>(state: Record<string, T[]>, tab: string): T[] {
	return state[tab] ?? [];
}

/**
 * Pure projection of the server-filter options from three independent inputs:
 * the current roster (which may contain server-scoped rows), the server
 * inventory supplied by the route, and the active server scope. Keeping this
 * a pure function means a component derives options without writing state, so
 * scoped rows cannot trigger an update-depth loop.
 */
export function projectServerOptions(
	players: readonly PlayerSummary[],
	configured: readonly { id: string; name: string }[],
	scope: string
): { id: string; name: string }[] {
	const byId: Record<string, string> = {};
	for (const option of serverOptionsFromRoster(players)) byId[option.id] = option.name;
	for (const option of configured) {
		if (option.id) byId[option.id] = option.name.trim() || option.id;
	}
	if (scope) byId[scope] = byId[scope] || scope;
	return Object.entries(byId)
		.map(([id, name]) => ({ id, name }))
		.sort((a, b) => compare(a.name.toLowerCase(), b.name.toLowerCase()));
}

// ---------------------------------------------------------------------------
// Moderation helpers
// ---------------------------------------------------------------------------

export type PlayerActionKind =
	'kick' | 'ban' | 'pardon' | 'op' | 'deop' | 'whitelist_add' | 'whitelist_remove';

/** Bulk RPC kinds are a smaller set than the single-target actions. */
const BULK_KINDS: Partial<Record<PlayerActionKind, string>> = {
	kick: 'kick',
	ban: 'ban',
	pardon: 'pardon',
	deop: 'deop',
	whitelist_add: 'allowlist_add',
	whitelist_remove: 'allowlist_remove'
};

/** Map a UI action kind to the RBAC action that actually authorizes its RPC. */
export function permissionActionFor(action: PlayerActionKind): string {
	switch (action) {
		case 'kick':
		case 'ban':
		case 'pardon':
			return 'moderate';
		case 'op':
		case 'deop':
			return 'moderate:op';
		case 'whitelist_add':
		case 'whitelist_remove':
			return 'access:write';
	}
}

export function bulkKindFor(action: PlayerActionKind): string | null {
	return BULK_KINDS[action] ?? null;
}

export function actionLabel(action: PlayerActionKind): string {
	switch (action) {
		case 'kick':
			return 'Kick';
		case 'ban':
			return 'Ban';
		case 'pardon':
			return 'Pardon';
		case 'op':
			return 'Grant operator';
		case 'deop':
			return 'Remove operator';
		case 'whitelist_add':
			return 'Add to allowlist';
		case 'whitelist_remove':
			return 'Remove from allowlist';
	}
}

export function actionIsDestructive(action: PlayerActionKind): boolean {
	return (
		action === 'kick' || action === 'ban' || action === 'deop' || action === 'whitelist_remove'
	);
}

export function actionSideEffectNote(action: PlayerActionKind): string {
	switch (action) {
		case 'kick':
			return 'The player is disconnected immediately. Their inventory and world data are untouched. This action cannot be undone — the player has already left.';
		case 'ban':
			return 'The player is added to the server ban list and cannot rejoin until pardoned.';
		case 'pardon':
			return 'The ban entry is removed. This does not restore any inventory or progress.';
		case 'op':
			return 'The operator entry is granted at level 4. Prefer the minimum level needed.';
		case 'deop':
			return 'The operator entry is removed. The player keeps any items already held.';
		case 'whitelist_add':
			return 'The player is added to the server allowlist.';
		case 'whitelist_remove':
			return 'The player is removed from the allowlist and may be unable to rejoin.';
	}
}

/** Plain-language reason shown when an undo is unavailable. */
export function undoUnavailableReason(action: PlayerActionKind): string {
	switch (action) {
		case 'kick':
			return 'A kick cannot be reversed — the player has already left.';
		case 'ban':
			return 'Pardon reverses a ban.';
		case 'pardon':
			return 'Ban the player again to reverse a pardon.';
		case 'op':
			return 'Remove operator reverses an operator grant.';
		case 'deop':
			return 'Grant operator again to reverse a removal.';
		case 'whitelist_add':
			return 'Remove from allowlist reverses this change.';
		case 'whitelist_remove':
			return 'Add to allowlist reverses this change.';
	}
}

export function canUndoResult(result: {
	undoSupported: boolean;
	undoToken?: string | undefined;
}): boolean {
	return result.undoSupported && !!result.undoToken?.trim();
}

export const BULK_PREVIEW_LIMIT = 5;

export function previewTargets(
	targets: readonly { name: string }[],
	limit = BULK_PREVIEW_LIMIT
): string {
	const names = targets.slice(0, limit).map((t) => t.name || 'unknown');
	const rest = targets.length - names.length;
	return rest > 0 ? `${names.join(', ')} +${rest} more` : names.join(', ');
}

/** Correlation id for an action; falls back when crypto.randomUUID is missing. */
export function newRequestId(): string {
	const cryptoObject = typeof globalThis !== 'undefined' ? globalThis.crypto : undefined;
	if (cryptoObject && typeof cryptoObject.randomUUID === 'function')
		return cryptoObject.randomUUID();
	return `req-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}
