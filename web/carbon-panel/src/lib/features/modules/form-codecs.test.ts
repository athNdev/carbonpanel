// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { create, toJson } from '@bufbuild/protobuf';
import {
	ModulePortSchema,
	ModuleDependencySchema,
	ModuleEventHookSchema,
	CreateModuleRequestSchema,
	CreateModuleTemplateRequestSchema
} from '../../proto/carbonpanel/v1/module_pb';
import {
	envVarsToJson,
	volumesToJson,
	parseEnvVars,
	parseVolumes,
	parsePorts,
	parseDependencies,
	parseEventHooks,
	parseMetadata,
	metadataToMap,
	instanceHealthDefaults
} from './form-codecs';

test('environment and metadata keep values but trim/drop keys and use the last duplicate', () => {
	const rows = [
		{ key: '  A  ', value: '  value  ' },
		{ key: '', value: 'drop' },
		{ key: '  ', value: 'drop' },
		{ key: 'A', value: 'last' },
		{ key: 'B', value: '' }
	];
	assert.equal(envVarsToJson(rows), '{"A":"last","B":""}');
	assert.deepEqual(metadataToMap(rows), { A: 'last', B: '' });
	assert.deepEqual(parseEnvVars('{"number":1,"flag":false,"nested":{},"nil":null}'), [
		{ key: 'number', value: '1' },
		{ key: 'flag', value: 'false' },
		{ key: 'nested', value: '[object Object]' },
		{ key: 'nil', value: 'null' }
	]);
	assert.deepEqual(parseMetadata({ A: 'value' }), [{ key: 'A', value: 'value' }]);
});

test('volume rows round-trip paths and independent read-only/create-dir flags', () => {
	const rows = [
		{ hostPath: ' /data/a ', containerPath: ' /a ', readOnly: true, createDir: false },
		{ hostPath: '/data/b', containerPath: '/b', readOnly: false, createDir: true },
		{ hostPath: ' ', containerPath: '/drop', readOnly: false, createDir: false }
	];
	const encoded = volumesToJson(rows);
	assert.deepEqual(JSON.parse(encoded), [
		{ source: '/data/a', target: '/a', read_only: true, create_dir: false },
		{ source: '/data/b', target: '/b', read_only: false, create_dir: true }
	]);
	assert.equal(volumesToJson(parseVolumes(encoded)), encoded);
});

test('malformed persisted JSON keeps the existing explicit empty-row fallback', () => {
	for (const raw of ['', '{invalid', 'null']) assert.deepEqual(parseEnvVars(raw), []);
	for (const raw of ['', '{invalid', 'null', '{}', '[null]'])
		assert.deepEqual(parseVolumes(raw), []);
	assert.deepEqual(parseEnvVars('["legacy"]'), [{ key: '0', value: 'legacy' }]);
});

test('instance rows are independent copies with the original numeric and protocol defaults', () => {
	const ports = [
		create(ModulePortSchema, { name: 'port', containerPort: 123, hostPort: 0, protocol: '' })
	];
	const rows = parsePorts(ports);
	assert.equal(rows[0].protocol, 'tcp');
	rows[0].hostPort = 8123;
	assert.equal(ports[0].hostPort, 0);
	const deps = [
		create(ModuleDependencySchema, {
			moduleId: 'dependency',
			waitForHealthy: true,
			timeoutSeconds: 42
		})
	];
	assert.equal(parseDependencies(deps)[0].timeoutSeconds, 42);
	assert.notEqual(parseDependencies(deps)[0], deps[0]);
	const hooks = [
		create(ModuleEventHookSchema, { command: 'echo fixture', delaySeconds: 3, condition: 'ready' })
	];
	assert.equal(parseEventHooks(hooks)[0].condition, 'ready');
	assert.notEqual(parseEventHooks(hooks)[0], hooks[0]);
	assert.deepEqual(
		instanceHealthDefaults({
			healthCheckInterval: 0,
			healthCheckTimeout: 0,
			healthCheckRetries: 0
		}),
		{ healthCheckInterval: 30, healthCheckTimeout: 5, healthCheckRetries: 3 }
	);
	assert.deepEqual(
		instanceHealthDefaults({
			healthCheckInterval: 12,
			healthCheckTimeout: 2,
			healthCheckRetries: 7
		}),
		{ healthCheckInterval: 12, healthCheckTimeout: 2, healthCheckRetries: 7 }
	);
});

test('template defaults and instance overrides encode equivalent values in distinct generated fields', () => {
	const env = envVarsToJson([{ key: ' A ', value: 'value' }]);
	const volumes = volumesToJson([
		{ hostPath: ' /data ', containerPath: '/mount', readOnly: true, createDir: false }
	]);
	const metadata = metadataToMap([{ key: ' custom ', value: 'value' }]);
	const instance = toJson(
		CreateModuleRequestSchema,
		create(CreateModuleRequestSchema, {
			name: 'Instance',
			templateId: 'template',
			envOverrides: env,
			volumeOverrides: volumes,
			metadata
		})
	);
	const template = toJson(
		CreateModuleTemplateRequestSchema,
		create(CreateModuleTemplateRequestSchema, {
			name: 'Template',
			defaultEnv: env,
			defaultVolumes: volumes,
			metadata
		})
	);
	assert.deepEqual(instance, {
		name: 'Instance',
		templateId: 'template',
		envOverrides: '{"A":"value"}',
		volumeOverrides: '[{"source":"/data","target":"/mount","read_only":true,"create_dir":false}]',
		metadata: { custom: 'value' }
	});
	assert.deepEqual(template, {
		name: 'Template',
		defaultEnv: '{"A":"value"}',
		defaultVolumes: '[{"source":"/data","target":"/mount","read_only":true,"create_dir":false}]',
		metadata: { custom: 'value' }
	});
});
