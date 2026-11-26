// SPDX-License-Identifier: MPL-2.0

import { create, type MessageInitShape } from '@bufbuild/protobuf';
import { describe, test } from 'node:test';
import assert from 'node:assert/strict';
import { ModDependencySchema, ModVersionSchema } from '$lib/proto/carbonpanel/v1/mod_browse_pb';
import { resolveWizardMods } from './create-content';

const target = { loader: 'fabric', mcVersion: '1.21.1' };
const required = (projectId: string, versionId = '') => ({
	projectId,
	versionId,
	dependencyType: 'required'
});
const version = (id: string, dependencies: MessageInitShape<typeof ModDependencySchema>[] = []) =>
	create(ModVersionSchema, {
		id,
		name: id,
		versionType: 'release',
		fileName: `${id}.jar`,
		downloadUrl: `https://cdn.example.com/${id}.jar`,
		dependencies
	});
const root = (id: string, name: string, release = version(id)) => ({
	id,
	name,
	platform: 'modrinth',
	version: release
});

describe('wizard mod dependency constraints', () => {
	test('reconsiders a choice when a later branch pins it and discards the superseded closure', async () => {
		const versions = {
			b: [version('b2', [required('removed')]), version('b1')],
			c: [version('c1', [required('b', 'b1')])],
			removed: [version('removed1')]
		};
		const plan = await resolveWizardMods(
			target,
			[root('a', 'A', version('a1', [required('b'), required('c')]))],
			async ({ project }) => versions[project as keyof typeof versions]
		);
		assert.deepEqual(
			plan[0].files.map((file) => file.version),
			['a1', 'b1', 'c1']
		);
	});

	test('resolves across roots and refuses conflicting exact pins', async () => {
		const lookup = async () => [version('b2'), version('b1')];
		const a = root('a', 'A', version('a1', [required('b')]));
		const c = root('c', 'C', version('c1', [required('b', 'b1')]));
		const plan = await resolveWizardMods(target, [a, c], lookup);
		assert.ok(plan.every((item) => item.files.some((file) => file.version === 'b1')));
		const aPinned = root('a', 'A', version('a2', [required('b', 'b2')]));
		await assert.rejects(resolveWizardMods(target, [aPinned, c], lookup), /conflicting versions/);
	});

	test('reports both incompatible mods even when the declaring mod was queued first', async () => {
		const a = root(
			'a',
			'Fixture Optimizer',
			version('a1', [{ projectId: 'b', dependencyType: 'incompatible' }])
		);
		const b = root('b', 'Fixture Visuals');
		await assert.rejects(
			resolveWizardMods(target, [a, b], async () => []),
			/Fixture Optimizer is incompatible with Fixture Visuals\./
		);
		await assert.rejects(
			resolveWizardMods(target, [b, a], async () => []),
			/Fixture Optimizer is incompatible with Fixture Visuals\./
		);
	});

	test('a version-only incompatibility applies to the named release', async () => {
		const a = root('a', 'A', version('a1', [{ versionId: 'b1', dependencyType: 'incompatible' }]));
		const b1 = root('b', 'B', version('b1'));
		const b2 = root('b', 'B', version('b2'));
		await assert.rejects(
			resolveWizardMods(target, [a, b1], async () => []),
			/A is incompatible with B\./
		);
		assert.equal((await resolveWizardMods(target, [a, b2], async () => [])).length, 2);
	});

	test('tries an older unpinned release when the preferred release declares an incompatibility', async () => {
		const a = root('a', 'A', version('a1', [required('b')]));
		const b2 = version('b2', [{ projectId: 'a', dependencyType: 'incompatible' }]);
		const b1 = version('b1');
		const plan = await resolveWizardMods(target, [a], async () => [b2, b1]);
		assert.deepEqual(
			plan[0].files.map((file) => file.version),
			['a1', 'b1']
		);
	});
});
