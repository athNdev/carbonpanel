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
		downloading: false,
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

test('the download action is disabled while its preflight is in flight', async () => {
	const html = await renderPanel({ phase: 'ready', acknowledged: true, downloading: true });
	const download = html.match(/<button[^>]*data-testid="archive-download"[^>]*>/)?.[0] ?? '';
	assert.match(download, /\sdisabled(?:=""|\s|>)/);
	assert.match(download, /aria-busy="true"/);
});

test('the preparation phase reports a longer-running operation', async () => {
	const html = await renderPanel({ phase: 'preparing' });
	assert.match(html, /Preparing archive/);
	assert.match(html, /can take several minutes/);
});

function actionTag(html: string, testId: string): string {
	return html.match(new RegExp(`<button[^>]*data-testid="${testId}"[^>]*>`))?.[0] ?? '';
}

function classTokens(markup: string): string[] {
	return (markup.match(/class="([^"]*)"/)?.[1] ?? '').split(/\s+/).filter(Boolean);
}

test('export actions keep the 40px CarbonButton control geometry', async () => {
	const carbonButton = readFileSync(
		new URL('../../components/carbon/CarbonButton.svelte', import.meta.url),
		'utf8'
	);
	// The exact class sets the modal trigger renders; if CarbonButton moves, the
	// panel copy below must move with it.
	const mdSize = 'h-[var(--carbon-control-md)] px-4 text-sm';
	const primaryKind =
		'bg-primary text-primary-foreground hover:bg-[var(--button-primary-hover)] active:bg-[var(--button-primary-active)] border border-transparent';
	assert.ok(carbonButton.includes(mdSize), 'CarbonButton md size classes moved');
	assert.ok(carbonButton.includes(primaryKind), 'CarbonButton primary classes moved');

	const actions = {
		prepare: actionTag(await renderPanel(), 'archive-prepare'),
		download: actionTag(
			await renderPanel({ phase: 'ready', acknowledged: true }),
			'archive-download'
		),
		retry: actionTag(
			await renderPanel({ phase: 'error', error: 'fixture refusal' }),
			'archive-retry'
		)
	};
	for (const [name, markup] of Object.entries(actions)) {
		assert.ok(markup, `${name} action must render`);
		const tokens = classTokens(markup);
		assert.ok(
			tokens.includes('h-[var(--carbon-control-md)]'),
			`${name}: actions use the 40px Carbon control height`
		);
		assert.ok(
			!tokens.some((token) => /^py-/.test(token)),
			`${name}: actions must not hand-roll vertical padding`
		);
	}
	for (const name of ['prepare', 'download'] as const) {
		const tokens = classTokens(actions[name]);
		for (const token of `${mdSize} ${primaryKind}`.split(' ')) {
			assert.ok(tokens.includes(token), `${name}: missing CarbonButton token ${token}`);
		}
	}
	// Try again keeps its compact secondary/mono intent, only the height changes.
	const retryTokens = classTokens(actions.retry);
	assert.ok(retryTokens.includes('font-mono') && retryTokens.includes('uppercase'));
	assert.ok(retryTokens.includes('hover:bg-accent'));
});
