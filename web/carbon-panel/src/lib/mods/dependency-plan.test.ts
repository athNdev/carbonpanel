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
	});
});
