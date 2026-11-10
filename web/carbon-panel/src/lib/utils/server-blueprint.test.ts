// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { ModLoader } from '$lib/proto/carbonpanel/v1/common_pb';
import { nativeBedrockLimitation, resolveCreateBlueprint } from './server-blueprint';

test('qualified Java seed resolves image, TYPE env and Java protocol', () => {
	const spec = resolveCreateBlueprint({
		id: 'builtin-paper',
		dockerImage: '',
		modLoader: 'paper',
		defaultEnv: '{"EULA":"TRUE","TYPE":"PAPER","MEMORY":"4G"}'
	});
	assert.equal(spec.qualified, true);
	assert.equal(spec.protocol, 'java');
	assert.equal(spec.image, 'itzg/minecraft-server:java21');
	assert.equal(spec.storedImage, 'java21');
	assert.equal(spec.env.TYPE, 'PAPER');
	assert.equal(spec.env.EULA, 'TRUE');
	assert.equal(spec.gamePort, 25565);
	assert.equal(spec.memoryMb, 4096);
	assert.equal(spec.modLoader, ModLoader.PAPER);
	assert.equal(spec.limitation, '');
});

test('Bedrock full-image seed is labeled, not a native edition path', () => {
	const spec = resolveCreateBlueprint({
		id: 'builtin-bedrock',
		dockerImage: 'itzg/minecraft-bedrock-server:latest',
		modLoader: 'vanilla',
		defaultEnv: '{"EULA":"TRUE","GAMEMODE":"survival"}'
	});
	assert.equal(spec.qualified, false);
	assert.equal(spec.protocol, 'bedrock');
	assert.equal(spec.limitation, nativeBedrockLimitation);
	assert.equal(spec.image, 'itzg/minecraft-bedrock-server:latest');
});

test('create wizard lists catalog seeds and does not treat Bedrock as selectable', () => {
	const pagePath = path.resolve(import.meta.dirname, '../../routes/servers/new/+page.svelte');
	const content = fs.readFileSync(pagePath, 'utf8');
	assert.ok(content.includes('listBlueprints'), 'create path must load the blueprint catalog');
	assert.ok(
		content.includes('resolveCreateBlueprint'),
		'create path must resolve image/env/protocol'
	);
	assert.ok(content.includes('spec.qualified'), 'unqualified seeds must not be selected');
	assert.ok(
		content.includes('spec.limitation'),
		'unqualified seeds must show a supported-mode limitation'
	);
});
