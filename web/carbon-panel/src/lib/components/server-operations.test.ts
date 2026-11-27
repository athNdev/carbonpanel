// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';

import {
	canReleaseLock,
	correlationTagType,
	countOperationsByOutcome,
	operationActionLabel,
	outcomeTagType,
	requestKeyTitle,
	requiresManualCheck
} from './server-operations';

test('an unknown outcome is amber, never red', () => {
	// Red means "this failed". Unknown means "nobody knows yet", and painting it
	// red pushes an operator into retrying something that may have succeeded.
	assert.equal(outcomeTagType('unknown'), 'yellow');
	assert.notEqual(outcomeTagType('unknown'), outcomeTagType('failed'));
	assert.equal(outcomeTagType('failed'), 'red');
	assert.equal(outcomeTagType('success'), 'green');
	assert.equal(outcomeTagType('running'), 'blue');
	assert.equal(outcomeTagType('something-new'), 'gray');
});

test('every next action has a sentence that says what to do', () => {
	const actions = [
		'none',
		'wait',
		'resolve_operation',
		'retry_with_new_request_id',
		'contact_operator'
	];
	for (const action of actions) {
		const label = operationActionLabel(action);
		assert.ok(label.length > 0, `${action} has no label`);
	}
	assert.equal(operationActionLabel('contact_operator'), 'Contact an operator');
	assert.equal(operationActionLabel('unknown-action'), 'Nothing to do');
});

test('the lock is only offered for release when the server is actually blocked', () => {
	assert.equal(canReleaseLock({ holdsLock: true, nextAction: 'resolve_operation' }), true);
	// A stale row the reconciler already released must not offer the button.
	assert.equal(canReleaseLock({ holdsLock: false, nextAction: 'resolve_operation' }), false);
	// A settled row never offers it, whatever the lock says.
	assert.equal(canReleaseLock({ holdsLock: true, nextAction: 'none' }), false);
	assert.equal(canReleaseLock({ holdsLock: true, nextAction: 'wait' }), false);
});

test('a stale or uncertain unknown row says do not retry blindly', () => {
	assert.equal(requiresManualCheck({ outcome: 'unknown', reasonCode: 'executing_stale' }), true);
	assert.equal(
		requiresManualCheck({ outcome: 'unknown', reasonCode: 'uncertain_external_effect' }),
		true
	);
	// A definite failure is safe to retry with a new request id; a recovered
	// row has already been resolved and needs no warning.
	assert.equal(requiresManualCheck({ outcome: 'failed', reasonCode: 'attempt_failed' }), false);
	assert.equal(
		requiresManualCheck({ outcome: 'unknown', reasonCode: 'recovered_without_confirmation' }),
		false
	);
	assert.equal(requiresManualCheck({ outcome: 'success', reasonCode: 'completed' }), false);
});

test('correlation never renders a daemon match when the state is unknown', () => {
	assert.equal(correlationTagType('matched'), 'green');
	assert.equal(correlationTagType('unmatched'), 'red');
	// Unknown is neutral, not a success and not a failure.
	assert.equal(correlationTagType('unknown'), 'gray');
	assert.equal(correlationTagType(undefined as unknown as string), 'gray');
});

test('the request key tooltip names the value that is on screen', () => {
	assert.equal(
		requestKeyTitle({ requestKey: 'req-9f21-start', requestId: 'req-9f21-start' }),
		'request key: req-9f21-start'
	);
	// A generated key is a journal request key, not a request id.
	assert.equal(
		requestKeyTitle({ requestKey: 'generated:7d3c-migrate', requestId: '' }),
		'request key: generated:7d3c-migrate'
	);
	assert.equal(
		requestKeyTitle({ requestKey: '', requestId: 'req-77ab-migrate' }),
		'request id: req-77ab-migrate'
	);
	assert.equal(requestKeyTitle({ requestKey: '', requestId: '' }), undefined);
});

test('counts add up to the number of rows', () => {
	const counts = countOperationsByOutcome([
		{ outcome: 'success' },
		{ outcome: 'success' },
		{ outcome: 'unknown' }
	]);
	assert.deepEqual(counts, { success: 2, unknown: 1 });
	assert.deepEqual(countOperationsByOutcome([]), {});
});

import fs from 'node:fs';
import path from 'node:path';

test('journal copy is trimmed to the approved sentences', () => {
	const opsPath = path.resolve(import.meta.dirname, 'server-operations.svelte');
	const content = fs.readFileSync(opsPath, 'utf-8').replace(/\s+/g, ' ');
	assert.ok(
		content.includes(
			'Starts, stops, restarts, deletes and migrations are journaled first. Interrupted requests show as unknown with the next step.'
		),
		'operations intro must use the trimmed copy'
	);
	assert.ok(
		content.includes(
			'Releases the lock only. Does not touch containers or world files. Release only after checking what the interrupted run did.'
		),
		'resolve action must use the trimmed copy'
	);
	assert.ok(
		content.includes('No operations yet. Start, stop or restart to populate this journal.'),
		'empty state must use the trimmed copy'
	);
	assert.ok(content.includes('op.requestId'), 'journal rows must show the request id for joining');
	assert.ok(content.includes('op.requestKey'), 'journal rows must show the stored request key');
	assert.ok(content.includes('op.nodeId'), 'journal rows must join to a node without a daemon id');
	assert.ok(
		content.includes('op.correlation'),
		'journal rows must show the derived correlation state'
	);
	assert.ok(content.includes('op.reasonCode'), 'journal rows must show the stable reason code');
	assert.ok(
		content.includes('requiresManualCheck(op)'),
		'stale and uncertain rows must warn against a blind retry'
	);
});

test('the journal has one keyboard-operable horizontal scroll region', () => {
	const markup = fs.readFileSync(
		path.resolve(import.meta.dirname, 'server-operations.svelte'),
		'utf-8'
	);
	const css = fs
		.readFileSync(path.resolve(import.meta.dirname, '../../app.css'), 'utf-8')
		.replace(/\r\n/g, '\n');

	assert.match(markup, /role="region"/);
	assert.match(markup, /aria-label="Operations table"/);
	assert.match(markup, /tabindex="0"/);
	assert.match(markup, /class="[^"]*focus-ring[^"]*"/);
	assert.match(markup, /class="[^"]*overflow-x-auto[^"]*"/);

	// The narrow-width table safety net turns a table into a block scroller.
	// This class keeps the wrapper the only horizontal scroll surface, so a long
	// cell cannot hide the right-hand columns behind a second scrollbar.
	assert.match(markup, /operation-journal-table/);
	assert.match(
		css,
		/\.operation-journal-table\s*\{[^}]*display:\s*table;[^}]*overflow:\s*visible;/
	);
});

test('a long node id truncates while keeping its full value available', () => {
	const markup = fs.readFileSync(
		path.resolve(import.meta.dirname, 'server-operations.svelte'),
		'utf-8'
	);
	assert.match(markup, /max-w-\[18rem\] truncate font-mono/);
	assert.match(markup, /title=\{op\.nodeId \|\| undefined\}/);
});
