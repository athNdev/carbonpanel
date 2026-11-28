// SPDX-License-Identifier: MPL-2.0

// Source contracts for the shared mods components. The components are small
// Carbon compositions; these assertions pin the operator-visible decisions the
// unification depends on (same result-row idiom, same badge vocabulary, same
// search-bar behavior across all mods surfaces).

import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

const read = (name: string) => fs.readFileSync(path.resolve(import.meta.dirname, name), 'utf-8');

const searchBar = read('ModsCatalogSearchBar.svelte');
const resultCard = read('ModsCatalogResultCard.svelte');
const outcomeList = read('ModsInstallOutcomeList.svelte');

test('the search bar submits on Enter without submitting enclosing forms', () => {
	assert.ok(searchBar.includes("event.key === 'Enter'"), 'Enter must trigger the search');
	assert.ok(searchBar.includes('event.preventDefault()'), 'implicit form submission is cancelled');
	assert.ok(
		searchBar.includes('aria-pressed={platform === option.id}'),
		'platform tabs are toggles'
	);
	assert.ok(
		!searchBar.includes('Loader2'),
		'loading feedback comes from CarbonButton, not an ad-hoc lucide spinner'
	);
});

test('the result card labels the Hangar source instead of mixing it in', () => {
	assert.ok(resultCard.includes('hangar:'), 'Hangar must have a first-class label');
	assert.ok(resultCard.includes('PLATFORM_LABELS'), 'platforms resolve through one label map');
});

test('the result card speaks the Carbon tag badge vocabulary only', () => {
	assert.ok(resultCard.includes('<CarbonTag'), 'badges are CarbonTags');
	assert.ok(!resultCard.includes("from '$lib/components/ui"), 'no shadcn imports');
	assert.ok(resultCard.includes('rounded-none'), 'the 0px-radius rule is explicit');
});

test('state badges spell the state out and never rely on color alone', () => {
	assert.ok(resultCard.includes("'Installed'") && resultCard.includes("'Added just now'"));
	assert.ok(resultCard.includes("'Selected'"), 'queued mods keep the Selected tag');
});

test('the outcome list renders per-item rows with tone, label and detail', () => {
	assert.ok(outcomeList.includes('aria-label={ariaLabel}'), 'rows are announced');
	assert.ok(outcomeList.includes('type={row.tone}'), 'tones come from the row');
	assert.ok(outcomeList.includes('{row.label}') && outcomeList.includes('{row.detail}'));
});
