// SPDX-License-Identifier: MPL-2.0
// Real-Chromium driver for the OSS-245/OSS-185 named diagnostics fixture.
// Uses the repository's existing owned review-browser harness and a seeded,
// disposable Vite fixture: no live credentials, worlds, daemon or global cache.
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { launchReviewBrowser } from './browser.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const project = path.resolve(here, '../../web/carbon-panel');
const fixture = path.join(project, 'e2e', 'oss-245-diagnostics-reconnect');
const requireFrontend = createRequire(path.join(project, 'package.json'));
const { createServer } = await import(pathToFileURL(requireFrontend.resolve('vite')).href);
const { svelte } = await import(pathToFileURL(requireFrontend.resolve('@sveltejs/vite-plugin-svelte')).href);
const { default: tailwind } = await import(pathToFileURL(requireFrontend.resolve('@tailwindcss/vite')).href);

const scenarioIndex = process.argv.indexOf('--scenario');
const scenario = scenarioIndex >= 0 ? process.argv[scenarioIndex + 1] : 'all';
const scenarios = ['stale-listing', 'auth-fail', 'non-idempotent', 'recreation'];
if (scenario !== 'all' && !scenarios.includes(scenario)) {
	throw new Error(`Unknown OSS-245 scenario ${scenario}; expected one of ${scenarios.join(', ')}`);
}

const cache = await fs.mkdtemp(path.join(os.tmpdir(), 'carbon-oss245-browser-'));
let server;
let browser;
let metadata;

async function open(page, base) {
	await page.goto(base);
	await page.waitForFunction(
		() => !!globalThis.__oss245Ui && !!globalThis.__oss245Rpc && !!globalThis.__oss245Ws,
		null,
		{ timeout: 120000 }
	);
	await page.waitForFunction(() => globalThis.__oss245Rpc.counts().listings >= 1, null, {
		timeout: 15000
	});
}

