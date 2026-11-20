// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { compile } from 'svelte/compiler';
import { render } from 'svelte/server';
import {
	ARCHIVE_CREDENTIAL_DISCLOSURE,
	ARCHIVE_CREDENTIAL_DISCLOSURE_DETAIL,
	ARCHIVE_FORMATS,
	archiveConsistencyLabel
} from './archive-presentation';
import {
	ServerArchiveConsistency,
	ServerArchiveFormat
} from '../../proto/carbonpanel/v1/server_pb';

const source = readFileSync(new URL('./ArchiveExportPanel.svelte', import.meta.url), 'utf8');

type Phase = 'idle' | 'preparing' | 'ready' | 'error';

async function renderPanel(overrides: Partial<ReturnType<typeof baseProps>> = {}): Promise<string> {
	const compiled = compile(source, { generate: 'server' }).js.code.replace(
		"'svelte/internal/server'",
		JSON.stringify(import.meta.resolve('svelte/internal/server'))
	);
	const { default: Panel } = await import(
		`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`
	);
	return render(Panel, { props: { ...baseProps(), ...overrides } }).body;
}

function baseProps() {
	return {
		serverName: 'survival-01',
		running: false,
		formats: ARCHIVE_FORMATS,
		format: ServerArchiveFormat.ZIP,
		onformat: () => {},
		phase: 'idle' as Phase,
		filename: 'survival-01.zip',
		totalSizeLabel: '12 MB',
		error: null as string | null,
		disclosure: ARCHIVE_CREDENTIAL_DISCLOSURE,
		disclosureDetail: ARCHIVE_CREDENTIAL_DISCLOSURE_DETAIL,
		consistencyText: archiveConsistencyLabel(ServerArchiveConsistency.STOPPED),
		acknowledged: false,
		onacknowledge: () => {},
		onprepare: () => {},
		ondownload: () => {}
	};
}

test('both archive formats and stopped-only consistency are offered', async () => {
	const html = await renderPanel();
	assert.match(html, /ZIP \(\.zip\)/);
	assert.match(html, /TAR\.GZ \(\.tar\.gz\)/);
	assert.match(html, /must already be stopped/);
	assert.match(html, /Prepare export/);
	assert.doesNotMatch(html, /secret detection/i);
});

test('a running server is refused rather than silently stopped', async () => {
	const html = await renderPanel({ running: true });
	assert.match(html, /will not stop a live\s+server/);
	const prepare = html.match(/<button[^>]*data-testid="archive-prepare"[^>]*>/)?.[0] ?? '';
	assert.match(prepare, /\sdisabled(?:=""|\s|>)/);
});

test('the credential disclosure appears before download and gates the button', async () => {
	const html = await renderPanel({ phase: 'ready' });
	assert.match(html, /Third-party plugin and mod files are included and can contain credentials/);
	assert.match(html, /contents of third-party plugin and mod files are not inspected/);
	const download = html.match(/<button[^>]*data-testid="archive-download"[^>]*>/)?.[0] ?? '';
	assert.match(download, /\sdisabled(?:=""|\s|>)/);
	assert.match(html, /survival-01\.zip/);
	assert.match(html, /12 MB/);
});

test('acknowledging the disclosure enables the download', async () => {
	const html = await renderPanel({ phase: 'ready', acknowledged: true });
	const download = html.match(/<button[^>]*data-testid="archive-download"[^>]*>/)?.[0] ?? '';
	assert.doesNotMatch(download, /\sdisabled(?:=""|\s|>)/);
});

test('the preparation phase reports a longer-running operation', async () => {
	const html = await renderPanel({ phase: 'preparing' });
	assert.match(html, /Preparing archive/);
	assert.match(html, /can take several minutes/);
});
