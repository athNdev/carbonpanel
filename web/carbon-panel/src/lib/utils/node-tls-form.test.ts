// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {
	buildTlsUpdate,
	hasTlsReplacementErrors,
	validateTlsReplacement,
	type TlsFormValues,
	type TlsSecretFields
} from './node-tls-form';

const redactedEcho: TlsSecretFields = {
	tlsCaCert: '',
	tlsCert: '',
	tlsKey: ''
};

const flags = { tlsEnabled: true, tlsSkipVerify: false };

test('redacted-empty echo is omitted, never sent as a real value', () => {
	const values: TlsFormValues = { ...flags, ...redactedEcho };
	const update = buildTlsUpdate(values, 'unchanged');

	// The fields are absent, not empty strings. Omission is what makes the
	// server keep stored bytes and leave EndpointGeneration untouched.
	assert.equal(Object.hasOwn(update, 'tlsCaCert'), false);
	assert.equal(Object.hasOwn(update, 'tlsCert'), false);
	assert.equal(Object.hasOwn(update, 'tlsKey'), false);
	assert.equal(update.tlsCaCert, undefined);
	assert.equal(update.tlsCert, undefined);
	assert.equal(update.tlsKey, undefined);
	assert.equal(JSON.stringify(update).includes('tlsCaCert'), false);
});

test('a non-secret edit cannot clear credentials or bump generation', () => {
	// Even a non-empty redaction placeholder must not become a real value when
	// the operator did not explicitly ask to replace credentials.
	const values: TlsFormValues = {
		tlsEnabled: false,
		tlsSkipVerify: true,
		tlsCaCert: '<redacted>',
		tlsCert: 'placeholder',
		tlsKey: 'placeholder'
	};
	const update = buildTlsUpdate(values, 'unchanged');

	assert.deepEqual(update, { tlsEnabled: false, tlsSkipVerify: true });
	assert.ok(!('tlsCaCert' in update) && !('tlsCert' in update) && !('tlsKey' in update));
});

test('explicit replacement submits all three fields together', () => {
	const values: TlsFormValues = {
		...flags,
		tlsCaCert: '  -----BEGIN CERTIFICATE-----\nca\n',
		tlsCert: '-----BEGIN CERTIFICATE-----\nclient\n ',
		tlsKey: '\n-----BEGIN PRIVATE KEY-----\nkey\n'
	};
	const update = buildTlsUpdate(values, 'replace');

	assert.deepEqual(update, {
		tlsEnabled: true,
		tlsSkipVerify: false,
		tlsCaCert: '-----BEGIN CERTIFICATE-----\nca',
		tlsCert: '-----BEGIN CERTIFICATE-----\nclient',
		tlsKey: '-----BEGIN PRIVATE KEY-----\nkey'
	});
});

test('replacement validation requires every field; unchanged mode needs none', () => {
	assert.deepEqual(validateTlsReplacement('unchanged', redactedEcho), {
		tlsCaCert: '',
		tlsCert: '',
		tlsKey: ''
	});
	assert.equal(hasTlsReplacementErrors(validateTlsReplacement('unchanged', redactedEcho)), false);

	const partial: TlsSecretFields = {
		tlsCaCert: 'ca',
		tlsCert: '   ',
		tlsKey: ''
	};
	const errors = validateTlsReplacement('replace', partial);
	assert.match(errors.tlsCaCert, /^$/);
	assert.match(errors.tlsCert, /required/);
	assert.match(errors.tlsKey, /required/);
	assert.equal(hasTlsReplacementErrors(errors), true);

	const complete = validateTlsReplacement('replace', {
		tlsCaCert: 'ca',
		tlsCert: 'client',
		tlsKey: 'key'
	});
	assert.equal(hasTlsReplacementErrors(complete), false);
});

test('component still gates submission on the explicit replace intent', () => {
	// Source-level guard: the editor must import the seam and spread its output,
	// not hand the redacted form fields straight back to updateNode.
	const source = fs.readFileSync(
		path.resolve(import.meta.dirname, '../components/node-settings.svelte'),
		'utf-8'
	);
	assert.match(source, /buildTlsUpdate\(editingNode, tlsReplaceIntent\)/);
	assert.match(source, /validateTlsReplacement\(tlsReplaceIntent, editingNode\)/);
	assert.match(source, /tlsReplaceIntent = 'unchanged'/);
	assert.doesNotMatch(source, /tlsCaCert: node\.tlsCaCert/);
});
