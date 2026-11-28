// SPDX-License-Identifier: MPL-2.0

import { describe, test } from 'node:test';
import assert from 'node:assert/strict';
import {
	KNOWN_MC_VERSIONS,
	buildFilterVocabulary,
	matchFilterSuggestions
} from './filter-vocabulary';

describe('buildFilterVocabulary', () => {
	test('suggests the server MC version first (E11: fed by server data)', () => {
		const vocabulary = buildFilterVocabulary({ mcVersion: '1.20.6' });
		const versionLabels = vocabulary
			.map((item) => item.label)
			.filter((label) => label.startsWith('version:'));
		assert.equal(versionLabels[0], 'version:1.20.6', 'the server version is reachable first');
		assert.ok(versionLabels.includes('version:1.21.4'), 'known versions stay reachable');
		// The server version must not be duplicated.
		assert.equal(versionLabels.filter((label) => label === 'version:1.20.6').length, 1);
	});

	test('explicit version lists replace the hardcoded fallback ordering', () => {
		const vocabulary = buildFilterVocabulary({ mcVersions: ['1.21.5', '1.21.4'] });
		const versionLabels = vocabulary
			.map((item) => item.label)
			.filter((label) => label.startsWith('version:'));
		assert.equal(versionLabels[0], 'version:1.21.5');
		assert.equal(versionLabels[1], 'version:1.21.4');
		// The provided 1.21.4 dedupes the KNOWN list; the next known follows.
		assert.equal(versionLabels[2], `version:${KNOWN_MC_VERSIONS[1]}`);
	});

	test('plugin loaders have no side suggestions (Hangar has no sides)', () => {
		const vocabulary = buildFilterVocabulary({ pluginLoader: true });
		assert.ok(
			vocabulary.every((item) => !item.label.startsWith('side:')),
			'side: filters are meaningless for Hangar plugins'
		);
		const withSides = buildFilterVocabulary({ pluginLoader: false });
		assert.ok(withSides.some((item) => item.label === 'side:server'));
	});

	test('loader, version and category suggestions are always present', () => {
		const vocabulary = buildFilterVocabulary({ pluginLoader: true });
		assert.ok(vocabulary.some((item) => item.label === 'loader:fabric'));
		assert.ok(vocabulary.some((item) => item.label === 'category:optimization'));
	});
});

describe('matchFilterSuggestions', () => {
	const vocabulary = buildFilterVocabulary({ mcVersion: '1.21.1' });

	test('empty input returns the first few suggestions', () => {
		const matches = matchFilterSuggestions(vocabulary, '');
		assert.equal(matches.length, 6);
	});

	test('matching covers label and description, case-insensitively', () => {
		const byLabel = matchFilterSuggestions(vocabulary, 'SIDE:SERVER');
		assert.equal(byLabel[0].label, 'side:server');
		const byDesc = matchFilterSuggestions(vocabulary, 'performance');
		assert.equal(byDesc[0].label, 'category:optimization');
	});
});
