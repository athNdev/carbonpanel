// SPDX-License-Identifier: MPL-2.0
// Scoped real-browser QA for OSS-383. Mounts the real PlayerLookup and
// AddAllowlistEntry components behind fixture RPC/auth stubs. No live
// Minecraft server, controller, provider or Docker daemon is contacted.
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { launchReviewBrowser } from '../../review/browser.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const fixture = path.join(here, 'player-lookup');
const project = path.resolve(here, '../../../web/carbon-panel');
const requireFrontend = createRequire(path.join(project, 'package.json'));
const { createServer } = await import(pathToFileURL(requireFrontend.resolve('vite')).href);
const { svelte } = await import(pathToFileURL(requireFrontend.resolve('@sveltejs/vite-plugin-svelte')).href);
const { default: tailwind } = await import(pathToFileURL(requireFrontend.resolve('@tailwindcss/vite')).href);

const out = path.join(here, 'out', 'player-lookup');
await fs.mkdir(out, { recursive: true });
const cache = await fs.mkdtemp(path.join(os.tmpdir(), 'carbon-player-qa-'));
let server;
let browser;
const scenarios = [];
const errors = [];

function pass(name, detail = '') {
	scenarios.push({ name, status: 'PASS', detail });
	console.log(`PASS: ${name}${detail ? ` — ${detail}` : ''}`);
}

