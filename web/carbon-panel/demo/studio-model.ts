// SPDX-License-Identifier: MPL-2.0
import type { StudioMod } from '$lib/components/fragments/StudioModTable.svelte';

type CatalogItem = StudioMod & { description: string; dependency?: StudioMod };

export const sampleCatalog: CatalogItem[] = [
	{
		slug: 'waystones',
		name: 'Waystones',
		file_name: 'waystones-sample.jar',
		side: 'both',
		platform: 'Modrinth',
		description: 'A new way to find your way home.',
		dependency: {
			slug: 'balm',
			name: 'Balm',
			file_name: 'balm-sample.jar',
			side: 'both',
			platform: 'Modrinth'
		}
	},
	{
		slug: 'sodium',
		name: 'Sodium',
		file_name: 'sodium-sample.jar',
		side: 'client',
		platform: 'Modrinth',
		description: 'A different rendering engine for your client.'
	}
];

export function addCatalogItem(mods: StudioMod[], slug: string): StudioMod[] {
	const item = sampleCatalog.find((candidate) => candidate.slug === slug);
	if (!item) return mods;
	const additions = item.dependency ? [item, item.dependency] : [item];
	return [
		...mods,
		...additions
			.filter((addition) => !mods.some((mod) => mod.slug === addition.slug))
			.map((addition) => ({ ...addition }))
	];
}

export function exportSelection(mods: StudioMod[], includeClient: boolean): StudioMod[] {
	return mods.filter((mod) => includeClient || mod.side !== 'client');
}