async function run(page, base) {
	const logText = () => page.locator('[role="log"]').innerText();
	const bufferCount = () => page.getByText(/lines in viewed buffer/).innerText();
	const counts = () => page.evaluate(() => globalThis.__oss245Rpc.counts());
	const wsState = () => page.evaluate(() => globalThis.__oss245Ws.state());
	const flush = () => page.evaluate(() => globalThis.__oss245Ui.flush());

	if (scenario === 'all' || scenario === 'stale-listing') {
		await open(page, base);
		// The initial listing for A is deliberately deferred. Navigate to B
		// before it resolves, then let B resolve before A's stale response.
		await page.evaluate(() => globalThis.__oss245Ui.setServer('server-B'));
		await flush();
		await page.waitForFunction(() => globalThis.__oss245Rpc.counts().listings >= 2);
		await page.evaluate(() =>
			globalThis.__oss245Rpc.resolveListing(1, [{ message: 'B-LIVE-LINE', level: 2 }])
		);
		await page.waitForFunction(() =>
			document.querySelector('[role="log"]')?.textContent?.includes('B-LIVE-LINE')
		);
		await page.evaluate(() =>
			globalThis.__oss245Rpc.resolveListing(0, [
				{ message: 'A-STALE-LINE', level: 2 },
				{ message: 'A-SECOND-STALE-LINE', level: 2 }
			])
		);
		await flush();
		await page.waitForTimeout(250);
		const text = await logText();
		assert.ok(text.includes('B-LIVE-LINE'), 'the current server listing must stay rendered');
		assert.ok(!text.includes('A-STALE-LINE'), 'a stale A listing must not render on B');
		assert.equal(await bufferCount(), '1 lines in viewed buffer');
		console.log('PASS: stale A log listing neither renders nor saves into B (Chromium)');

		// A round trip A -> B -> A reuses the same server id, so an id-only
		// guard is not enough: the superseded A listing must still be dropped.
		await open(page, base);
		await page.evaluate(() => globalThis.__oss245Ui.setServer('server-B'));
		await flush();
		await page.waitForFunction(() => globalThis.__oss245Rpc.counts().listings >= 2);
		await page.evaluate(() => globalThis.__oss245Ui.setServer('server-A'));
		await flush();
		await page.waitForFunction(() => globalThis.__oss245Rpc.counts().listings >= 3);
		await page.evaluate(() =>
			globalThis.__oss245Rpc.resolveListing(2, [{ message: 'NEWER-A-LINE', level: 2 }])
		);
		await page.waitForFunction(() =>
			document.querySelector('[role="log"]')?.textContent?.includes('NEWER-A-LINE')
		);
		await page.evaluate(() =>
			globalThis.__oss245Rpc.resolveListing(0, [{ message: 'OLDER-STALE-LINE', level: 2 }])
		);
		await flush();
		await page.waitForTimeout(250);
		const refreshed = await logText();
		assert.ok(refreshed.includes('NEWER-A-LINE'), 'the newest listing must stay rendered');
		assert.ok(!refreshed.includes('OLDER-STALE-LINE'), 'a superseded listing must be dropped');
		console.log('PASS: superseded same-server listing cannot overwrite newer logs (Chromium)');
	}

	if (scenario === 'all' || scenario === 'auth-fail') {
		await open(page, base);
		await page.evaluate(() =>
			globalThis.__oss245Rpc.resolveListing(0, [{ message: 'BEFORE-AUTH-FAIL', level: 2 }])
		);
		await page.waitForFunction(() =>
			document.querySelector('[role="log"]')?.textContent?.includes('BEFORE-AUTH-FAIL')
		);
		await page.evaluate(() => globalThis.__oss245Ws.authFail());
		await flush();
		await page.getByText('SIGNED OUT').waitFor();
		assert.equal(await page.getByText('RECONNECTING').count(), 0, 'no reconnect spinner');
		assert.equal(
			await page.getByRole('button', { name: 'Reconnect WebSocket' }).count(),
			0,
			'no reconnect affordance after a credential rejection'
		);
		const before = await counts();
		const state = await wsState();
		assert.equal(state.reconnectCalls, 0);
		assert.equal(state.connectionState, 'signed-out');
		// Wait past the console's five-second fallback poll interval.
		await page.waitForTimeout(5500);
		assert.deepEqual(await counts(), before, 'signed-out console must not keep polling');
		console.log('PASS: AUTH_FAIL shows signed-out state with no spinner or retry (Chromium)');
	}

	if (scenario === 'all' || scenario === 'non-idempotent') {
		await open(page, base);
		await page.evaluate(() =>
			globalThis.__oss245Rpc.resolveListing(0, [{ message: 'CLEARABLE-LINE', level: 2 }])
		);
		await page.waitForFunction(() =>
			document.querySelector('[role="log"]')?.textContent?.includes('CLEARABLE-LINE')
		);
		await page.evaluate(() => globalThis.__oss245Rpc.failNextClear());
		await page.getByRole('button', { name: 'Clear log buffer' }).click();
		await page.getByRole('button', { name: 'Clear viewed buffer' }).click();
		await page.waitForFunction(() => globalThis.__oss245Rpc.counts().actions >= 1);
		await page.waitForTimeout(1000);
		assert.equal(
			(await counts()).actions,
			1,
			'a failed non-idempotent clear must not be blindly retried'
		);
		console.log('PASS: non-idempotent clear is dispatched exactly once on failure (Chromium)');
	}

	if (scenario === 'all' || scenario === 'recreation') {
		await open(page, base);
		await page.evaluate(() =>
			globalThis.__oss245Rpc.resolveListing(0, [{ message: 'BEFORE-RECREATE', level: 2 }])
		);
		await page.waitForFunction(() =>
			document.querySelector('[role="log"]')?.textContent?.includes('BEFORE-RECREATE')
		);
		const before = await wsState();
		await page.evaluate(() => globalThis.__oss245Ui.recreate());
		await flush();
		// Subscriptions are keyed by server identity, not container identity:
		// recreation must not drop the listener or clear the viewed buffer.
		const afterRecreate = await wsState();
		assert.deepEqual(afterRecreate.subscriptions, before.subscriptions);
		assert.equal(afterRecreate.logHandlers, before.logHandlers);
		await page.evaluate(() => globalThis.__oss245Ws.emitLogEntry('server-A', ['AFTER-RECREATE']));
		await flush();
		await page.waitForFunction(() =>
			document.querySelector('[role="log"]')?.textContent?.includes('AFTER-RECREATE')
		);
		const text = await logText();
		assert.ok(text.includes('BEFORE-RECREATE'), 'recreation must not clear the viewed buffer');
		const state = await wsState();
		assert.ok(state.subscriptions.some((item) => item.id === 'server-A'));
		assert.ok(state.logHandlers >= 1, 'the retained subscription must still own a handler');
		console.log('PASS: console subscription survives container recreation (Chromium)');
	}
}

