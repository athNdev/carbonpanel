// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';

import {
	canReleaseLock,
	countOperationsByOutcome,
	operationActionLabel,
	outcomeTagType
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

test('counts add up to the number of rows', () => {
	const counts = countOperationsByOutcome([
		{ outcome: 'success' },
		{ outcome: 'success' },
		{ outcome: 'unknown' }
	]);
	assert.deepEqual(counts, { success: 2, unknown: 1 });
	assert.deepEqual(countOperationsByOutcome([]), {});
});
