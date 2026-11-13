// SPDX-License-Identifier: MPL-2.0
import type { StudioMod } from '$lib/components/fragments/StudioModTable.svelte';
export type SampleMod = StudioMod & {
	version: string;
	description: string;
	category: 'Exploration' | 'Performance' | 'Utility';
	requires: string[];
	newVersion?: string;
};
function mod(
	slug: string,
	name: string,
	side: StudioMod['side'],
	category: SampleMod['category'],
	description: string,
	requires: string[] = [],
	newVersion?: string
): SampleMod {
	return {
		slug,
		name,
		side,
		category,
		description,
		requires,
		newVersion,
		version: 'sample.1',
		file_name: `${slug}-sample.1.jar`,
		platform: 'Modrinth',
		pinned: false
	};
}
// Illustrative catalog entries, not live provider/version metadata.
export const sampleCatalog: SampleMod[] = [
	mod('waystones', 'Waystones', 'both', 'Exploration', 'Give your players a way home.', ['balm']),
	mod('sodium', 'Sodium', 'client', 'Performance', 'A leaner rendering engine for your players.'),
	mod(
		'voicechat',
		'Simple Voice Chat',
		'both',
		'Utility',
		'Bring proximity chat to your next adventure.'
	),
	mod('chunky', 'Chunky', 'server', 'Performance', 'Prepare the world before your players arrive.'),
	mod(
		'fabric-api',
		'Fabric API',
		'both',
		'Utility',
		'Shared foundations for your Fabric mods.',
		[],
		'sample.2'
	),
	mod(
		'lithium',
		'Lithium',
		'server',
		'Performance',
		'Make room for more simulation.',
		[],
		'sample.2'
	),
	mod(
		'ferrite-core',
		'FerriteCore',
		'both',
		'Performance',
		'Keep the memory footprint lighter.',
		[],
		'sample.2'
	),
	mod('jade', 'Jade', 'both', 'Utility', 'Know what you are looking at.', ['fabric-api']),
	mod('modmenu', 'Mod Menu', 'client', 'Utility', 'A clearer view of the mods in your client.'),
	mod('appleskin', 'AppleSkin', 'client', 'Utility', 'A little more information at mealtimes.'),
	mod('balm', 'Balm', 'both', 'Utility', 'The shared library used by Waystones.'),
	mod('terralith', 'Terralith', 'both', 'Exploration', 'More landscapes to discover together.')
];
export function initialMods(): SampleMod[] {
	return ['fabric-api', 'lithium', 'ferrite-core', 'jade', 'modmenu', 'appleskin'].map((slug) => ({
		...sampleCatalog.find((item) => item.slug === slug)!,
		pinned: slug === 'lithium'
	}));
}
export function addCatalogItem(mods: SampleMod[], slug: string): SampleMod[] {
	const result = [...mods];
	const add = (id: string) => {
		if (result.some((item) => item.slug === id)) return;
		const item = sampleCatalog.find((candidate) => candidate.slug === id);
		if (!item) return;
		result.push({ ...item, requires: [...item.requires] });
		item.requires.forEach(add);
	};
	add(slug);
	return result;
}
export function removeMods(mods: SampleMod[], slugs: string[]): SampleMod[] {
	const removed = new Set(slugs);
	// Removing a library also removes dependants, never leaving a broken sample.
	let changed = true;
	while (changed) {
		changed = false;
		for (const item of mods)
			if (!removed.has(item.slug) && item.requires.some((id) => removed.has(id))) {
				removed.add(item.slug);
				changed = true;
			}
	}
	return mods.filter((item) => !removed.has(item.slug));
}
export function exportSelection<T extends StudioMod>(mods: T[], includeClient: boolean): T[] {
	return mods.filter((item) => includeClient || item.side !== 'client');
}
export function availableUpdates(mods: SampleMod[]): SampleMod[] {
	return mods.filter((item) => !item.pinned && item.newVersion && item.newVersion !== item.version);
}
export function updateMods(mods: SampleMod[]): SampleMod[] {
	const updates = new Set(availableUpdates(mods).map((item) => item.slug));
	return mods.map((item) =>
		updates.has(item.slug)
			? { ...item, version: item.newVersion!, file_name: `${item.slug}-${item.newVersion}.jar` }
			: item
	);
}
export const sampleOverrides = [
	{
		path: 'config/voicechat/voicechat-server.properties',
		side: 'server',
		content: 'port=24454\nmax_voice_distance=48\n'
	},
	{
		path: 'config/sodium-options.json',
		side: 'client',
		content: '{"quality":{"weather_quality":"FAST"}}\n'
	},
	{ path: 'options.txt', side: 'client', content: 'autoJump:false\nguiScale:3\n' }
];
export const exportFormats = {
	mrpack: { label: 'Modrinth (.mrpack)', extension: 'mrpack', entry: 'modrinth.index.json' },
	curseforge: { label: 'CurseForge (.zip)', extension: 'zip', entry: 'manifest.json' },
	instance: { label: 'Prism / MultiMC instance (.zip)', extension: 'zip', entry: 'mmc-pack.json' },
	packwiz: { label: 'Packwiz (.zip)', extension: 'zip', entry: 'pack.toml' }
};
export type ExportFormat = keyof typeof exportFormats;
export function buildExportRecipe(
	mods: SampleMod[],
	options: { format: ExportFormat; includeClient: boolean; includeOverrides: boolean }
) {
	const selected = exportSelection(mods, options.includeClient);
	if (!selected.length) throw new Error('Add at least one mod for this export.');
	for (const item of selected) {
		const missing = item.requires.filter(
			(id) => !selected.some((candidate) => candidate.slug === id)
		);
		if (missing.length)
			throw new Error(
				`${item.name} needs ${missing.join(', ')} in this export. Adjust their sides or include client mods.`
			);
	}
	const overrides = options.includeOverrides
		? sampleOverrides.filter((file) => options.includeClient || file.side !== 'client')
		: [];
	return {
		kind: 'carbon-panel-demo-recipe',
		playable: false,
		note: 'Illustrative manifest only. No mod binaries, provider IDs or real download URLs are included.',
		name: 'A little more adventure',
		minecraft: '1.21.1',
		loader: 'Fabric',
		...options,
		filename: `a-little-more-adventure.${exportFormats[options.format].extension}`,
		mods: selected.map(({ slug, name, version, side, pinned, file_name }) => ({
			slug,
			name,
			version,
			side,
			pinned: !!pinned,
			file_name
		})),
		excluded: mods.filter((item) => !selected.includes(item)).map((item) => item.name),
		overrides: overrides.map((file) => ({ ...file })),
		files: [
			exportFormats[options.format].entry,
			...(options.format === 'packwiz'
				? ['index.toml', ...selected.map((item) => `mods/${item.slug}.pw.toml`)]
				: []),
			...overrides.map((file) => `overrides/${file.path}`)
		]
	};
}
