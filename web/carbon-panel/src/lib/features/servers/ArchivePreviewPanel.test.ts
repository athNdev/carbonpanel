// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { create } from '@bufbuild/protobuf';
import { compile } from 'svelte/compiler';
import { render } from 'svelte/server';
import { ModLoader } from '../../proto/carbonpanel/v1/common_pb';
import {
	PreviewServerArchiveResponseSchema,
	ServerArchiveFindingCode,
	ServerArchiveFindingSchema,
	ServerArchiveFindingSeverity,
	ServerArchiveFormat
} from '../../proto/carbonpanel/v1/server_pb';
import { describePreview, type ArchivePreviewView } from './archive-presentation';

const source = readFileSync(new URL('./ArchivePreviewPanel.svelte', import.meta.url), 'utf8');

async function renderPanel(view: ArchivePreviewView, nodeId = '', port = 25565): Promise<string> {
	const compiled = compile(source, { generate: 'server' }).js.code.replace(
		"'svelte/internal/server'",
		JSON.stringify(import.meta.resolve('svelte/internal/server'))
	);
	const { default: Panel } = await import(
		`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`
	);
	return render(Panel, { props: { view, nodeId, port } }).body;
}

test('the rendered preview shows runtime defaults, sizes, node capacity and ports', async () => {
	const html = await renderPanel(
		describePreview(
			create(PreviewServerArchiveResponseSchema, {
				format: ServerArchiveFormat.ZIP,
				schemaVersion: 1,
				exporterVersion: '1.0.0',
				mcVersion: '1.20.4',
				modLoader: ModLoader.PAPER,
				loaderVersion: 'build-400',
				memory: 2048,
				maxPlayers: 8,
				fileCount: 10,
				expandedBytes: 1048576n,
				requiredBytes: 2097152n,
				availableBytes: 4194304n
			})
		),
		'node-123',
		25570
	);

	assert.match(html, /ZIP \(\.zip\)/);
	assert.match(html, /1\.20\.4/);
	assert.match(html, /paper/);
	assert.match(html, /2048 MB/);
	assert.match(html, /2 MB/);
	assert.match(html, /4 MB/);
	assert.match(html, /Destination has enough free space/);
	assert.match(html, /node-123/);
	assert.match(html, /25570/);
	assert.match(html, /No compatibility findings/);
});

test('capacity and destination pairs stay compact instead of spanning the panel', () => {
	assert.match(
		source,
		/Required<\/span>\s*<span class="text-foreground">\{view\.requiredBytes\}/,
		'the required label and value must be an inline pair'
	);
	assert.match(
		source,
		/class="flex flex-wrap items-baseline gap-x-6 gap-y-1"/,
		'label/value groups must wrap as compact units'
	);
	assert.ok(
		!source.includes('flex justify-between gap-2'),
		'labels and values must not be pushed to opposite edges at desktop width'
	);
});

test('typed blockers and capacity refusal are visible in the rendered panel', async () => {
	const html = await renderPanel(
		describePreview(
			create(PreviewServerArchiveResponseSchema, {
				format: ServerArchiveFormat.TAR_GZ,
				requiredBytes: 4096n,
				availableBytes: 1024n,
				findings: [
					create(ServerArchiveFindingSchema, {
						severity: ServerArchiveFindingSeverity.BLOCKER,
						code: ServerArchiveFindingCode.CUSTOM_RUNTIME,
						message: 'Archive needs custom container settings'
					})
				]
			})
		)
	);

	assert.match(html, /TAR\.GZ \(\.tar\.gz\)/);
	assert.match(html, /Destination does not have enough free space/);
	assert.match(html, /Blocker/);
	assert.match(html, /Custom runtime/);
	assert.match(html, /Archive needs custom container settings/);
	assert.match(html, /blocking findings/);
});
