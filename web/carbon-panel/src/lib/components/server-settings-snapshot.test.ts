// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
// Svelte's runtime proxy is the seam a `$state` component value exposes, but
// its types are not part of the package's public type surface.
// @ts-expect-error -- no declaration file for svelte/internal/client
import { proxy } from 'svelte/internal/client';
import { create } from '@bufbuild/protobuf';
import { DockerOverridesSchema } from '$lib/proto/carbonpanel/v1/common_pb';
import { UpdateServerRequestSchema } from '$lib/proto/carbonpanel/v1/server_pb';
import { snapshotDockerOverrides, snapshotServerForm } from './server-settings-snapshot';

test('snapshotServerForm clones a form whose Docker overrides contain int64 bigints', () => {
	const memoryLimit = 9_007_199_254_740_993n;
	const shmSize = 8_589_934_592n;
	const source = create(UpdateServerRequestSchema, {
		id: 'server-1',
		name: 'Snapshot source',
		dockerOverrides: create(DockerOverridesSchema, {
			memoryLimit,
			shmSize,
			environment: { MODE: 'source' },
			capAdd: ['NET_ADMIN']
		})
	});

	const snapshot = snapshotServerForm(source);

	assert.equal(snapshot.dockerOverrides?.memoryLimit, memoryLimit);
	assert.equal(snapshot.dockerOverrides?.shmSize, shmSize);

	// The snapshot must be independent of the form it was taken from.
	assert.notEqual(snapshot, source);
	assert.notEqual(snapshot.dockerOverrides, source.dockerOverrides);
	snapshot.dockerOverrides!.memoryLimit = 1n;
	snapshot.dockerOverrides!.environment.MODE = 'clone';
	assert.equal(source.dockerOverrides?.memoryLimit, memoryLimit);
	assert.equal(source.dockerOverrides?.environment.MODE, 'source');
});

test('snapshotServerForm keeps user map keys that collide with message internals', () => {
	const memoryLimit = 9_007_199_254_740_993n;
	const environment = Object.create(null) as { [key: string]: string };
	Object.defineProperty(environment, '$typeName', {
		value: 'literal-type-name',
		enumerable: true,
		configurable: true,
		writable: true
	});
	Object.defineProperty(environment, '__proto__', {
		value: 'literal-proto',
		enumerable: true,
		configurable: true,
		writable: true
	});
	const source = create(UpdateServerRequestSchema, {
		dockerOverrides: create(DockerOverridesSchema, { memoryLimit, environment })
	});

	const snapshot = snapshotServerForm(source);
	const snapshotEnvironment = snapshot.dockerOverrides!.environment;

	// `$typeName` and `__proto__` are legal environment variable names, not
	// internal state, so both must survive with their values as own entries.
	assert.equal(
		Object.getOwnPropertyDescriptor(snapshotEnvironment, '$typeName')?.value,
		'literal-type-name'
	);
	assert.equal(
		Object.getOwnPropertyDescriptor(snapshotEnvironment, '__proto__')?.value,
		'literal-proto'
	);
	assert.equal(snapshot.$typeName, 'carbonpanel.v1.UpdateServerRequest');

	snapshot.dockerOverrides!.memoryLimit = 1n;
	Object.defineProperty(snapshotEnvironment, '$typeName', { value: 'changed' });
	Object.defineProperty(snapshotEnvironment, '__proto__', { value: 'changed' });

	assert.equal(source.dockerOverrides?.memoryLimit, memoryLimit);
	assert.equal(
		Object.getOwnPropertyDescriptor(source.dockerOverrides!.environment, '$typeName')?.value,
		'literal-type-name'
	);
	assert.equal(
		Object.getOwnPropertyDescriptor(source.dockerOverrides!.environment, '__proto__')?.value,
		'literal-proto'
	);
});

