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
import {
	normalizeDockerOverrides,
	formatDockerOverrides,
	DockerOverridesValidationError
} from './docker-overrides';
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

test('semantically invalid overrides are rejected with the offending field named', () => {
	const cases: Array<[string, RegExp]> = [
		['{"memoryLimit":1.5}', /Docker overrides field "memoryLimit" must be an integer/],
		['{"memoryLimit":"abc"}', /Docker overrides field "memoryLimit" must be an integer/],
		[
			'{"memoryLimit":"9223372036854775808"}',
			/Docker overrides field "memoryLimit" must be within the int64 range/
		],
		['{"cpuLimit":"2.5"}', /Docker overrides field "cpuLimit" must be a finite number/],
		['{"environment":{"A":5}}', /Docker overrides field "environment\.A" must be a string/],
		['{"environment":["A"]}', /Docker overrides field "environment" must be an object of strings/],
		['{"labels":null}', /Docker overrides field "labels" must be an object of strings/],
		['{"volumes":{}}', /Docker overrides field "volumes" must be an array/],
		['{"volumes":[5]}', /Docker overrides field "volumes" entries must be objects/],
		[
			'{"volumes":[{"source":5}]}',
			/Docker overrides field "volumes\[0\]\.source" must be a string/
		],
		[
			'{"volumes":[{"readOnly":"yes"}]}',
			/Docker overrides field "volumes\[0\]\.readOnly" must be a boolean/
		],
		[
			'{"volumes":[{"source":"/a","bogus":true}]}',
			/unknown Docker overrides field "volumes\[0\]\.bogus"/
		],
		['{"volumes":[{"__proto__":{}}]}', /unknown Docker overrides field "volumes\[0\]\.__proto__"/],
		['{"capAdd":[5]}', /Docker overrides field "capAdd\[0\]" must be a string/],
		['{"capAdd":[null]}', /Docker overrides field "capAdd\[0\]" must be a string/],
		['{"capAdd":"NET_ADMIN"}', /Docker overrides field "capAdd" must be an array/],
		['{"dns":["1.1.1.1",{}]}', /Docker overrides field "dns\[1\]" must be a string/],
		[
			'{"memoryLimit":9007199254740993}',
			/Docker overrides field "memoryLimit" is not exactly representable as a number; use a decimal string/
		],
		['{"privileged":"true"}', /Docker overrides field "privileged" must be a boolean/],
		['{"networkMode":5}', /Docker overrides field "networkMode" must be a string/],
		['{"cpu_limt":2}', /unknown Docker overrides field "cpu_limt"/],
		['"abc"', /Docker overrides must be a JSON object/]
	];

	for (const [json, pattern] of cases) {
		assert.throws(
			() => normalizeDockerOverrides(JSON.parse(json)),
			(error: unknown) => {
				assert.ok(
					error instanceof DockerOverridesValidationError,
					`${json} must raise a validation error`
				);
				assert.match(error.message, pattern);
				return true;
			},
			json
		);
	}
});

test('a valid volume mount entry normalizes and snapshots', () => {
	const parsed = JSON.parse(
		'{"volumes":[{"source":"/host/data","target":"/data","type":"bind","readOnly":true,"createDir":false}]}'
	);

	const overrides = normalizeDockerOverrides(parsed);
	assert.ok(overrides);
	assert.equal(overrides.volumes.length, 1);
	const [volume] = overrides.volumes;
	assert.equal(volume.source, '/host/data');
	assert.equal(volume.target, '/data');
	assert.equal(volume.type, 'bind');
	assert.equal(volume.readOnly, true);
	assert.equal(volume.createDir, false);

	const form = formWith(overrides);
	assert.doesNotThrow(() => snapshotServerForm(form));
	const baseline = snapshotServerForm(form);
	assert.equal(
		dockerOverridesFingerprint(form.dockerOverrides),
		dockerOverridesFingerprint(baseline.dockerOverrides)
	);
});

test('int64 decimal strings stay exact at both range ends', () => {
	const max = normalizeDockerOverrides(JSON.parse('{"memoryLimit":"9223372036854775807"}'));
	const min = normalizeDockerOverrides(JSON.parse('{"memoryLimit":"-9223372036854775808"}'));

	assert.equal(max?.memoryLimit, 9223372036854775807n);
	assert.equal(min?.memoryLimit, -9223372036854775808n);
	assert.doesNotThrow(() => snapshotServerForm(formWith(max)));
	assert.doesNotThrow(() => snapshotServerForm(formWith(min)));
});

test('a decimal int64 string is still coerced to bigint after validation', () => {
	const overrides = normalizeDockerOverrides(JSON.parse('{"memoryLimit":"1024","cpuLimit":2.5}'));

	assert.equal(overrides?.memoryLimit, 1024n);
	assert.equal(overrides?.cpuLimit, 2.5);
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

test('the JSON editor announces validation failures and marks the textarea invalid', () => {
	const component = readFileSync(
		new URL('./docker-overrides-editor.svelte', import.meta.url),
		'utf8'
	);

	assert.match(component, /DockerOverridesValidationError/);
	assert.match(component, /id="docker-overrides-json-error"/);
	assert.match(component, /role="alert"/);
	assert.match(component, /aria-invalid=\{jsonError \? 'true' : undefined\}/);
	assert.match(
		component,
		/aria-describedby=\{jsonError \? 'docker-overrides-json-error' : undefined\}/
	);
});
