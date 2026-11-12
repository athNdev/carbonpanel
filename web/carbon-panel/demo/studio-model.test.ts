// SPDX-License-Identifier: MPL-2.0
import { describe, expect, test } from 'bun:test';
import { addCatalogItem, exportSelection } from './studio-model';

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
});
