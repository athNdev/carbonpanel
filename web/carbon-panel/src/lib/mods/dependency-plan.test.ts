// SPDX-License-Identifier: MPL-2.0

import { describe, test } from 'node:test';
import assert from 'node:assert/strict';
import {
	MAX_DEPENDENCY_DEPTH,
	buildDependencyPlan,
	type DependencyLookup,
	type PlanDependency,
	type PlanVersion
} from './dependency-plan';

const dep = (projectId: string, type = 'required', title?: string): PlanDependency => ({
	project_id: projectId,
	dependency_type: type,
	title
});

const version = (name: string, deps: PlanDependency[] = []): PlanVersion => ({
	id: `${name}-id`,
	version_number: '1.0.0',
	name,
	version_type: 'release',
	file_name: `${name}.jar`,
	download_url: `https://cdn/${name}.jar`,
	file_size: 1024,
	dependencies: deps
});

function lookup(handlers: Record<string, PlanVersion[]>): DependencyLookup {
	const calls: string[] = [];
	const fn: DependencyLookup = async (request) => {
		calls.push(request.project);
		const versions = handlers[request.project];
		if (!versions) throw new Error(`provider outage for ${request.project}`);
		return { versions };
	};
	(fn as DependencyLookup & { calls: string[] }).calls = calls;
	return fn;
}

const base = {
	platform: 'modrinth',
	loader: 'fabric',
	mcVersion: '1.21.1',
	selection: {} as Record<string, boolean>
};