try {
	server = await createServer({
		configFile: false,
		root: project,
		cacheDir: cache,
		publicDir: path.join(project, 'static'),
		resolve: {
			dedupe: ['svelte'],
			alias: [
				{ find: '$lib/api/rpc-client', replacement: path.join(fixture, 'rpc-stub.js') },
				{ find: '$lib/stores/websocket.svelte', replacement: path.join(fixture, 'ws-stub.svelte.ts') },
				{ find: /^\$lib\/components\/carbon$/, replacement: path.join(fixture, 'carbon.js') },
				{ find: '$lib', replacement: path.join(project, 'src/lib') }
			]
		},
		plugins: [
			svelte({ configFile: path.join(project, 'sveltekit.config.js') }),
			tailwind(),
			{
				name: 'oss245-diagnostics-reconnect',
				configureServer(vite) {
					vite.middlewares.use(async (req, res, next) => {
						if (req.url === '/favicon.ico') {
							res.statusCode = 204;
							res.end();
							return;
						}
						if (req.url !== '/oss245.html') return next();
						try {
							const entry = '/@fs/' + path.join(fixture, 'main.js').replaceAll('\\', '/');
							res.setHeader('Content-Type', 'text/html');
							res.end(
								await vite.transformIndexHtml(
									'/oss245.html',
									`<html><body><div id="app"></div><script type="module" src="${entry}"></script></body></html>`
								)
							);
						} catch (error) {
							next(error);
						}
					});
				}
			}
		],
		optimizeDeps: {
			noDiscovery: true,
			include: [
				'svelte',
				'svelte/store',
				'@bufbuild/protobuf',
				'@bufbuild/protobuf/codegenv2',
				'@bufbuild/protobuf/wkt',
				'@lucide/svelte',
				'@lucide/svelte/icons/grip-vertical',
				'ansi-to-html',
				'bits-ui',
				'paneforge',
				'svelte-sonner',
				'tailwind-merge'
			]
		},
		server: { host: '127.0.0.1', port: 0, hmr: false, fs: { allow: [project, fixture, cache] } }
	});
	await server.listen();
	const base = `http://127.0.0.1:${server.httpServer.address().port}/oss245.html`;

	const launched = await launchReviewBrowser({ headless: true });
	browser = launched.browser;
	metadata = launched.metadata;
	const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
	const errors = [];
	page.on('pageerror', (error) => errors.push(error.message));
	await run(page, base);
	assert.deepEqual(errors, [], 'the fixture page must not throw');
	await fs.writeFile(
		path.join(cache, '..', 'oss245-browser.json'),
		JSON.stringify({ scenario, ...metadata, errors }, null, 2)
	);
} finally {
	await browser?.close();
	await server?.close();
	await fs.rm(cache, { recursive: true, force: true });
}