try {
	const viteConfig = {
		configFile: false,
		root: project,
		cacheDir: cache,
		publicDir: path.join(project, 'static'),
		resolve: {
			dedupe: ['svelte'],
			alias: [
				{ find: '$lib/api/rpc-client', replacement: path.join(fixture, 'rpc-stub.js') },
				{ find: '$lib/stores/auth', replacement: path.join(fixture, 'auth-stub.js') },
				{ find: /^\$lib\/components\/carbon$/, replacement: path.join(fixture, 'carbon.js') },
				{ find: '$lib', replacement: path.join(project, 'src/lib') }
			]
		},
		plugins: [
			svelte({ configFile: path.join(project, 'sveltekit.config.js') }),
			tailwind(),
			{
				name: 'player-lookup-qa',
				configureServer(vite) {
					vite.middlewares.use(async (req, res, next) => {
						if (req.url === '/favicon.ico') {
							res.statusCode = 204;
							res.end();
							return;
						}
						if (req.url !== '/player-lookup.html') return next();
						try {
							const entry =
								'/@fs/' + path.join(fixture, 'main.js').replaceAll('\\', '/');
							res.setHeader('Content-Type', 'text/html');
							res.end(
								await vite.transformIndexHtml(
									'/player-lookup.html',
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
		server: { host: '127.0.0.1', port: 0, hmr: false, fs: { allow: [project, fixture, cache] } },
		optimizeDeps: {
			include: ['svelte', '@bufbuild/protobuf', '@connectrpc/connect', 'svelte-sonner', '@lucide/svelte']
		}
	};
	server = await createServer(viteConfig);
	await server.listen();
	const { browser: launched, metadata } = await launchReviewBrowser();
	browser = launched;
	await fs.writeFile(path.join(out, 'browser.json'), JSON.stringify(metadata, null, 2));
	const base = `http://127.0.0.1:${server.httpServer.address().port}/player-lookup.html`;

	const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
	const page = await ctx.newPage();
	page.on('pageerror', (error) => errors.push(error.message));
	page.on('console', (msg) => {
		if (msg.type() === 'error' && !/favicon/i.test(msg.text())) errors.push(`console: ${msg.text()}`);
	});
	await page.goto(base, { waitUntil: 'networkidle' });
	await page.waitForFunction(() => Boolean(globalThis.__qa && globalThis.__qaAuth));

	const lookupInput = page.getByLabel('Exact UUID or username').first();
	const addInput = page.getByLabel('Exact UUID or username').nth(1);
	const lookupButton = page.getByRole('button', { name: 'Look up', exact: true });
	const addButton = page.getByRole('button', { name: 'Add to allowlist', exact: true });

	// 1. No-play-history lookup by exact username, repeated: read-only, no mutation.
	await page.evaluate(() => __qa.reset());
	await lookupInput.fill('UnseenPlayer');
	await lookupInput.press('Enter');
	await page.getByText('UnseenPlayer', { exact: true }).first().waitFor();
	let text = await page.locator('section[aria-labelledby="player-lookup-heading"]').innerText();
	assert.match(text, /FOUND/);
	assert.match(text, /069a79f4-44e9-4726-a5be-fca90e38aaf5/);
	assert.match(text, /No identity, roster entry or presence was created/i);
	await lookupButton.click();
	await lookupButton.click();
	await page.waitForFunction(() => __qa.calls().filter((c) => c.method === 'lookupPlayer').length === 3);
	let calls = await page.evaluate(() => __qa.calls());
	assert.equal(calls.length, 3, 'repeated lookups must not call a mutation');
	assert.equal(calls.every((c) => c.method === 'lookupPlayer'), true);
	pass('no-play-history username lookup is found, ephemeral, and repeated lookups do not mutate');

	// 2. Exact UUID path (same fixture, exact query forwarded).
	await page.evaluate(() => __qa.reset());
	await lookupInput.fill('069a79f444e94726a5befca90e38aaf5');
	await lookupButton.click();
	await page.waitForFunction(() => __qa.calls().length === 1);
	calls = await page.evaluate(() => __qa.calls());
	assert.equal(calls[0].query, '069a79f444e94726a5befca90e38aaf5');
	await page.screenshot({ path: path.join(out, 'desktop-found.png') });
	pass('exact UUID lookup forwards the undashed UUID unchanged');

	// 3. Authoritative not found — distinct from outage.
	await page.evaluate(() => __qa.set({ lookup: 'not_found' }));
	await page.locator('[data-scenario="not_found"]').click();
	await lookupInput.fill('Ghost');
	await lookupButton.click();
	await page.locator('section[aria-labelledby="player-lookup-heading"]').getByText(/not found/i).first().waitFor();
	text = await page.locator('section[aria-labelledby="player-lookup-heading"]').innerText();
	assert.match(text, /authoritatively reported no match/);
	assert.doesNotMatch(text, /UNAVAILABLE/);
	pass('authoritative not-found is distinct and creates no identity');

	// 4. Ambiguous.
	await page.locator('[data-scenario="ambiguous"]').click();
	await lookupInput.fill('SameName');
	await lookupButton.click();
	await page.locator('section[aria-labelledby="player-lookup-heading"]').getByText(/ambiguous/i).first().waitFor();
	text = await page.locator('section[aria-labelledby="player-lookup-heading"]').innerText();
	assert.match(text, /exact UUID/);
	await page.screenshot({ path: path.join(out, 'desktop-ambiguous.png') });
	pass('ambiguous state explains how to disambiguate');

	// 5. Provider outage is unavailable, never not found.
	await page.locator('[data-scenario="unavailable"]').click();
	await lookupInput.fill('UnseenPlayer');
	await lookupButton.click();
	await page.locator('section[aria-labelledby="player-lookup-heading"]').getByText(/unavailable/i).first().waitFor();
	text = await page.locator('section[aria-labelledby="player-lookup-heading"]').innerText();
	assert.match(text, /outage or timeout/);
	assert.doesNotMatch(text, /no match/);
	await page.screenshot({ path: path.join(out, 'desktop-outage.png') });
	pass('provider outage is UNAVAILABLE, never NOT FOUND');

	// 6. Absent permission hides/gates the lookup control.
	await page.locator('[data-perm="none"]').click();
	await page.getByText(/You need the global/).waitFor();
	assert.equal(await lookupButton.isDisabled(), true);
	assert.equal(await addButton.isDisabled(), true);
	pass('absent permission gates both lookup and allowlist-add controls');

	// 7. Authorized manual allowlist add, scoped to this server, with undo.
	await page.locator('[data-perm="granted"]').click();
	await page.evaluate(() => __qa.reset());
	await addInput.fill('FreshName');
	await addInput.press('Enter');
	await page.locator('section[aria-label="Add allowlist entry"]').getByText(/added/i).first().waitFor();
	await page.getByRole('button', { name: 'Undo', exact: true }).click();
	await page.waitForFunction(() => __qa.calls().some((c) => c.method === 'undoPlayerAction'));
	calls = await page.evaluate(() => __qa.calls());
	const add = calls.find((c) => c.method === 'setWhitelistEntry');
	const undo = calls.find((c) => c.method === 'undoPlayerAction');
	assert.equal(add.serverId, 'srv-1', 'manual add must be scoped to the host server');
	assert.equal(add.playerQuery, 'FreshName');
	assert.equal(add.remove, false);
	assert.equal(undo.serverId, 'srv-1');
	assert.equal(undo.undoToken, 'undo_fixture_1');
	pass('manual allowlist add is server-scoped and undoable through the journal');

	// 8. Failure and cross-server denial surface as errors, never false success.
	await page.locator('[data-whitelist="failure"]').click();
	await addInput.fill('FailName');
	await addButton.click();
	await page.getByText(/rcon unavailable/).waitFor();
	assert.equal(await page.locator('section[aria-label="Add allowlist entry"]').getByText(/added/i).count(), 0);
	await page.locator('[data-whitelist="denied"]').click();
	await addInput.fill('CrossServerName');
	await addButton.click();
	await page.getByText(/permission_denied/).waitFor();
	assert.equal(await page.locator('section[aria-label="Add allowlist entry"]').getByText(/added/i).count(), 0);
	pass('whitelist failure and cross-server denial surface errors with no false success');

	// 9. Out-of-order provider replies: only the current query's result wins.
	await page.locator('[data-scenario="race"]').click();
	await lookupInput.fill('OldQuery');
	await lookupInput.press('Enter');
	await page.waitForFunction(() => __qa.calls().filter((c) => c.method === 'lookupPlayer').length === 1);
	await lookupInput.fill('NewQuery');
	await lookupInput.press('Enter');
	await page.getByText('CurrentResult', { exact: true }).waitFor();
	await page.waitForTimeout(1000); // let the stale 700ms reply arrive
	const lookupSectionText = await page
		.locator('section[aria-labelledby="player-lookup-heading"]')
		.innerText();
	assert.doesNotMatch(lookupSectionText, /StaleResult/);
	assert.match(lookupSectionText, /CurrentResult/);
	calls = await page.evaluate(() => __qa.calls());
	assert.equal(calls.filter((c) => c.method === 'lookupPlayer').length, 2);
	assert.equal(calls.some((c) => c.method !== 'lookupPlayer'), false);
	pass('out-of-order provider replies discard stale results and never mutate state');

	// 10. Bedrock edition + capability refusal is explicit (Gamertag/XUID).
	await page.locator('[data-whitelist="bedrock_capability"]').click();
	await addInput.fill('SomeGamertag');
	await addButton.click();
	await page.getByText(/Gamertag\/XUID/).waitFor();
	assert.equal(
		await page.locator('section[aria-label="Add allowlist entry"]').getByText(/added/i).count(),
		0
	);
	pass('Bedrock capability refusal names Gamertag/XUID and shows no false success');

	// 11. Mobile viewport + keyboard reachability.
	await page.setViewportSize({ width: 390, height: 844 });
	await page.evaluate(() => __qa.reset());
	await page.locator('[data-scenario="found"]').click();
	await page.screenshot({ path: path.join(out, 'mobile-found.png'), fullPage: true });
	const overflow = await page.evaluate(() => ({
		scrollW: document.documentElement.scrollWidth,
		clientW: document.documentElement.clientWidth
	}));
	assert.ok(overflow.scrollW <= overflow.clientW + 1, `mobile horizontal overflow ${JSON.stringify(overflow)}`);
	await lookupInput.fill('UnseenPlayer');
	await lookupInput.focus();
	assert.equal(await lookupInput.evaluate((el) => document.activeElement === el), true);
	await lookupInput.press('Enter');
	await page.waitForFunction(() => __qa.calls().length === 1);
	pass('mobile 390px layout has no horizontal overflow and keyboard Enter submits');

	assert.deepEqual(errors, [], `browser/page errors: ${errors.join(' | ')}`);
	await fs.writeFile(
		path.join(out, 'results.json'),
		JSON.stringify({ scenarios, errors, overflow, calls }, null, 2)
	);
	console.log(`PASS: ${scenarios.length} scoped browser scenarios; 0 page errors; evidence in ${out}`);
} finally {
	await browser?.close();
	await server?.close();
	await fs.rm(cache, { recursive: true, force: true });
}
