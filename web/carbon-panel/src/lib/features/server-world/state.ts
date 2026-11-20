// SPDX-License-Identifier: MPL-2.0
/**
 * Pure derivation of the server-detail WORLD tab. The tab never infers a world
 * from a tag colour or a folder name: a library binding is the only authority
 * for "managed", and anything else is reported as unmanaged, unobserved or
 * unknown. No world bytes or configuration are written here.
 */

import type { ConfigCategory } from '../../proto/carbonpanel/v1/config_pb';
import { WorldRef_Kind, type WorldViewTarget } from '../../proto/carbonpanel/v1/worldview_pb';
import type { WorldPresentation } from '../world-library/presentation';

/** The binding projection the tab needs; both raw bindings and library views satisfy it. */
export interface ServerWorldBindingView {
	id: string;
	serverId: string;
	levelName: string;
	revisionId: string;
	nodeId: string;
	generation: bigint;
	consistency: string;
}

/**
 * A read-only compatibility report is valid only for the exact tuple it was
 * computed against. A late response for revision A must never unlock revision
 * B, and a level-server change must invalidate every previously approved
 * report.
 */
export interface CompatibilityTuple {
	serverId: string;
	revisionId: string;
	targetMcVersion: string;
	levelName: string;
}

export function sameCompatibilityTuple(
	a: CompatibilityTuple | null | undefined,
	b: CompatibilityTuple | null | undefined
): boolean {
	if (!a || !b) return false;
	return (
		a.serverId === b.serverId &&
		a.revisionId === b.revisionId &&
		a.targetMcVersion === b.targetMcVersion &&
		a.levelName === b.levelName
	);
}

/** Accept a delayed report only when it still belongs to the live selection. */
export function shouldAcceptCompatibility(input: {
	captured: CompatibilityTuple;
	current: CompatibilityTuple;
	navigationChanged: boolean;
	requestIsCurrent: boolean;
}): boolean {
	return (
		!input.navigationChanged &&
		input.requestIsCurrent &&
		sameCompatibilityTuple(input.captured, input.current)
	);
}

export type ServerWorldManagement =
	| 'loading'
	| 'bound'
	| 'unmanaged'
	| 'unbound'
	| 'none'
	| 'denied'
	| 'error';

export const MANAGEMENT_PRESENTATION: Record<ServerWorldManagement, WorldPresentation> = {
	loading: {
		label: 'CHECKING WORLD',
		tone: 'gray',
		hint: 'Reading binding, world-view and configuration observations.'
	},
	bound: {
		label: 'LIBRARY BOUND',
		tone: 'green',
		hint: 'An immutable library revision is bound to this level. The server writes to its own private clone.'
	},
	unmanaged: {
		label: 'UNMANAGED WORLD',
		tone: 'yellow',
		hint: 'A world is observable on this server, but no library binding is active. These bytes are not managed by the World Library.'
	},
	unbound: {
		label: 'UNBOUND LEVEL',
		tone: 'gray',
		hint: 'A level name is configured, but no active world was observed and no binding is present.'
	},
	none: {
		label: 'NO WORLD DETECTED',
		tone: 'warm-gray',
		hint: 'No binding, world-view target or configured level was reported. Nothing is assumed.'
	},
	denied: {
		label: 'ACCESS DENIED',
		tone: 'red',
		hint: 'Your account lacks worlds/read on this server. No world information is shown.'
	},
	error: {
		label: 'OBSERVATION FAILED',
		tone: 'red',
		hint: 'The world observations could not be read. The previous state is not replaced by a guess.'
	}
};

/** Keys worth showing on the world tab, in display order. Missing keys are omitted. */
const RELEVANT_CONFIG: ReadonlyArray<{ key: string; label: string }> = [
	{ key: 'level', label: 'Level folder' },
	{ key: 'levelType', label: 'World type' },
	{ key: 'seed', label: 'Seed' },
	{ key: 'gamemode', label: 'Game mode' },
	{ key: 'difficulty', label: 'Difficulty' }
];

export interface WorldConfigRow {
	key: string;
	label: string;
	value: string;
}

/** Read one config property without inventing a default for an absent key. */
export function configProperty(
	categories: ConfigCategory[] | undefined,
	key: string
): string | undefined {
	if (!categories) return undefined;
	for (const category of categories) {
		for (const property of category.properties ?? []) {
			if (property.key === key) {
				const value = property.value?.trim();
				return value ? value : undefined;
			}
		}
	}
	return undefined;
}

/** Only fields actually reported by the server are returned. */
export function relevantWorldConfig(categories: ConfigCategory[] | undefined): WorldConfigRow[] {
	return RELEVANT_CONFIG.flatMap(({ key, label }) => {
		const value = configProperty(categories, key);
		return value ? [{ key, label, value }] : [];
	});
}

/** Server-local world-view targets are observations, never the binding authority. */
export function serverLocalTargets(
	targets: WorldViewTarget[] | undefined,
	serverId: string
): WorldViewTarget[] {
	return (targets ?? []).filter(
		(target) => target.serverId === serverId && target.worldRef?.kind === WorldRef_Kind.SERVER_LOCAL
	);
}

export function bindingTargets(
	targets: WorldViewTarget[] | undefined,
	serverId: string
): WorldViewTarget[] {
	return (targets ?? []).filter(
		(target) =>
			target.serverId === serverId && target.worldRef?.kind === WorldRef_Kind.LIBRARY_BINDING
	);
}

export interface ServerWorldDescriptionInput {
	bindings: ServerWorldBindingView[] | undefined;
	targets: WorldViewTarget[] | undefined;
	configCategories: ConfigCategory[] | undefined;
	serverId: string;
	loading?: boolean;
	denied?: boolean;
	error?: boolean;
}

export interface ServerWorldDescription {
	management: ServerWorldManagement;
	presentation: WorldPresentation;
	/** The active level name, from the binding, an observation or config — in that order. */
	levelName: string | undefined;
	binding: ServerWorldBindingView | undefined;
	target: WorldViewTarget | undefined;
}

/**
 * Resolve the honest management state. A binding always wins. Without one, an
 * observed server-local world is *unmanaged*, never "bound"; a configured level
 * without an observation is *unbound*; neither yields *none*. Denied, loading
 * and error are terminal render states that replace the data-derived ones.
 */
export function describeServerWorld(input: ServerWorldDescriptionInput): ServerWorldDescription {
	const { serverId, bindings, targets, configCategories } = input;
	if (input.denied || input.loading || input.error) {
		const management: ServerWorldManagement = input.denied
			? 'denied'
			: input.loading
				? 'loading'
				: 'error';
		return {
			management,
			presentation: MANAGEMENT_PRESENTATION[management],
			levelName: undefined,
			binding: undefined,
			target: undefined
		};
	}

	const binding = (bindings ?? []).find((entry) => entry.serverId === serverId);
	const local = serverLocalTargets(targets, serverId);
	const observed = binding
		? bindingTargets(targets, serverId).find((target) => target.worldRef?.bindingId === binding.id)
		: local[0];
	const configLevel =
		configProperty(configCategories, 'level') ?? configProperty(configCategories, 'level-name');
	const levelName = binding?.levelName || observed?.worldRef?.levelName || configLevel || undefined;

	let management: ServerWorldManagement;
	if (binding) management = 'bound';
	else if (local.length > 0) management = 'unmanaged';
	else if (configLevel) management = 'unbound';
	else management = 'none';

	return {
		management,
		presentation: MANAGEMENT_PRESENTATION[management],
		levelName,
		binding,
		target: observed
	};
}