test('snapshotServerForm keeps production-shaped map keys through the reactive proxy', () => {
	// Production maps are plain `{}` objects, not null-prototype ones, and the
	// component snapshot is taken from `$state`-proxied form data. Svelte's
	// `$state.snapshot` would clone these with `copy[key] = ...` and lose the
	// own `__proto__` entry before protobuf-es's clone could copy it.
	const environment: { [key: string]: string } = { MODE: 'source' };
	const labels: { [key: string]: string } = { 'com.example.role': 'server' };
	for (const map of [environment, labels]) {
		Object.defineProperty(map, '$typeName', {
			value: 'literal-type-name',
			enumerable: true,
			configurable: true,
			writable: true
		});
		Object.defineProperty(map, '__proto__', {
			value: 'literal-proto',
			enumerable: true,
			configurable: true,
			writable: true
		});
	}
	const source = create(UpdateServerRequestSchema, {
		id: 'server-1',
		dockerOverrides: create(DockerOverridesSchema, { environment, labels })
	});
	const proxySource = proxy(source);
	assert.notEqual(proxySource, source, 'expected the reactive proxy wrapper');

	const snapshot = snapshotServerForm(proxySource);

	assert.notEqual(snapshot, proxySource);
	assert.notEqual(snapshot.dockerOverrides, proxySource.dockerOverrides);

	for (const [name, map] of [
		['environment', snapshot.dockerOverrides!.environment],
		['labels', snapshot.dockerOverrides!.labels]
	] as const) {
		assert.equal(
			Object.getOwnPropertyDescriptor(map, '$typeName')?.value,
			'literal-type-name',
			`${name} must keep its own $typeName entry`
		);
		assert.equal(
			Object.getOwnPropertyDescriptor(map, '__proto__')?.value,
			'literal-proto',
			`${name} must keep its own __proto__ entry`
		);
	}

	// The snapshot must not write through to the reactive form.
	snapshot.dockerOverrides!.environment.MODE = 'snapshot';
	Object.defineProperty(snapshot.dockerOverrides!.environment, '__proto__', { value: 'changed' });
	assert.equal(environment.MODE, 'source');
	assert.equal(Object.getOwnPropertyDescriptor(environment, '__proto__')?.value, 'literal-proto');
});

test('snapshotDockerOverrides preserves production-shaped map keys and is independent', () => {
	// The dirty check compares snapshots of both overrides. Snapshotting with
	// `$state.snapshot` would drop the own `__proto__` entry, so an edit to
	// only that legal environment/label key would never mark the form dirty.
	const environment: { [key: string]: string } = { MODE: 'source' };
	const labels: { [key: string]: string } = { 'com.example.role': 'server' };
	for (const map of [environment, labels]) {
		Object.defineProperty(map, '$typeName', {
			value: 'literal-type-name',
			enumerable: true,
			configurable: true,
			writable: true
		});
		Object.defineProperty(map, '__proto__', {
			value: 'literal-proto',
			enumerable: true,
			configurable: true,
			writable: true
		});
	}
	const source = create(DockerOverridesSchema, { environment, labels });

	const snapshot = snapshotDockerOverrides(source);
	assert.ok(snapshot);
	assert.notEqual(snapshot, source);

	for (const [name, map] of [
		['environment', snapshot.environment],
		['labels', snapshot.labels]
	] as const) {
		assert.equal(
			Object.getOwnPropertyDescriptor(map, '$typeName')?.value,
			'literal-type-name',
			`${name} must keep its own $typeName entry`
		);
		assert.equal(
			Object.getOwnPropertyDescriptor(map, '__proto__')?.value,
			'literal-proto',
			`${name} must keep its own __proto__ entry`
		);
	}

	// The snapshot must not alias the source it was taken from.
	Object.defineProperty(snapshot.environment, '__proto__', { value: 'changed' });
	snapshot.labels['com.example.role'] = 'changed';
	assert.equal(Object.getOwnPropertyDescriptor(environment, '__proto__')?.value, 'literal-proto');
	assert.equal(labels['com.example.role'], 'server');

	assert.equal(snapshotDockerOverrides(undefined), undefined);
});

test('the settings form hands the reactive source straight to the snapshot helpers', () => {
	const component = readFileSync(new URL('./server-settings.svelte', import.meta.url), 'utf8');

	// `$state.snapshot(source)` drops an own `__proto__` map key before the
	// helper's protobuf-es clone runs, which loses a legal Docker
	// environment/label entry from the dirty baseline and from Undo.
	assert.doesNotMatch(component, /snapshotServerForm\(\$state\.snapshot\(/);
	assert.match(component, /return snapshotServerForm\(source\);/);

	// The dirty comparison must use the Docker overrides snapshot too, or the
	// same key-loss class hides an edit from Save.
	assert.doesNotMatch(component, /\$state\.snapshot\([^)]*dockerOverrides/);
	assert.match(component, /snapshotDockerOverrides\(form\.dockerOverrides\)/);
	assert.match(component, /snapshotDockerOverrides\(baseline\.dockerOverrides\)/);
});
