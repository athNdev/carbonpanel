// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

// The datapack search input renders inside the create-server wizard's outer
// <form>, which has a rendered type="submit" button on the last step. Pressing
// Enter in the input must run a search only: the keydown handler has to cancel
// the implicit form submission or the wizard submits and creates the server.
const source = readFileSync(new URL('./datapack-search.svelte', import.meta.url), 'utf8');

test('Enter in the datapack search never submits an enclosing form', () => {
	assert.match(
		source,
		/function onkeydown\(event: KeyboardEvent\) \{[\s\S]*?event\.preventDefault\(\);[\s\S]*?search\(\);[\s\S]*?\}/,
		'the Enter branch must preventDefault before searching'
	);
	assert.ok(source.includes("if (event.key === 'Enter')"), 'the handler must stay keyed to Enter');
});
