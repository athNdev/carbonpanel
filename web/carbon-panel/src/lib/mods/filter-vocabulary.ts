// SPDX-License-Identifier: MPL-2.0

// Pure `key:value` filter vocabulary for the mods browser filter bar.
// Extracted from mod-browser-dialog.svelte so the suggestions are testable and
// fed by data instead of a hardcoded list that can drift from the node.
//
// Edge case E11: the version suggestions are caller-supplied (the server's own
// MC version first) rather than a frozen list compiled into the dialog. Edge
// case §5.2: plugin loaders (Hangar) have no client/server sides, so `side:`
// suggestions are omitted for them.

export interface FilterSuggestion {
	label: string;
	desc: string;
}

/** Minecraft versions offered as `version:` suggestions when the caller has no node-provided list. */
export const KNOWN_MC_VERSIONS = [
	'1.21.4',
	'1.21.1',
	'1.20.4',
	'1.20.1',
	'1.19.4',
	'1.19.2',
	'1.18.2',
	'1.16.5'
] as const;

const SIDE_SUGGESTIONS: FilterSuggestion[] = [
	{ label: 'side:server', desc: 'Server-compatible only (omits client-only)' },
	{ label: 'side:both', desc: 'Runs on client and server' },
	{ label: 'side:client', desc: 'Client-side mods' }
];

const LOADER_SUGGESTIONS: FilterSuggestion[] = [
	{ label: 'loader:fabric', desc: 'Fabric mod loader' },
	{ label: 'loader:forge', desc: 'Forge mod loader' },
	{ label: 'loader:neoforge', desc: 'NeoForge mod loader' },
	{ label: 'loader:quilt', desc: 'Quilt mod loader' }
];

const CATEGORY_SUGGESTIONS: FilterSuggestion[] = [
	{ label: 'category:optimization', desc: 'Performance and FPS' },
	{ label: 'category:technology', desc: 'Tech & machinery' },
	{ label: 'category:magic', desc: 'Spells, rituals, sorcery' },
	{ label: 'category:storage', desc: 'Chests & backpacks' },
	{ label: 'category:adventure', desc: 'Dungeons & exploration' },
	{ label: 'category:utility', desc: 'QoL tools & utilities' },
	{ label: 'category:worldgen', desc: 'Biomes & structures' }
];

export interface FilterVocabularyInput {
	/** The server's MC version — always suggested first so it is reachable. */
	mcVersion?: string;
	/**
	 * Explicit version suggestions (e.g. a node-provided supported list).
	 * Falls back to KNOWN_MC_VERSIONS.
	 */
	mcVersions?: string[];
	/** Bukkit/Paper family: Hangar has no sides, so omit `side:` suggestions. */
	pluginLoader?: boolean;
}

export function buildFilterVocabulary(input: FilterVocabularyInput = {}): FilterSuggestion[] {
	const provided = (input.mcVersions ?? []).filter((version) => version.trim() !== '');
	const versions = [...new Set([input.mcVersion ?? '', ...provided, ...KNOWN_MC_VERSIONS])]
		.filter((version) => version !== '')
		.map((version): FilterSuggestion => ({
			label: `version:${version}`,
			desc: `Minecraft ${version}`
		}));
	const sides = input.pluginLoader ? [] : SIDE_SUGGESTIONS;
	return [...sides, ...LOADER_SUGGESTIONS, ...versions, ...CATEGORY_SUGGESTIONS];
}

/** Filter the vocabulary down to what matches a typed query fragment. */
export function matchFilterSuggestions(
	vocabulary: FilterSuggestion[],
	query: string,
	limit = 6
): FilterSuggestion[] {
	const q = query.trim().toLowerCase();
	if (!q) return vocabulary.slice(0, limit);
	return vocabulary
		.filter((item) => item.label.toLowerCase().includes(q) || item.desc.toLowerCase().includes(q))
		.slice(0, limit);
}