describe('buildDependencyPlan', () => {
	test('resolves selected dependencies into install items', async () => {
		const getVersions = lookup({ 'storage-api': [version('Storage API')] });
		const plan = await buildDependencyPlan({
			...base,
			deps: [dep('storage-api', 'required', 'Storage API')],
			selection: { 'storage-api': true },
			getVersions
		});
		assert.deepEqual(
			plan.items.map((item) => item.file_name),
			['Storage API.jar']
		);
		assert.equal(plan.unresolved.length, 0);
	});

	test('ignores unchecked and incompatible dependencies', async () => {
		const getVersions = lookup({});
		const plan = await buildDependencyPlan({
			...base,
			deps: [dep('checked'), dep('unchecked'), dep('bad', 'incompatible')],
			selection: { checked: true, unchecked: false, bad: true },
			getVersions
		});
		assert.equal(plan.items.length, 0);
		// Only the explicitly checked, non-incompatible dep was looked up.
		const calls = (getVersions as DependencyLookup & { calls: string[] }).calls;
		assert.deepEqual(calls, ['checked']);
	});

	test('provider outage collects unresolved dependencies instead of dropping them (E9)', async () => {
		const getVersions = lookup({}); // every project "fails"
		const plan = await buildDependencyPlan({
			...base,
			deps: [dep('flaky-api', 'required', 'Flaky API')],
			selection: { 'flaky-api': true },
			getVersions
		});
		assert.equal(plan.items.length, 0);
		assert.deepEqual(plan.unresolved, [
			{
				projectId: 'flaky-api',
				title: 'Flaky API',
				reason: 'Unresolved: provider outage for flaky-api'
			}
		]);
	});

	test('an empty version list is an explicit unresolved outcome, not a silent skip', async () => {
		const getVersions = lookup({ 'no-files': [] });
		const plan = await buildDependencyPlan({
			...base,
			deps: [dep('no-files')],
			selection: { 'no-files': true },
			getVersions
		});
		assert.equal(plan.items.length, 0);
		assert.equal(plan.unresolved.length, 1);
		assert.match(plan.unresolved[0].reason, /No compatible version/);
	});

	test('transitive required dependencies are folded into the selection and reported (E8)', async () => {
		const getVersions = lookup({
			'cloth-config': [version('Cloth Config', [dep('fabric-api')])],
			'fabric-api': [version('Fabric API')]
		});
		const plan = await buildDependencyPlan({
			...base,
			deps: [dep('cloth-config', 'required', 'Cloth Config')],
			selection: { 'cloth-config': true },
			getVersions
		});
		// Cloth Config plus its own required dep.
		assert.deepEqual(
			plan.items.map((item) => item.name).sort(),
			['Cloth Config', 'Fabric API'].sort()
		);
		assert.deepEqual(plan.autoIncluded, ['fabric-api']);
		assert.equal(plan.selection['fabric-api'], true);
	});

	test('explicit user choices are never overridden by auto-inclusion', async () => {
		const getVersions = lookup({
			a: [version('A', [dep('shared')])],
			b: [version('B', [dep('shared')])]
		});
		const plan = await buildDependencyPlan({
			...base,
			deps: [dep('a'), dep('b')],
			selection: { a: true, b: true, shared: false }, // operator unchecked it
			getVersions
		});
		assert.deepEqual(plan.selection.shared, false);
		assert.equal(plan.items.length, 2); // a and b only
	});

	test('known projects (the root mod) are not resolved a second time', async () => {
		const getVersions = lookup({ other: [version('Other')] });
		const plan = await buildDependencyPlan({
			...base,
			deps: [dep('root-mod'), dep('other')],
			selection: { 'root-mod': true, other: true },
			isKnownProject: (projectId) => projectId === 'root-mod',
			getVersions
		});
		const calls = (getVersions as DependencyLookup & { calls: string[] }).calls;
		assert.deepEqual(calls, ['other'], 'the known root project must never be looked up');
		assert.deepEqual(
			plan.items.map((item) => item.name),
			['Other']
		);
		assert.equal(plan.unresolved.length, 0);
	});

	test('resolution depth is bounded', async () => {
		const handlers: Record<string, PlanVersion[]> = {};
		for (let i = 0; i <= MAX_DEPENDENCY_DEPTH + 3; i++) {
			handlers[`p${i}`] = [version(`P${i}`, [dep(`p${i + 1}`)])];
		}
		const getVersions = lookup(handlers);
		const plan = await buildDependencyPlan({
			...base,
			deps: [dep('p0')],
			selection: { p0: true },
			getVersions
		});
		// p0's own lookup plus MAX_DEPENDENCY_DEPTH nested lookups.
		assert.equal(plan.items.length, MAX_DEPENDENCY_DEPTH + 1);
		assert.deepEqual(plan.unresolved, [
			{
				projectId: `p${MAX_DEPENDENCY_DEPTH + 1}`,
				title: `p${MAX_DEPENDENCY_DEPTH + 1}`,
				reason: 'Dependency resolution depth limit reached'
			}
		]);
	});

	test('two different pinned versions for one project conflict instead of installing twice', async () => {
		const getVersions = lookup({
			'pinned-api': [
				{ ...version('Pinned A'), id: 'a-id' },
				{ ...version('Pinned B'), id: 'b-id' }
			]
		});
		const plan = await buildDependencyPlan({
			...base,
			deps: [
				{
					project_id: 'pinned-api',
					version_id: 'a-id',
					dependency_type: 'required',
					title: 'Pinned API'
				},
				{
					project_id: 'pinned-api',
					version_id: 'b-id',
					dependency_type: 'required',
					title: 'Pinned API'
				}
			],
			selection: { 'pinned-api': true },
			getVersions
		});

		// Only the first resolved pin is installed; the conflicting request is
		// surfaced, never installed as a second copy.
		assert.deepEqual(
			plan.items.map((item) => item.file_name),
			['Pinned A.jar']
		);
		const calls = (getVersions as DependencyLookup & { calls: string[] }).calls;
		assert.deepEqual(calls, ['pinned-api'], 'the project must be looked up once');
		assert.equal(plan.unresolved.length, 1);
		assert.equal(plan.unresolved[0].projectId, 'pinned-api');
		assert.match(plan.unresolved[0].reason, /conflicting pinned versions/i);
		assert.match(plan.unresolved[0].reason, /a-id/);
		assert.match(plan.unresolved[0].reason, /b-id/);
	});

	test('a repeated conflicting pin produces a single conflict entry', async () => {
		const getVersions = lookup({
			'pinned-api': [
				{ ...version('Pinned A'), id: 'a-id' },
				{ ...version('Pinned B'), id: 'b-id' }
			]
		});
		const plan = await buildDependencyPlan({
			...base,
			deps: [
				{ project_id: 'pinned-api', version_id: 'a-id', dependency_type: 'required' },
				{ project_id: 'pinned-api', version_id: 'b-id', dependency_type: 'required' },
				{ project_id: 'pinned-api', version_id: 'b-id', dependency_type: 'required' },
				{ project_id: 'pinned-api', version_id: 'b-id', dependency_type: 'required' }
			],
			selection: { 'pinned-api': true },
			getVersions
		});

		assert.deepEqual(
			plan.items.map((item) => item.file_name),
			['Pinned A.jar']
		);
		assert.equal(plan.unresolved.length, 1, 'the same project+pin conflict is reported once');
		assert.match(plan.unresolved[0].reason, /b-id/);
	});

	test('production-shaped empty version_id is unpinned and dedupes without conflict', async () => {
		const getVersions = lookup({ 'latest-api': [version('Latest API')] });
		const plan = await buildDependencyPlan({
			...base,
			deps: [
				{ project_id: 'latest-api', version_id: '', dependency_type: 'required' },
				{ project_id: 'latest-api', version_id: '   ', dependency_type: 'required' }
			],
			selection: { 'latest-api': true },
			getVersions
		});

		assert.deepEqual(
			plan.items.map((item) => item.file_name),
			['Latest API.jar']
		);
		assert.equal(plan.unresolved.length, 0, 'empty and whitespace pins are both unpinned');
		const calls = (getVersions as DependencyLookup & { calls: string[] }).calls;
		assert.deepEqual(calls, ['latest-api']);
	});

	test('a known project pin seeds the plan so a later conflict is surfaced', async () => {
		const getVersions = lookup({});
		const plan = await buildDependencyPlan({
			...base,
			deps: [
				{
					project_id: 'root-mod',
					version_id: 'root-a',
					dependency_type: 'required',
					title: 'Root Mod'
				},
				{
					project_id: 'root-mod',
					version_id: 'root-b',
					dependency_type: 'required',
					title: 'Root Mod'
				}
			],
			selection: { 'root-mod': true },
			isKnownProject: (projectId) => projectId === 'root-mod',
			getVersions
		});

		const calls = (getVersions as DependencyLookup & { calls: string[] }).calls;
		assert.deepEqual(calls, [], 'the known project is still never looked up');
		assert.equal(plan.items.length, 0);
		assert.equal(plan.unresolved.length, 1);
		assert.equal(plan.unresolved[0].projectId, 'root-mod');
		assert.match(plan.unresolved[0].reason, /root-a/);
		assert.match(plan.unresolved[0].reason, /root-b/);
	});

	test('the same pin requested twice is deduped without a conflict', async () => {
		const getVersions = lookup({
			'pinned-api': [{ ...version('Pinned A'), id: 'a-id' }]
		});
		const plan = await buildDependencyPlan({
			...base,
			deps: [
				{ project_id: 'pinned-api', version_id: 'a-id', dependency_type: 'required' },
				{ project_id: 'pinned-api', version_id: 'a-id', dependency_type: 'required' }
			],
			selection: { 'pinned-api': true },
			getVersions
		});

		assert.deepEqual(
			plan.items.map((item) => item.file_name),
			['Pinned A.jar']
		);
		assert.equal(plan.unresolved.length, 0);
		const calls = (getVersions as DependencyLookup & { calls: string[] }).calls;
		assert.deepEqual(calls, ['pinned-api']);
	});

	test('an unpinned project requested twice is deduped without a conflict', async () => {
		const getVersions = lookup({ 'latest-api': [version('Latest API')] });
		const plan = await buildDependencyPlan({
			...base,
			deps: [dep('latest-api'), dep('latest-api')],
			selection: { 'latest-api': true },
			getVersions
		});

		assert.deepEqual(
			plan.items.map((item) => item.file_name),
			['Latest API.jar']
		);
		assert.equal(plan.unresolved.length, 0);
		const calls = (getVersions as DependencyLookup & { calls: string[] }).calls;
		assert.deepEqual(calls, ['latest-api']);
	});

	test('a pinned dependency version is selected instead of the newest version', async () => {
		const getVersions = lookup({
			'pinned-api': [version('Newest API'), { ...version('Pinned API'), id: 'pinned-version-id' }]
		});
		const plan = await buildDependencyPlan({
			...base,
			deps: [
				{
					project_id: 'pinned-api',
					version_id: 'pinned-version-id',
					dependency_type: 'required',
					title: 'Pinned API'
				}
			],
			selection: { 'pinned-api': true },
			getVersions
		});

		assert.deepEqual(
			plan.items.map((item) => item.file_name),
			['Pinned API.jar']
		);
		assert.equal(plan.unresolved.length, 0);
	});
});
