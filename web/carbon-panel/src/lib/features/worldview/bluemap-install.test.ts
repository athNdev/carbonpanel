// SPDX-License-Identifier: MPL-2.0

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { create } from '@bufbuild/protobuf';
import {
	ModuleSchema,
	type CreateModuleRequest,
	type ListModulesRequest,
	type Module
} from '$lib/proto/carbonpanel/v1/module_pb';
import { NodeSchema } from '$lib/proto/carbonpanel/v1/node_pb';
import { WorldViewTargetSchema, WorldRef_Kind } from '$lib/proto/carbonpanel/v1/worldview_pb';
import {
	BLUEMAP_TEMPLATE_ID,
	blueMapInstallEligibility,
	installBlueMap,
	type BlueMapInstallRpc
} from './bluemap-install';

class FakeRpc implements BlueMapInstallRpc {
	modules: Module[] = [];
	createCalls: CreateModuleRequest[] = [];
	listCalls: ListModulesRequest[] = [];

	async listModules(request: ListModulesRequest) {
		this.listCalls.push(request);
		return { modules: this.modules };
	}

	async createModule(request: CreateModuleRequest) {
		this.createCalls.push(request);
		return { module: create(ModuleSchema, { id: 'new-mod', name: request.name }) };
	}

	async listNodes(_request: Parameters<BlueMapInstallRpc['listNodes']>[0]) {
		return { nodes: [create(NodeSchema, { id: 'default', isLocal: true })] };
	}
}

const target = () =>
	create(WorldViewTargetSchema, {
		serverId: 'srv-blue',
		nodeId: 'default',
		worldRef: { kind: WorldRef_Kind.SERVER_LOCAL, serverId: 'srv-blue', levelName: 'actual_world' }
	});

const consent = { accepted: true };

test('eligibility requires local node, module permissions and a known level', () => {
	const nodes = [create(NodeSchema, { id: 'default', isLocal: true })];
	const canAll = () => true;
	assert.equal(blueMapInstallEligibility(canAll, nodes, target()).eligible, true);

	const remote = [create(NodeSchema, { id: 'node-2', isLocal: false })];
	assert.equal(blueMapInstallEligibility(canAll, remote, target()).eligible, false);

	assert.equal(blueMapInstallEligibility(() => false, nodes, target()).eligible, false);

	const noLevel = create(WorldViewTargetSchema, { serverId: 'srv-blue', nodeId: 'default' });
	assert.equal(blueMapInstallEligibility(canAll, nodes, noLevel).eligible, false);
});

test('adoption never creates, starts, reconfigures or duplicates', async () => {
	const rpc = new FakeRpc();
	rpc.modules = [
		create(ModuleSchema, {
			id: 'existing-mod',
			serverId: 'srv-blue',
			templateId: BLUEMAP_TEMPLATE_ID,
			status: 3
		})
	];
	const result = await installBlueMap(rpc, 'srv-blue', consent);
	assert.equal(result.outcome, 'adopted');
	assert.equal(result.module.id, 'existing-mod');
	assert.equal(rpc.createCalls.length, 0);
	assert.equal(rpc.listCalls.length, 1);
	assert.equal(rpc.listCalls[0]?.templateId, BLUEMAP_TEMPLATE_ID);
});

test('new install records consent and uses existing ModuleService create flow', async () => {
	const rpc = new FakeRpc();
	const result = await installBlueMap(rpc, 'srv-blue', consent);
	assert.equal(result.outcome, 'installed');
	assert.equal(rpc.createCalls.length, 1);
	const req = rpc.createCalls[0];
	assert.equal(req?.serverId, 'srv-blue');
	assert.equal(req?.templateId, BLUEMAP_TEMPLATE_ID);
	assert.equal(req?.startImmediately, true);
	assert.equal(req?.accessUrls.length, 0);
	assert.equal(req?.metadata['bluemap_managed'], 'true');
	assert.equal(req?.metadata['bluemap_consent_accepted'], 'true');
	assert.equal(req?.metadata['bluemap_version'], 'v5.29');
	assert.equal(req?.initCommand, '');
	assert.equal(req?.restartAfterInit, false);
});

test('consent is non-bypassable and performs no RPC', async () => {
	const rpc = new FakeRpc();
	let threw = false;
	try {
		await installBlueMap(rpc, 'srv-blue', { ...consent, accepted: false });
	} catch {
		threw = true;
	}
	assert.equal(threw, true);
	assert.equal(rpc.listCalls.length, 0);
	assert.equal(rpc.createCalls.length, 0);
});
