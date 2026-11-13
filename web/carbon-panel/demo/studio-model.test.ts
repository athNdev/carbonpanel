// SPDX-License-Identifier: MPL-2.0
import { describe, expect, test } from 'bun:test';
import {
	addCatalogItem,
	exportSelection,
	initialMods,
	sampleCatalog,
	removeMods,
	availableUpdates,
	updateMods,
	buildExportRecipe,
	exportFormats,
	type ExportFormat
} from './studio-model';

describe('homepage Studio sample workflow', () => {
	test('adding a mod includes its dependency once and preserves existing edits', () => {
		const first = addCatalogItem([], 'waystones');
		expect(first.map((mod) => mod.slug)).toEqual(['waystones', 'balm']);
		const edited = first.map((mod) => ({ ...mod, pinned: true }));
		expect(addCatalogItem(edited, 'waystones')).toEqual(edited);
		expect(first.every((mod) => !mod.pinned)).toBe(true);
	});
	test('a server export excludes client mods without removing them from the pack', () => {
		const pack = addCatalogItem(addCatalogItem([], 'waystones'), 'sodium');
		expect(exportSelection(pack, false).map((mod) => mod.slug)).toEqual(['waystones', 'balm']);
		expect(exportSelection(pack, true)).toEqual(pack);
		expect(pack).toHaveLength(3);
	});
	test('seeded curation, dependency removal, updates and export form one workflow', () => {
		const initial = initialMods();
		expect(initial).toHaveLength(6);
		expect(sampleCatalog).toHaveLength(12);
		const pack = addCatalogItem(initial, 'waystones');
		expect(pack).toHaveLength(8);
		expect(availableUpdates(pack).map((item) => item.slug)).toEqual(['fabric-api', 'ferrite-core']);
		const updated = updateMods(pack);
		expect(updated.find((item) => item.slug === 'lithium')?.version).toBe('sample.1');
		expect(updated.find((item) => item.slug === 'fabric-api')?.version).toBe('sample.2');
		const recipe = buildExportRecipe(updated, {
			format: 'packwiz',
			includeClient: false,
			includeOverrides: true
		});
		expect(recipe.playable).toBe(false);
		expect(recipe.mods.some((item) => item.side === 'client')).toBe(false);
		expect(recipe.excluded).toEqual(['Mod Menu', 'AppleSkin']);
		expect(recipe.overrides.map((file) => file.side)).toEqual(['server']);
		expect(recipe.files).toContain('mods/waystones.pw.toml');
		expect(recipe.mods.find((item) => item.slug === 'fabric-api')?.file_name).toContain('sample.2');
		const removed = removeMods(updated, ['balm']);
		expect(removed.some((item) => ['waystones', 'balm'].includes(item.slug))).toBe(false);
		expect(removed).toHaveLength(6);
		expect(initial.find((item) => item.slug === 'fabric-api')?.version).toBe('sample.1');
	});
	test('format and override choices are reflected in detached download snapshots', () => {
		const pack = initialMods();
		for (const format of Object.keys(exportFormats) as ExportFormat[]) {
			const recipe = buildExportRecipe(pack, {
				format,
				includeClient: true,
				includeOverrides: false
			});
			expect(recipe.files[0]).toBe(exportFormats[format].entry);
			expect(recipe.overrides).toHaveLength(0);
			expect(recipe.filename).toEndWith(`.${exportFormats[format].extension}`);
		}
		const recipe = buildExportRecipe(pack, {
			format: 'mrpack',
			includeClient: true,
			includeOverrides: true
		});
		pack[0].version = 'edited-after-build';
		expect(recipe.mods[0].version).toBe('sample.1');
		expect(recipe.overrides).toHaveLength(3);
	});
	test('empty or dependency-incomplete exports are blocked', () => {
		expect(() =>
			buildExportRecipe([], { format: 'mrpack', includeClient: true, includeOverrides: true })
		).toThrow();
		const pack = addCatalogItem([], 'waystones').map((item) =>
			item.slug === 'balm' ? { ...item, side: 'client' as const } : item
		);
		expect(() =>
			buildExportRecipe(pack, { format: 'mrpack', includeClient: false, includeOverrides: true })
		).toThrow('Waystones needs balm');
	});
});
