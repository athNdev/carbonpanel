// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { create } from '@bufbuild/protobuf';
import type { DockerOverrides } from '$lib/proto/carbonpanel/v1/common_pb';
import { UpdateServerRequestSchema } from '$lib/proto/carbonpanel/v1/server_pb';
import { normalizeDockerOverrides, formatDockerOverrides } from './docker-overrides';
import { dockerOverridesFingerprint, snapshotServerForm } from './server-settings-snapshot';

function formWith(overrides: unknown) {
	const form = create(UpdateServerRequestSchema, { id: 'server-1' });
	(form as { dockerOverrides?: DockerOverrides }).dockerOverrides = overrides as DockerOverrides;
	return form;
}

test('the JSON editor boundary normalizes a plain parsed object for save', () => {
	const parsed = JSON.parse('{"environment":{"MODE":"json"},"memoryLimit":1024}');

	const overrides = normalizeDockerOverrides(parsed);

	assert.ok(overrides);
	assert.notEqual(overrides, parsed, 'a plain parse result must not be handed to the form as-is');
	assert.equal(overrides.environment.MODE, 'json');
	assert.equal(overrides.memoryLimit, 1024n);

	assert.doesNotThrow(() => snapshotServerForm(formWith(overrides)));
});

test('a typed partial with $typeName is normalized instead of fast-pathed', () => {
	const parsed = JSON.parse(
		'{"$typeName":"carbonpanel.v1.DockerOverrides","environment":{"MODE":"json"}}'
	);

	// Regression: the raw parse looks like a message to `create`, so the
	// typed partial reached the save snapshot and broke it.
	assert.throws(() => snapshotServerForm(formWith(parsed)));

	const overrides = normalizeDockerOverrides(parsed);
	assert.ok(overrides);
	assert.notEqual(overrides, parsed);
	assert.equal(overrides.environment.MODE, 'json');

	assert.doesNotThrow(() => snapshotServerForm(formWith(overrides)));
});

test('after save the form and baseline overrides fingerprints are equal', () => {
	const parsed = JSON.parse('{"environment":{"MODE":"json"},"memoryLimit":1024}');

	// Regression: the raw parse lacks the default fields a cloned baseline
	// carries, so a successful save left the form dirty.
	const rawForm = formWith(parsed);
	const rawBaseline = snapshotServerForm(rawForm);
	assert.notEqual(
		dockerOverridesFingerprint(rawForm.dockerOverrides),
		dockerOverridesFingerprint(rawBaseline.dockerOverrides)
	);

	const form = formWith(normalizeDockerOverrides(parsed));
	const baseline = snapshotServerForm(form);
	assert.equal(
		dockerOverridesFingerprint(form.dockerOverrides),
		dockerOverridesFingerprint(baseline.dockerOverrides)
	);
});

test('legal __proto__ and $typeName map keys survive normalization', () => {
	const parsed = JSON.parse(
		'{"environment":{"__proto__":"literal-proto","$typeName":"literal-type-name"},"labels":{"$typeName":"literal-label-type"}}'
	);

	const overrides = normalizeDockerOverrides(parsed);
	assert.ok(overrides);
	assert.equal(
		Object.getOwnPropertyDescriptor(overrides.environment, '__proto__')?.value,
		'literal-proto'
	);
	assert.equal(
		Object.getOwnPropertyDescriptor(overrides.environment, '$typeName')?.value,
		'literal-type-name'
	);
	assert.equal(
		Object.getOwnPropertyDescriptor(overrides.labels, '$typeName')?.value,
		'literal-label-type'
	);

	const form = formWith(overrides);
	const baseline = snapshotServerForm(form);
	const fingerprint = dockerOverridesFingerprint(baseline.dockerOverrides);
	assert.equal(dockerOverridesFingerprint(form.dockerOverrides), fingerprint);
	assert.match(fingerprint ?? '', /"__proto__":"literal-proto"/);
	assert.match(fingerprint ?? '', /"\$typeName":"literal-type-name"/);
});

test('an explicit empty override stays undefined', () => {
	assert.equal(normalizeDockerOverrides({}), undefined);
	assert.equal(normalizeDockerOverrides(undefined), undefined);
	assert.equal(normalizeDockerOverrides(null), undefined);
});

test('the JSON editor formatter round-trips int64 fields of a message', () => {
	const overrides = normalizeDockerOverrides(JSON.parse('{"memoryLimit":1024}'));
	assert.ok(overrides);

	let text = '';
	assert.doesNotThrow(() => {
		text = formatDockerOverrides(overrides);
	});
	assert.match(text, /"memoryLimit": "1024"/);

	const roundTripped = normalizeDockerOverrides(JSON.parse(text));
	assert.equal(roundTripped?.memoryLimit, 1024n);
	assert.equal(dockerOverridesFingerprint(roundTripped), dockerOverridesFingerprint(overrides));
});

test('the Docker Overrides editor normalizes at its JSON boundary', () => {
	const component = readFileSync(
		new URL('./docker-overrides-editor.svelte', import.meta.url),
		'utf8'
	);

	assert.match(component, /overrides = normalizeDockerOverrides\(parsed\);/);
});
