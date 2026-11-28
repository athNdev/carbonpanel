#!/usr/bin/env node
// SPDX-License-Identifier: MPL-2.0
//
// Real-Chromium keyboard/accessibility journeys over (1) the shared component
// fixture that mounts the actual Carbon components and (2) REAL APPLICATION
// ROUTES served from the SvelteKit PRODUCTION STATIC BUILD (the same assets the
// Go binary embeds) with ConnectRPC responses seeded at the network boundary.
// Both use the existing owned review browser (scripts/review/browser.mjs) and
// the locked Playwright package in this directory. An optional cross-engine
// smoke mode exercises the same packaged routes in Firefox or WebKit when those
// browsers are installed into the owned cache by CI. No claim is made about
// mobile engines, screen readers or 400% zoom; only public UI seams are driven.
//
// Usage: node a11y-check.mjs
//        node a11y-check.mjs --dashboard-telemetry-only
//        node a11y-check.mjs --engine=firefox   (cross-engine smoke)
//        node a11y-check.mjs --engine=webkit    (cross-engine smoke)
//   REVIEW_CHROME may point at an explicit owned Chromium executable.
//   The packaged-route half requires `bun run build` in web/carbon-panel first.

import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
import { verifyDashboardTelemetry } from './dashboard-telemetry.mjs';
import { launchReviewBrowser, reviewBrowserPath, reviewDriver } from '../../review/browser.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const repo = resolve(here, '../../..');
const webDir = resolve(repo, 'web/carbon-panel');

function requestedEngine() {
	const arg = process.argv.find((value) => value.startsWith('--engine='));
	return arg ? arg.slice('--engine='.length) : 'chromium';
}

async function runShellJourney(browser, base) {
	const root = base.replace(/\/$/, '');
	const context = await browser.newContext({
		viewport: { width: 1440, height: 900 }, colorScheme: 'dark', reducedMotion: 'reduce'
	});
	try {
		const page = await context.newPage();
		await seedPackagedRoutes(page, { servers: 'large', detail: 'populated', narrowSettings: true }, 'anonymous');
		const openPreferences = async () => {
			await page.locator('header button[aria-haspopup="menu"]').click();
			await page.getByRole('menuitem', { name: 'Preferences' }).click();
		};
		const palette = (mode) => page.waitForFunction((mode) => {
			const html = document.documentElement;
			return html.dataset.carbonTheme === mode && html.classList.contains('dark') === (mode === 'dark') &&
				getComputedStyle(document.body).backgroundColor === (mode === 'dark' ? 'rgb(22, 22, 22)' : 'rgb(255, 255, 255)');
		}, mode);
		await page.goto(root + '/');
		await page.getByRole('heading', { name: 'Cluster Overview' }).waitFor();
		await check('shell: explicit theme, persistence and live system changes', async () => {
			const select = page.getByLabel('Theme', { exact: true });
			await openPreferences();
			assert.equal(await select.inputValue(), 'system');
			await palette('dark');
			await select.selectOption('light');
			await palette('light');
			await page.reload();
			await palette('light');
			await openPreferences();
			assert.equal(await select.inputValue(), 'light');
			await select.selectOption('dark');
			await page.emulateMedia({ colorScheme: 'light' });
			await palette('dark');
			await select.selectOption('system');
			await palette('light');
			await page.emulateMedia({ colorScheme: 'dark' });
			await palette('dark');
			assert.equal(await page.evaluate(() => localStorage.getItem('carbon-theme')), 'system');
			await page.getByRole('button', { name: 'Done' }).click();
		});
		await check('shell: expanded navigation summarizes all registered hosts', async () => {
			await page.locator('#carbon-sidenav').getByText('2 of 2 online').waitFor();
		});
		await check('shell: a refused node inventory is shown as unavailable', async () => {
			const nodes = '**/carbonpanel.v1.NodeService/ListNodes';
			await page.route(nodes, (route) => route.fulfill({
				status: 403, contentType: 'application/json',
				body: JSON.stringify({ code: 'permission_denied', message: 'Node inventory unavailable' })
			}));
			try {
				await page.reload();
				await page.locator('#carbon-sidenav').getByText('Node status unavailable').waitFor();
			} finally {
				await page.unroute(nodes);
			}
		});
		for (const [width, height] of [[1440, 900], [1024, 640], [768, 450], [360, 640]]) {
			await page.setViewportSize({ width, height });
			await page.goto(root + '/');
			await page.getByTestId('health-nodes').waitFor();
			await check(`dashboard @${width}x${height}: health card and bottom gutter are reachable`, async () => {
				const bounds = await page.evaluate(() => {
					const main = document.querySelector('main');
					main.scrollTop = main.scrollHeight;
					const grid = document.querySelector('[data-testid="health-nodes"]').parentElement;
					const card = grid.parentElement.getBoundingClientRect();
					const contentFits = [...grid.children].every((cell) => {
						const [label, value] = [...cell.children].map((child) => child.getBoundingClientRect());
						return label.right <= value.left && value.right <= cell.getBoundingClientRect().right;
					});
					return { top: card.top, bottom: card.bottom, padding: parseFloat(getComputedStyle(main).paddingBottom), contentFits };
				});
				assert.ok(bounds.top >= 48 && bounds.bottom <= height - bounds.padding + 1, JSON.stringify(bounds));
				assert.ok(bounds.contentFits, 'System health labels and values must fit each cell');
				if (process.env.UI_PASS_SHOTS)
					await page.screenshot({ path: `${process.env.UI_PASS_SHOTS}/system-health-${width}.png`, animations: 'disabled' });
				return bounds;
			});
			await page.goto(root + '/servers');
			await page.getByRole('button', { name: 'Tile View' }).click();
			await check(`server tiles @${width}: long names wrap within the card`, async () => {
				const title = page.getByRole('link', { name: LARGE_FLEET[0].name, exact: true });
				await title.waitFor();
				const metrics = await title.evaluate((element) => ({
					width: element.clientWidth, scrollWidth: element.scrollWidth,
					height: element.clientHeight, lineHeight: parseFloat(getComputedStyle(element).lineHeight)
				}));
				assert.ok(metrics.width >= 185 && metrics.scrollWidth <= metrics.width + 1 && metrics.height >= 2 * metrics.lineHeight - 1, JSON.stringify(metrics));
				if (process.env.UI_PASS_SHOTS)
					await page.screenshot({ path: `${process.env.UI_PASS_SHOTS}/server-tiles-${width}.png`, animations: 'disabled' });
				return metrics;
			});
		}
		await check('server detail: resource meters paint a measured RAM and CPU sample', async () => {
			await page.setViewportSize({ width: 1440, height: 900 });
			const measured = { ...FLEET[0], containerId: 'fixture-container', memoryUsage: '1024', cpuPercent: 35 };
			await page.route('**/carbonpanel.v1.ServerService/GetServer', (route) => route.fulfill({
				contentType: 'application/json', body: JSON.stringify({ server: measured })
			}));
			await page.goto(root + '/servers/srv-1');
			// The route can replace its initial DOM during hydration. Read the complete
			// painted sample in one browser turn instead of sampling after a locator wait.
			const sample = await page.waitForFunction(() => {
				const tracks = [...document.querySelectorAll('.telemetry-track')];
				if (tracks.length !== 2) return;
				const bars = tracks.map((track) => {
					const fill = track.firstElementChild;
					const box = track.getBoundingClientRect();
					return {
						height: box.height,
						percent: Math.round(100 * fill.getBoundingClientRect().width / box.width),
						visibleFill: getComputedStyle(fill).backgroundColor !== getComputedStyle(track).backgroundColor
					};
				});
				if (bars[0].percent === 50 && bars[1].percent === 35) return bars;
			}, null, { timeout: 10000 });
			const geometry = await sample.jsonValue();
			await sample.dispose();
			assert.deepEqual(geometry.map((bar) => bar.percent), [50, 35]);
			assert.ok(geometry.every((bar) => bar.height >= 10 && bar.visibleFill));
			if (process.env.UI_PASS_SHOTS)
				await page.screenshot({ path: `${process.env.UI_PASS_SHOTS}/server-resource-meters.png`, animations: 'disabled' });
			return geometry;
		});
	} finally {
		await context.close();
	}
}

async function runDashboardTelemetry(browser, base) {
	const root = base.replace(/\/$/, '');
	for (const width of [360, 1024]) {
		const context = await browser.newContext({
			viewport: { width, height: 900 },
			reducedMotion: 'reduce'
		});
		try {
			const page = await context.newPage();
			const state = { servers: 'populated', detail: 'populated' };
			await seedPackagedRoutes(page, state, 'anonymous');
			await check(`dashboard telemetry @${width}: missing, measured zero and partial samples`, () =>
				verifyDashboardTelemetry(page, root, state)
			);
		} finally {
			await context.close();
		}
	}
}

async function launchEngine(engine) {
	if (engine === 'chromium') return launchReviewBrowser();
	if (engine !== 'firefox' && engine !== 'webkit') {
		throw new Error(`Unsupported engine '${engine}' (chromium, firefox or webkit)`);
	}
	process.env.PLAYWRIGHT_BROWSERS_PATH = reviewBrowserPath();
	// The shared reviewDriver() validates the pinned driver version and owns the
	// cache path, but intentionally exposes only Chromium. Load the same locked
	// package directly for the optional engines instead of broadening it.
	const { version } = reviewDriver();
	const playwright = createRequire(new URL('./package.json', import.meta.url))('playwright');
	const browser = await playwright[engine].launch({ headless: true });
	return {
		browser,
		metadata: {
			driver: 'playwright',
			driverVersion: version,
			browserVersion: browser.version(),
			engine,
			browserCache: reviewBrowserPath()
		}
	};
}

const checks = [];
let failures = 0;
async function check(name, fn) {
	try {
		const detail = await fn();
		checks.push({ name, status: 'passed', detail: detail ?? null });
		console.log(`PASS ${name}`);
	} catch (error) {
		failures += 1;
		checks.push({ name, status: 'failed', detail: String(error?.message ?? error) });
		console.error(`FAIL ${name}: ${error?.message ?? error}`);
	}
}

function watchServer(child, marker, label, timeoutMs = 30000) {
	return new Promise((resolveUrl, reject) => {
		const timer = setTimeout(() => reject(new Error(`${label} did not start in ${timeoutMs / 1000}s`)), timeoutMs);
		let stderr = '';
		child.stderr.on('data', (chunk) => (stderr += chunk.toString()));
		child.stdout.on('data', (chunk) => {
			const match = chunk.toString().match(marker);
			if (match) {
				clearTimeout(timer);
				resolveUrl(match[1].trim());
			}
		});
		child.once('exit', (code) => {
			clearTimeout(timer);
			reject(new Error(`${label} exited ${code}: ${stderr.slice(0, 500)}`));
		});
	});
}

function startFixture() {
	const child = spawn('bun', ['test/a11y-fixture/serve.mjs'], {
		cwd: webDir,
		stdio: ['ignore', 'pipe', 'pipe']
	});
	return { child, url: watchServer(child, /A11Y_FIXTURE_URL=(\S+)/, 'Fixture server') };
}

function startPackagedServer() {
	const child = spawn('node', ['test/a11y-routes/serve.mjs'], {
		cwd: webDir,
		stdio: ['ignore', 'pipe', 'pipe']
	});
	return { child, url: watchServer(child, /A11Y_ROUTES_URL=(\S+)/, 'Packaged route server') };
}

// Public-seam seeds. Only network responses at the external API boundary are
// stubbed; no source module, store or component internal is imported or driven.
const LARGE_FLEET = Array.from({ length: 8 }, (_, index) => ({
	id: `srv-large-${index}`,
	name: `Very long fixture server name ${index} for responsive dashboard verification`,
	status: index % 3 === 0 ? 'SERVER_STATUS_ERROR' : index % 3 === 1 ? 'SERVER_STATUS_RUNNING' : 'SERVER_STATUS_STOPPED',
	modLoader: 'MOD_LOADER_VANILLA',
	version: '1.21.1',
	port: 25570 + index,
	memory: 65536,
	maxPlayers: 999
}));

const FLEET = [
	{
		id: 'srv-1',
		name: 'Alpha Server',
		status: 'SERVER_STATUS_RUNNING',
		modLoader: 'MOD_LOADER_VANILLA',
		version: '1.21.1',
		port: 25565,
		memory: 2048,
		maxPlayers: 20
	},
	{
		id: 'srv-2',
		name: 'Beta Server',
		status: 'SERVER_STATUS_STOPPED',
		modLoader: 'MOD_LOADER_FABRIC',
		version: '1.20.4',
		port: 25566,
		memory: 1024,
		maxPlayers: 10
	}
];

// The shell focus prefix is stable; the sidebar nav collapses below 768, so
// only its first three stops are asserted at the narrowest viewport.
const SHELL_FOCUS = [
	'Skip to content',
	'Toggle Navigation',
	'Carbon Panel',
	'A1 A11y Reviewer',
	'Dashboard',
	/^Servers\b/,
	'Create server'
];
// Seeded roster, library and view rows. Timestamps are RFC3339 protojson, and
// enums use their schema names, so the real generated client decodes them.
const ISO = (seconds) => new Date(seconds * 1000).toISOString();
const PLAYERS = [
	{
		ref: {
			identityKey: 'seed-solarfox',
			name: 'SolarFox',
			uuid: '00000000-0000-4000-8000-000000000001'
		},
		serverId: 'srv-1',
		serverName: 'Alpha Server',
		presence: 'PLAYER_PRESENCE_ONLINE',
		presenceObservation: {
			source: 'PLAYER_DATA_SOURCE_PRESENCE',
			observedAt: ISO(1_700_000_000),
			confidence: 'PLAYER_CONFIDENCE_DETECTED'
		},
		lastSeen: ISO(1_700_000_000)
	}
];
const WORLD_ENTRIES = [
	{
		id: 'world-1',
		name: 'Seeded Overworld',
		edition: 'JAVA',
		family: 'overworld',
		currentRevisionId: 'rev-1',
		revisionCount: 2,
		createdAt: ISO(1_700_000_000),
		updatedAt: ISO(1_700_000_000)
	}
];
// Representative existing modpack rows, seeded only at the SearchModpacks
// network boundary. Field names are the generated client's camelCase keys and
// the JSON-array columns are valid JSON, so the real page renders them.
const MODPACKS = [
	{
		id: 'seed-pack-1',
		indexerId: 'seed-modrinth',
		indexer: 'modrinth',
		name: 'Seeded Fabric Pack',
		slug: 'seeded-fabric-pack',
		summary: 'Representative catalog entry for the recurring modpack journey',
		description: 'Seeded at the RPC boundary; no network indexer is contacted.',
		logoUrl: '',
		websiteUrl: '',
		downloadCount: 1234,
		categories: '["fabric"]',
		gameVersions: '["1.21.1"]',
		modLoaders: '["fabric"]',
		latestFileId: 'seed-file-1',
		mcVersion: '1.21.1',
		javaVersion: 21,
		dockerImage: '',
		recommendedRam: 2048,
		isFavorited: false,
		indexError: ''
	},
	{
		id: 'seed-pack-2',
		indexerId: 'seed-modrinth',
		indexer: 'modrinth',
		name: 'Seeded Forge Pack',
		slug: 'seeded-forge-pack',
		summary: 'Second representative catalog entry',
		description: 'Seeded at the RPC boundary; no network indexer is contacted.',
		logoUrl: '',
		websiteUrl: '',
		downloadCount: 987,
		categories: '["forge"]',
		gameVersions: '["1.20.4"]',
		modLoaders: '["forge"]',
		latestFileId: 'seed-file-2',
		mcVersion: '1.20.4',
		javaVersion: 17,
		dockerImage: '',
		recommendedRam: 2048,
		isFavorited: false,
		indexError: ''
	}
];

const WORLD_TARGETS = [
	{
		serverId: 'srv-1',
		worldRef: { kind: 'KIND_SERVER_LOCAL', serverId: 'srv-1', levelName: 'seed-world' },
		name: 'Alpha Overworld',
		dimension: 'overworld',
		rendererType: 'RENDERER_TYPE_BLUEMAP',
		rendererState: 'RENDERER_STATE_READY',
		lastRender: ISO(1_700_000_000),
		renderer: {
			type: 'RENDERER_TYPE_BLUEMAP',
			version: '2.5.0',
			capabilities: ['map.2d', 'markers'],
			webBasePath: '/maps/alpha'
		},
		nodeId: 'local'
	}
];

const FOCUSABLE = 'button:not([disabled]), [role="radio"], input:not([disabled]), a[href]';
const themeBackgrounds = {};

function matches(actual, expected) {
	return expected instanceof RegExp ? expected.test(actual) : actual === expected;
}

const SETTINGS_NODE = 'west-coast-minecraft-replica-primary-maintenance-target';
const SETTINGS_CATEGORIES = [
	{
		name: 'Game',
		properties: [
			{ key: 'motd', label: 'Message of the day', value: 'Welcome', type: 'text' },
			{
				key: 'difficulty',
				label: 'Difficulty',
				value: 'normal',
				type: 'select',
				options: ['easy', 'normal', 'hard']
			}
		]
	},
	{
		name: 'Networking',
		properties: [{ key: 'port', label: 'Game port', envVar: 'SERVER_PORT', description: 'TCP listener for the game.', value: '25565', type: 'number' }]
	}
];

const LOOKUP_UUID = 'c3b9d17a-561d-4086-99d3-400bac225283';
// Synthetic 16x32 portrait. The provider and atlas projection have separate Go coverage.
const LOOKUP_PNG = 'iVBORw0KGgoAAAANSUhEUgAAABAAAAAgCAYAAAAbifjMAAAAOUlEQVR4nGNgQAPXFpb8x4fR1WOAUQOGpAGqya//U4JHDRg1YLgYgA7kNK3/48MEc+OoAaMGDE0DAMemII6Cz5U4AAAAAElFTkSuQmCC';

async function seedPackagedRoutes(page, state, authMode) {
	await page.route(/\/carbonpanel\.v1\.[^/]+\/[^/?]+/, async (route) => {
		const method = new URL(route.request().url()).pathname.split('/').at(-1);
		const reply = (body, status = 200) =>
			route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
		const fail = (message) =>
			reply({ code: 'unavailable', message }, 503);
		if (method === 'GetAuthStatus') {
			return reply(
				authMode === 'login-failure'
					? {
							localAuthEnabled: true,
							oidcEnabled: false,
							firstUserSetup: false,
							allowRegistration: false,
							anonymousAccessEnabled: false
						}
					: { localAuthEnabled: false, oidcEnabled: false, anonymousAccessEnabled: true, firstUserSetup: false }
			);
		}
		if (method === 'GetCurrentUser') {
			return reply(
				authMode === 'login-failure'
					? {}
					: {
							user: { id: 'a11y', username: 'A11y Reviewer' },
							permissions: state.permissions ?? [{ resource: '*', action: '*', objectId: '*' }]
						}
			);
		}
		if (method === 'Login' && authMode === 'login-failure') {
			return reply({ code: 'unauthenticated', message: 'Invalid username or password' }, 401);
		}
		if (method === 'ListServers') {
			if (Array.isArray(state.servers)) return reply({ servers: state.servers });
			if (state.servers === 'error') return fail('Fixture servers unavailable');
			if (state.servers === 'large') return reply({ servers: LARGE_FLEET });
			return reply({ servers: state.servers === 'empty' ? [] : FLEET });
		}
		if (method === 'GetServer') {
			if (state.detail === 'error') return fail('Fixture server unavailable');
			// A held refresh lets a check deliver a fresh Server object for the
			// same id at a deterministic point (the real inventory polls every
			// 5s). The spread always returns a new object, like the live store.
			const control = state.detailRefresh;
			if (control?.hold) {
				control.count = (control.count ?? 0) + 1;
				control.arrived?.();
				await control.release;
			}
			const server = FLEET.find((server) => server.id === 'srv-1');
			return reply({
				server: {
					...server,
					...(control?.marker ? { name: control.marker } : {}),
					...(state.narrowSettings
						? { nodeId: 'local', containerId: 'fixture-container', mcVersion: '1.21.1' }
						: {})
				}
			});
		}
		if (state.narrowSettings) {
			if (method === 'ListNodes') {
				if (state.nodesReady) await state.nodesReady;
				return reply({
					nodes: [
						{ id: 'local', name: 'Local Controller', isLocal: true, enabled: true, status: 1 },
						{ id: 'target', name: SETTINGS_NODE, enabled: true, status: 1, availableMemoryMb: '8192' }
					]
				});
			}
			if (method === 'GetGlobalSettings') return reply({ categories: SETTINGS_CATEGORIES });
			if (method === 'GetServerConfig') return reply({ categories: SETTINGS_CATEGORIES });
			if (method === 'UpdateServerConfig') {
				state.configRequest = route.request().postDataJSON();
				return reply({ categories: SETTINGS_CATEGORIES });
			}
			if (method === 'UpdateGlobalSettings') {
				const { updates } = route.request().postDataJSON();
				state.savedMotd = updates?.motd;
				const categories = SETTINGS_CATEGORIES.map((category) => ({
					...category,
					properties: category.properties.map((field) => ({
						...field,
						value: updates[field.key] ?? field.value
					}))
				}));
				return reply({ categories });
			}
			if (method === 'GetAuthConfig')
				return reply({ localAuthEnabled: true, sessionTimeout: 86400, oidcEnabled: false });
			if (method === 'GetMinecraftVersions')
				return reply({ versions: [{ id: '1.21.1', type: 'release' }], latest: '1.21.1' });
			if (method === 'GetModLoaders') return reply({ modloaders: [] });
		}
		if (method === 'ListFiles') {
			state.fileListings = (state.fileListings ?? 0) + 1;
			return reply({ files: [] });
		}
		if (method === 'ListNodes') return reply({ nodes: [] });
		if (method === 'ListPlayers') {
			return reply({ players: state.lookupQueries ? [] : PLAYERS, sourceDetail: 'Synthetic a11y route fixture' });
		}
		if (method === 'SetWhitelistEntry') {
			assert.equal(route.request().postDataJSON().serverId, 'srv-1');
			return reply({ outcome: { results: [{
				success: true, message: 'Review guest added.', undoSupported: true, undoToken: 'fixture-undo'
			}] } });
		}
		if (method === 'UndoPlayerAction') {
			const request = route.request().postDataJSON();
			assert.equal(request.serverId, 'srv-1');
			assert.equal(request.undoToken, 'fixture-undo');
			return reply({ outcome: { results: [{ success: true }] } });
		}
		if (method === 'LookupPlayer') {
			const request = route.request().postDataJSON();
			state.lookupQueries?.push(request);
			if (request?.query === 'FixtureMissing') {
				return reply({ status: 'PLAYER_LOOKUP_STATUS_NOT_FOUND' });
			}
			if (request?.query === 'FixtureObserved') {
				return reply({
					status: 'PLAYER_LOOKUP_STATUS_FOUND',
					name: request.query,
					uuid: LOOKUP_UUID,
					observedIdentityKey: 'fixture-identity',
					observation: {
						source: 'PLAYER_DATA_SOURCE_PANEL_DB',
						observedAt: '2039-01-01T00:00:00Z'
					}
				});
			}
			return reply({
				status: 'PLAYER_LOOKUP_STATUS_FOUND',
				name: request?.query,
				uuid: LOOKUP_UUID,
				observation: { source: 'PLAYER_DATA_SOURCE_IDENTITY_SERVICE', observedAt: '2026-11-20T03:30:00Z' },
				...(request?.query === 'FixtureSkin'
					? { skin: { data: LOOKUP_PNG, contentType: 'image/png' } }
					: {})
			});
		}
		if (method === 'ListPlayerSessions' && state.lookupSessionRequests) {
			const request = route.request().postDataJSON();
			state.lookupSessionRequests.push(request);
			if (request?.serverId !== 'fixture-presence-server' || state.lookupSessionDenied) {
				return reply({ code: 'permission_denied', message: 'No presence grant' }, 403);
			}
			return reply({
				sessions: [
					{ identityKey: 'fixture-identity', serverId: 'fixture-presence-server', joinedAt: '2003-06-01T10:30:00Z' },
					{ identityKey: 'fixture-identity', serverId: 'fixture-hidden-server', joinedAt: '2039-01-01T00:00:00Z' }
				]
			});
		}
		if (method === 'ListWorldEntries') return reply({ entries: WORLD_ENTRIES });
		if (method === 'ListWorldViewTargets') return reply({ targets: WORLD_TARGETS });
		if (method === 'GetWorldViewStatus') return reply({ target: WORLD_TARGETS[0] });
		if (method === 'GetMinecraftVersions') return reply({ versions: ['1.21.1', '1.20.4'], latest: '1.21.1' });
		if (method === 'GetModLoaders') return reply({ modloaders: ['vanilla', 'fabric'] });
		if (method === 'GetDockerImages') return reply({ images: [] });
		if (method === 'GetNextAvailablePort') return reply({ port: 25567 });
		if (method === 'GetIndexerStatus') {
			return reply({ indexersAvailable: { modrinth: true }, modpacksByIndexer: { modrinth: 2 } });
		}
		if (method === 'ListFavorites') return reply({ modpacks: [] });
		if (method === 'SearchModpacks') {
			if (state.modpacks === 'refused') return fail('Fixture modpack search refused');
			return reply({ modpacks: MODPACKS, total: MODPACKS.length, page: 1, pageSize: 20 });
		}
		if (method === 'GetModpackFiles') {
			return reply({
				files: [{
					id: 'seed-file-1',
					displayName: 'Fabric pack 1.0',
					fileName: 'fabric-pack.mrpack',
					fileLength: '524288',
					gameVersions: ['1.21.1'],
					releaseType: 'release'
				}]
			});
		}
		if (method === 'GetGlobalSettings') return reply({ categories: [] });
		return reply({});
	});
	// OIDC exchange and REST downloads are not part of these journeys.
	await page.route('**/api/v1/**', (route) =>
		route.fulfill({ status: 200, contentType: 'application/json', body: '{}' })
	);
}

async function focusNames(page, count) {
	const names = [];
	for (let i = 0; i < count; i += 1) {
		await page.keyboard.press('Tab');
		names.push(
			await page.evaluate(() => {
				const el = document.activeElement;
				if (!el) return '';
				return (el.getAttribute('aria-label') || el.textContent || '').replace(/\s+/g, ' ').trim();
			})
		);
	}
	return names;
}

async function runComponentJourney(browser, base) {
	for (const width of [1440, 768, 360]) {
		const page = await browser.newPage({ viewport: { width, height: 900 } });
		try {
			// Vite's HMR socket keeps `load` pending in this pinned Chromium;
			// the explicit component wait below is the real readiness signal.
			await page.goto(base, { waitUntil: 'domcontentloaded' });
			await page.locator('[role="tablist"]').waitFor();

			await check(`reflow ${width}: no horizontal page scroll`, async () => {
				const metrics = await page.evaluate(() => ({
					scrollWidth: document.documentElement.scrollWidth,
					innerWidth: window.innerWidth
				}));
				if (metrics.scrollWidth > metrics.innerWidth + 1) {
					throw new Error(`document scrollWidth ${metrics.scrollWidth} > viewport ${metrics.innerWidth}`);
				}
				return metrics;
			});

			await check(`focus order ${width}: primary route controls are keyboard reachable`, async () => {
				await page.locator('body').click({ position: { x: 2, y: 2 } });
				const order = [];
				for (let i = 0; i < 5; i += 1) {
					await page.keyboard.press('Tab');
					order.push(
						await page.evaluate(() => {
							const el = document.activeElement;
							return el?.id || el?.getAttribute('role') || el?.tagName || null;
						})
					);
				}
				const expected = [
					'crumb-servers',
					'header-action',
					'nav-servers',
					'a11y-tab-overview',
					'open-dialog'
				];
				if (JSON.stringify(order) !== JSON.stringify(expected)) {
					throw new Error(`focus order was ${JSON.stringify(order)}, expected ${JSON.stringify(expected)}`);
				}
				return order;
			});

			await check(`visible focus ${width}: keyboard focus has a real focus ring`, async () => {
				await page.locator('body').click({ position: { x: 2, y: 2 } });
				await page.keyboard.press('Tab');
				const style = await page.evaluate(() => {
					const el = document.activeElement;
					const computed = getComputedStyle(el);
					return {
						id: el?.id,
						outlineStyle: computed.outlineStyle,
						outlineWidth: computed.outlineWidth
					};
				});
				if (style.outlineStyle === 'none' || parseFloat(style.outlineWidth) < 2) {
					throw new Error(`no visible focus on ${style.id}: ${JSON.stringify(style)}`);
				}
				return style;
			});

			await check(`dialog ${width}: semantics, focus trap and Escape`, async () => {
				await page.locator('#open-dialog').click();
				const dialog = page.locator('[role="dialog"]');
				await dialog.waitFor();
				const semantics = await dialog.evaluate((el) => ({
					modal: el.getAttribute('aria-modal'),
					labelledBy: el.getAttribute('aria-labelledby'),
					label: document.getElementById(el.getAttribute('aria-labelledby') ?? '')?.textContent?.trim()
				}));
				if (semantics.modal !== 'true' || !semantics.labelledBy || !semantics.label) {
					throw new Error(`dialog semantics incomplete: ${JSON.stringify(semantics)}`);
				}
				// CarbonModal focuses its first control on a timer; poll the real
				// state instead of relying on a fixed sleep under CI load.
				await page.waitForFunction(
					() => {
						const el = document.querySelector('[role="dialog"]');
						return Boolean(el && el.contains(document.activeElement));
					},
					null,
					{ timeout: 5000 }
				);

				const buttons = dialog.locator('button:not([disabled])');
				await buttons.last().focus();
				await page.keyboard.press('Tab');
				const wrapped = await dialog.evaluate((el) => {
					const focusable = [...el.querySelectorAll('button:not([disabled])')];
					return document.activeElement === focusable[0];
				});
				if (!wrapped) throw new Error('Tab did not wrap to the first dialog control');

				await page.keyboard.press('Escape');
				await dialog.waitFor({ state: 'detached' });
				await page.waitForFunction(
					() => document.activeElement?.id === 'open-dialog',
					null,
					{ timeout: 5000 }
				);
				const restored = await page.evaluate(() => document.activeElement?.id);
				if (restored !== 'open-dialog') {
					throw new Error(`focus was not restored to the trigger (got ${restored})`);
				}
				return { ...semantics, restored };
			});

			await check(`tabs ${width}: roles, names and arrow-key navigation`, async () => {
				const semantics = await page.evaluate(() => {
					const list = document.querySelector('[role="tablist"]');
					const tabs = [...document.querySelectorAll('[role="tab"]')];
					return {
						label: list?.getAttribute('aria-label'),
						selected: tabs.map((tab) => tab.getAttribute('aria-selected')),
						ids: tabs.map((tab) => tab.id)
					};
				});
				if (semantics.label !== 'Sections' || semantics.selected[0] !== 'true') {
					throw new Error(`tab semantics incomplete: ${JSON.stringify(semantics)}`);
				}
				await page.locator('#a11y-tab-overview').focus();
				await page.keyboard.press('ArrowRight');
				const focused = await page.evaluate(() => document.activeElement?.id);
				if (focused !== 'a11y-tab-files') {
					throw new Error(`ArrowRight did not move tab focus (got ${focused})`);
				}
				const after = await page.evaluate(() =>
					[...document.querySelectorAll('[role="tab"]')].map((tab) =>
						tab.getAttribute('aria-selected')
					)
				);
				if (after[1] !== 'true' || after[0] !== 'false') {
					throw new Error(`ArrowRight did not move tab selection: ${JSON.stringify(after)}`);
				}
				return { ...semantics, focused, selectedAfter: after };
			});

			await check(`states ${width}: empty, populated and error are exposed`, async () => {
				const populated = await page.locator('.update-batch').isVisible();
				if (!populated) throw new Error('populated staged batch is not visible');
				await page.locator('#toggle-state').click();
				const alert = page.locator('[role="alert"]');
				await alert.waitFor();
				const text = (await alert.textContent()) ?? '';
				if (!text.includes('Failed to load server')) {
					throw new Error(`error state text missing: ${text}`);
				}
				await page.locator('#toggle-state').click();
				await alert.waitFor({ state: 'detached' });
				if (!(await page.locator('.update-batch').isVisible())) {
					throw new Error('empty/populated state did not return');
				}
				return { populated: true, error: text.trim().slice(0, 80) };
			});
		} finally {
			await page.close();
		}
	}
}

const PACKAGED_ROUTES = [
	{
		path: '/',
		h1: 'Cluster Overview',
		controls: [
			['heading', 'Cluster Overview'],
			['button', 'Refresh'],
			['link', 'Create Server']
		]
	},
	{
		path: '/players',
		h1: 'Players',
		controls: [
			['heading', 'Players'],
			['textbox', 'Minecraft username or UUID'],
			['textbox', 'Search players'],
			['button', 'Look up'],
			['button', 'SolarFox']
		]
	},
	{
		path: '/library',
		h1: 'World Library',
		controls: [
			['heading', 'World Library'],
			['textbox', 'Search world library'],
			['combobox', 'Edition'],
			['button', 'Reset filters'],
			['link', 'Seeded Overworld']
		]
	},
	{
		path: '/worlds',
		h1: 'World View',
		controls: [
			['heading', 'World View'],
			['textbox', 'Search maps'],
			['combobox', 'Server'],
			['combobox', 'Map status'],
			['button', 'Refresh'],
			['link', 'Open map for Alpha Overworld on Alpha Server']
		]
	},
	{
		path: '/servers',
		h1: 'Servers',
		controls: [
			['searchbox', 'Search servers'],
			['button', 'All (2)'],
			['button', 'Table View'],
			['link', 'Alpha Server']
		]
	},
	{
		path: '/servers/new',
		h1: 'Create New Server',
		controls: [
			['radio', /^Manual Configuration/],
			['link', 'Back to Servers'],
			['button', 'Next Step']
		]
	},
	{
		path: '/servers/srv-1',
		h1: 'Alpha Server',
		controls: [
			['tablist', 'Sections'],
			['button', 'More server actions'],
			['button', 'Refresh map preview'],
			['button', 'Expand world map']
		]
	},
	{
		path: '/settings',
		h1: 'Settings',
		controls: [
			['heading', 'Settings'],
			['tablist', 'Sections'],
			['tab', 'Server Defaults']
		]
	}
];

async function checkPlayerLookup(page, state, root, width, theme) {
	await check(`player lookup @${width} ${theme}: same-origin portrait, identity and absence`, async () => {
		const requests = [];
		const listen = (request) => requests.push(request);
		page.on('request', listen);
		state.lookupQueries = [];
		try {
			await page.goto(root + '/players', { waitUntil: 'domcontentloaded' });
			const lookup = page.getByRole('region', { name: 'Look up a player' });
			const input = lookup.getByRole('textbox', { name: 'Minecraft username or UUID' });
			const submit = async (query) => {
				await input.fill(query);
				await input.press('Enter');
			};

			await submit('FixtureSkin');
			const portrait = lookup.getByRole('img', { name: 'Full-body skin for FixtureSkin' });
			await portrait.waitFor();
			await page.waitForFunction(() => {
				const image = document.querySelector('img[alt="Full-body skin for FixtureSkin"]');
				return image?.complete && image.naturalWidth === 16 && image.naturalHeight === 32;
			});
			if (!(await portrait.getAttribute('src'))?.startsWith('data:image/png;base64,')) {
				throw new Error('Portrait was not rendered from panel response bytes');
			}
			await lookup.getByRole('heading', { name: 'FixtureSkin', exact: true }).waitFor();
			await lookup.getByText(LOOKUP_UUID, { exact: true }).waitFor();
			await lookup.getByText('Mojang', { exact: true }).waitFor();
			await lookup.getByText('Looking up an account does not add it to your roster.', { exact: true }).waitFor();
			if (await lookup.getByRole('link', { name: 'View player record' }).count()) {
				throw new Error('An unknown account linked to a roster identity');
			}

			await submit('FixtureNoSkin');
			await lookup.getByRole('heading', { name: 'FixtureNoSkin', exact: true }).waitFor();
			await lookup.getByText('Skin unavailable', { exact: true }).waitFor();
			await lookup.getByText(LOOKUP_UUID, { exact: true }).waitFor();

			await submit('FixtureMissing');
			await lookup.getByText('Account not found', { exact: true }).waitFor();
			if (await lookup.getByRole('article').count()) throw new Error('A missing account rendered a profile');

			if (state.lookupQueries.length !== 3 || state.lookupQueries.some((request) => !request.includeSkin)) {
				throw new Error('Profile requests did not opt into portrait data');
			}
			if (requests.some((request) => {
				const url = new URL(request.url());
				if (['http:', 'https:'].includes(url.protocol) && url.origin !== root) return true;
				return url.pathname.includes('/carbonpanel.v1.PlayerService/') &&
					!['LookupPlayer', 'ListPlayers'].includes(url.pathname.split('/').at(-1));
			})) {
				throw new Error('Lookup made an external request or accessed another player API');
			}
			return 'An account survived a missing portrait; no outside texture request or player mutation';
		} finally {
			page.off('request', listen);
			delete state.lookupQueries;
		}
	});
	await check(`player lookup @${width} ${theme}: historical joins respect explicit server scope`, async () => {
		const allServers = state.servers;
		const identityGrant = { resource: 'players', action: 'read', objectId: '*' };
		const presenceGrant = { resource: 'players', action: 'presence', objectId: 'fixture-presence-server' };
		state.servers = 'empty';
		state.lookupSessionRequests = [];
		const profile = async () => {
			await page.goto(root + '/players', { waitUntil: 'domcontentloaded' });
			const lookup = page.getByRole('region', { name: 'Look up a player' });
			const input = lookup.getByRole('textbox', { name: 'Minecraft username or UUID' });
			await input.fill('FixtureObserved');
			await input.press('Enter');
			await lookup.getByRole('heading', { name: 'FixtureObserved', exact: true }).waitFor();
			await lookup.getByText('Looking up an account does not add it to your roster.', { exact: true }).waitFor();
			return lookup;
		};
		try {
			state.permissions = [identityGrant];
			const readOnly = await profile();
			if (state.lookupSessionRequests.length || /recorded server join|last seen|2039/i.test(await readOnly.innerText())) {
				throw new Error('Identity-only lookup displayed or requested historical presence');
			}

			state.permissions = [identityGrant, presenceGrant];
			const scoped = await profile();
			await scoped.getByText('Recorded server join', { exact: true }).waitFor();
			const text = await scoped.innerText();
			if (!text.includes('2003') || /2039|fixture-presence-server|fixture-hidden-server|last seen/i.test(text)) {
				throw new Error('Wrong session timestamp or a hidden server identifier appeared');
			}
			if (state.lookupSessionRequests.length !== 1 || state.lookupSessionRequests[0]?.serverId !== presenceGrant.objectId) {
				throw new Error('An explicit presence grant did not use exactly its own server scope');
			}

			state.lookupSessionDenied = true;
			const refused = page.waitForResponse((response) => response.url().endsWith('/ListPlayerSessions') && response.status() === 403);
			const denied = await profile();
			await refused;
			if (/recorded server join|last seen|2039|2003/i.test(await denied.innerText())) {
				throw new Error('A refused session request altered or leaked the profile history');
			}
			return 'Known identity remained visible. History used the authorized scope with no server inventory.';
		} finally {
			state.servers = allServers;
			delete state.permissions;
			delete state.lookupSessionRequests;
			delete state.lookupSessionDenied;
		}
	});
}

async function runPackagedRouteJourney(browser, base, theme = 'light') {
	const root = base.replace(/\/$/, '');
	const state = { servers: 'populated', detail: 'populated' };
	const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
	// `carbon-theme` is the app's persisted preference key (see
	// display-preferences.svelte.ts) and drives `data-carbon-theme` on <html>.
	await context.addInitScript((mode) => {
		try {
			localStorage.setItem('carbon-theme', mode);
		} catch {
			// The per-route theme check reports the effective theme from the DOM.
		}
	}, theme);
	try {
		const page = await context.newPage();
		await seedPackagedRoutes(page, state, 'anonymous');

		for (const width of [1440, 1024, 768, 360]) {
			await page.setViewportSize({ width, height: 900 });
			for (const route of PACKAGED_ROUTES) {
				await page.goto(root + route.path, { waitUntil: 'domcontentloaded' });
				await page.getByRole('heading', { name: route.h1, level: 1 }).waitFor();

				await check(`route ${route.path} @${width} ${theme}: theme applied`, async () => {
					const info = await page.evaluate(() => ({
						carbonTheme: document.documentElement.getAttribute('data-carbon-theme'),
						dark: document.documentElement.classList.contains('dark'),
						background: getComputedStyle(document.body).backgroundColor
					}));
					if (info.carbonTheme !== theme || info.dark !== (theme === 'dark')) {
						throw new Error(`effective theme ${JSON.stringify(info)}, expected ${theme}`);
					}
					themeBackgrounds[theme] = info.background;
					return info;
				});

				await check(`route ${route.path} @${width} ${theme}: no horizontal page scroll`, async () => {
					const metrics = await page.evaluate(() => ({
						scrollWidth: document.documentElement.scrollWidth,
						innerWidth: window.innerWidth
					}));
					if (metrics.scrollWidth > metrics.innerWidth + 1) {
						throw new Error(`document scrollWidth ${metrics.scrollWidth} > viewport ${metrics.innerWidth}`);
					}
					return metrics;
				});

				await check(`route ${route.path} @${width} ${theme}: landmark and heading semantics`, async () => {
					const info = await page.evaluate(() => ({
						h1: document.querySelectorAll('h1').length,
						main: document.querySelectorAll('main').length,
						navigation: document.querySelectorAll('nav[aria-label="Main Navigation"]').length
					}));
					if (info.h1 !== 1 || info.main !== 1 || info.navigation !== 1) {
						throw new Error(`landmarks incomplete: ${JSON.stringify(info)}`);
					}
					return info;
				});

				await check(`route ${route.path} @${width} ${theme}: primary controls have accessible names`, async () => {
					for (const [role, name] of route.controls) {
						await page.getByRole(role, { name }).first().waitFor({ timeout: 10000 });
					}
					return route.controls.map(([role, name]) => `${role}:${name}`);
				});
				if (route.path === '/servers/srv-1') {
					await check(`server actions @${width} ${theme}: Copy Address announces a dismissible bottom-right toast`, async () => {
						const trigger = page.getByRole('button', { name: 'More server actions', exact: true });
						await trigger.press('Enter');
						const copyAddress = page.getByRole('menuitem', { name: 'Copy Address', exact: true });
						await copyAddress.waitFor();
						await copyAddress.press('Enter');
						await copyAddress.waitFor({ state: 'hidden' });
						const toast = page.locator('[data-sonner-toast]').filter({ hasText: 'Address copied to clipboard' });
						const offset = width <= 600 ? 12 : 24;
						await page.waitForFunction((offset) => {
							const item = Array.from(document.querySelectorAll('[data-sonner-toast][data-mounted="true"]')).find(
								(item) => item.querySelector('[data-title]')?.textContent?.trim() === 'Address copied to clipboard'
							);
							if (!item) return false;
							const list = item.closest('[data-sonner-toaster]');
							if (!list) return false;
							const placement = getComputedStyle(list);
							const anchor = list.getBoundingClientRect();
							const { left, right, bottom } = item.getBoundingClientRect();
							return placement.position === 'fixed' && placement.right === `${offset}px` &&
								placement.bottom === `${offset}px` && left >= 8 && right <= innerWidth &&
								Math.abs(right - anchor.right) <= 1 && Math.abs(bottom - anchor.bottom) <= 1;
						}, offset, { timeout: 5000 });
						assert.equal(await toast.evaluate((item) => item.closest('[aria-live]')?.getAttribute('aria-live')), 'polite');
						assert.equal(await toast.evaluate((item) => getComputedStyle(item).borderTopLeftRadius), '0px');
						if (process.env.UI_PASS_SHOTS)
							await page.screenshot({ path: `${process.env.UI_PASS_SHOTS}/toast-${width}-${theme}.png`, animations: 'disabled' });
						await toast.getByRole('button', { name: 'Dismiss notification' }).press('Enter');
						await toast.waitFor({ state: 'hidden' });
					});
					await check(`server map @${width} ${theme}: opens by keyboard and restores focus`, async () => {
						const expand = page.getByRole('button', { name: 'Expand world map', exact: true });
						await expand.press('Enter');
						const dialog = page.getByRole('dialog', { name: 'World map', exact: true });
						await dialog.getByRole('button', { name: 'Close modal', exact: true }).waitFor();
						await page.waitForFunction(() => Boolean(document.activeElement?.closest('[role="dialog"]')));
						await page.keyboard.press('Escape');
						await dialog.waitFor({ state: 'hidden' });
						assert.equal(await expand.evaluate((button) => document.activeElement === button), true);
					});
				}
			}

			if (width === 1440 || width === 360) await checkPlayerLookup(page, state, root, width, theme);

      await check(
        `shell @${width} ${theme}: skip link and off-canvas focus containment`,
        async () => {
          const state = await page.evaluate(() => ({
            main: Boolean(document.getElementById("carbon-main")),
            skip:
              document
                .querySelector('a[href="#carbon-main"]')
                ?.textContent?.trim() ?? null,
            navInert: document.getElementById("carbon-sidenav")?.inert ?? false,
          }));
          if (
            !state.main ||
            state.skip !== "Skip to content" ||
            state.navInert !== (width === 360)
          ) {
            throw new Error(
              `shell keyboard seam incomplete: ${JSON.stringify(state)}`,
            );
          }
          return state;
        },
      );

      await page.goto(root + "/", { waitUntil: "domcontentloaded" });
      await page
        .getByRole("heading", { name: "Cluster Overview", level: 1 })
        .waitFor();
      await check(
        `shell @${width} ${theme}: skip link and profile menu keyboard restoration`,
        async () => {
          await page.locator("body").click({ position: { x: 2, y: 2 } });
          await page.keyboard.press("Tab");
          const skipFocused = await page.evaluate(
            () =>
              document.activeElement?.textContent?.trim() === "Skip to content",
          );
          await page.keyboard.press("Enter");
          const landed = await page.evaluate(
            () => document.activeElement?.id ?? null,
          );
          await page.keyboard.press("Tab");
          const nextInMain = await page.evaluate(() =>
            Boolean(document.activeElement?.closest("main")),
          );
          const trigger = page.locator('[aria-haspopup="menu"]');
          await trigger.focus();

          await page.keyboard.press("Enter");
          const menu = page.locator('[role="menu"]');
          await menu.waitFor();
          await page.locator('[role="menuitem"]').first().focus();
          await page.keyboard.press("Escape");
          await menu.waitFor({ state: "detached" });
          await page.waitForFunction(
            () =>
              document.activeElement ===
              document.querySelector('[aria-haspopup="menu"]'),
            null,
            { timeout: 3000 },
          );
          const menuEscapeRestored = true;

          const dialogResults = {};
          for (const [label, title] of [
            ["Profile", "Profile"],
            ["Preferences", "Preferences"],
          ]) {
            await trigger.focus();
            await page.keyboard.press("Enter");
            await menu.waitFor();
            await page
              .getByRole("menuitem", { name: label, exact: true })
              .click();
            const dialog = page.getByRole("dialog");
            await dialog.waitFor();
            const heading = await dialog.getAttribute("aria-labelledby");
            dialogResults[label] = Boolean(heading);
            await page.keyboard.press("Escape");
            await dialog.waitFor({ state: "detached" });
            await page.waitForFunction(
              () =>
                document.activeElement ===
                document.querySelector('[aria-haspopup="menu"]'),
              null,
              { timeout: 3000 },
            );
            dialogResults[`${label}FocusRestored`] = true;
          }

          if (
            !skipFocused ||
            landed !== "carbon-main" ||
            !nextInMain ||
            !menuEscapeRestored ||
            Object.values(dialogResults).some((value) => !value)
          ) {
            throw new Error(
              `shell keyboard restoration incomplete: ${JSON.stringify({ skipFocused, landed, nextInMain, menuEscapeRestored, dialogResults })}`,
            );
          }
          return {
            skipFocused,
            landed,
            nextInMain,
            menuEscapeRestored,
            dialogResults,
          };
        },
      );
			await check(`dashboard @${width} ${theme}: KPI footers wrap without clipping`, async () => {
				const readTiles = () => page.evaluate(() => {
					const tiles = [...document.querySelectorAll('[data-testid="showcase-kpi-tiles"] > div')];
					if (tiles.length !== 4) throw new Error('Expected four populated KPI tiles');
					return tiles.map((tile) => {
						const footer = tile.querySelector('.border-t');
						const spans = footer ? [...footer.querySelectorAll('span')] : [];
						return {
							tileOverflow: tile.scrollWidth > tile.clientWidth + 1,
							footerOverflow: footer ? footer.scrollWidth > footer.clientWidth + 1 : false,
							clipped: spans
								.filter((span) => span.scrollWidth > span.clientWidth + 1)
								.map((span) => span.textContent?.trim() ?? ''),
							text: (footer?.textContent ?? '').replace(/\s+/g, ' ').trim()
						};
					});
				});
				await page.goto(root + '/', { waitUntil: 'domcontentloaded' });
				await page.getByRole('heading', { name: 'Cluster Overview', level: 1 }).waitFor();
				await page.locator('[data-testid="showcase-kpi-tiles"] > div').first().waitFor();
				const samples = [await readTiles()];
				if (width === 1024) {
					await page.locator('[aria-label="Toggle Navigation"]').click();
					await page.waitForTimeout(250);
					samples.push(await readTiles());
				}
				state.servers = 'large';
				await page.goto(root + '/', { waitUntil: 'domcontentloaded' });
				await page.getByRole('heading', { name: 'Cluster Overview', level: 1 }).waitFor();
				await page.locator('[data-testid="showcase-kpi-tiles"] > div').first().waitFor();
				if (process.env.UI_PASS_SHOTS) {
					await page.screenshot({ path: `${process.env.UI_PASS_SHOTS}/dashboard-kpi-${width}-${theme}.png` });
				}
				samples.push(await readTiles());
				state.servers = 'populated';
				const bad = samples.flat().filter((tile) => tile.tileOverflow || tile.footerOverflow || tile.clipped.length > 0);
				if (bad.length > 0) {
					throw new Error(`dashboard KPI footer clipped: ${JSON.stringify(bad)}`);
				}
				return samples.map((sample) => sample.map((tile) => tile.text));
			});

			await check(`server runtime metrics @${width} ${theme}: keyboard disclosure persists on reload and across servers`, async () => {
				await page.goto(root + '/servers/srv-1', { waitUntil: 'domcontentloaded' });
				const trigger = page.getByRole('button', { name: 'Runtime metrics', exact: true });
				const cards = page.locator('#server-runtime-metrics');
				await trigger.waitFor();
				if (await trigger.getAttribute('aria-controls') !== 'server-runtime-metrics' || !await cards.isVisible()) {
					throw new Error('Default server metrics or disclosure relationship missing');
				}
				await trigger.focus();
				await page.keyboard.press('Space');
				await cards.waitFor({ state: 'hidden' });
				if (await trigger.getAttribute('aria-expanded') !== 'false') throw new Error('Collapsed state not announced');
				await page.getByRole('tab', { name: 'World', exact: true }).click();
				if (await cards.isVisible()) throw new Error('Changing tab reopened the metrics');
				await page.reload({ waitUntil: 'domcontentloaded' });
				await trigger.waitFor();
				if (await cards.isVisible()) throw new Error('Reload lost the collapsed preference');
				await page.goto(root + '/servers/srv-2', { waitUntil: 'domcontentloaded' });
				await trigger.waitFor();
				if (await cards.isVisible()) throw new Error('Opening another server lost the preference');
				const searchInstalledMods = async (query) => {
					await page.getByRole('tab', { name: 'Mods', exact: true }).click();
					const panel = page.getByRole('tabpanel');
					const search = panel.getByPlaceholder('Search installed mods...');
					await search.click();
					await page.keyboard.press('ControlOrMeta+A');
					await page.keyboard.type(query);
					if (await search.inputValue() !== query) throw new Error('Mods control did not receive input');
					const bounds = await panel.boundingBox();
					if (!bounds || bounds.height < 160) throw new Error('Selected tab has no usable body');
				};
				await searchInstalledMods('collapsed');
				await trigger.focus();
				await page.keyboard.press('Enter');
				await cards.waitFor({ state: 'visible' });
				if (await trigger.getAttribute('aria-expanded') !== 'true') throw new Error('Expanded state not announced');
				await searchInstalledMods('expanded');
			});

			for (const path of ['/servers', '/servers/srv-1']) {
				await page.goto(root + path, { waitUntil: 'domcontentloaded' });
				await page.locator('h1').first().waitFor();
				await check(`route ${path} @${width} ${theme}: keyboard focus order`, async () => {
					await page.locator('body').click({ position: { x: 2, y: 2 } });
					const expected = width === 360 ? SHELL_FOCUS.slice(0, 3) : SHELL_FOCUS;
					const order = await focusNames(page, expected.length);
					if (!order.every((name, index) => matches(name, expected[index]))) {
						throw new Error(`focus order was ${JSON.stringify(order)}, expected ${JSON.stringify(expected)}`);
					}
					return order;
				});
			}

			await page.goto(root + '/servers', { waitUntil: 'domcontentloaded' });
			const deleteTrigger = page.getByRole('button', { name: 'Delete server Alpha Server' });
			await deleteTrigger.waitFor();
			await check(`route /servers @${width} ${theme}: delete dialog semantics, focus trap and Escape`, async () => {
				await deleteTrigger.click();
				const dialog = page.getByRole('dialog');
				await dialog.waitFor();
				const semantics = await dialog.evaluate((el) => ({
					modal: el.getAttribute('aria-modal'),
					labelledBy: el.getAttribute('aria-labelledby'),
					label: document.getElementById(el.getAttribute('aria-labelledby') ?? '')?.textContent?.trim()
				}));
				if (semantics.modal !== 'true' || !semantics.labelledBy || semantics.label !== 'Delete server?') {
					throw new Error(`dialog semantics incomplete: ${JSON.stringify(semantics)}`);
				}
				await page.waitForFunction(
					() => {
						const el = document.querySelector('[role="dialog"]');
						return Boolean(el && el.contains(document.activeElement));
					},
					null,
					{ timeout: 5000 }
				);
				const focusables = dialog.locator(FOCUSABLE);
				await focusables.last().focus();
				await page.keyboard.press('Tab');
				const wrapped = await dialog.evaluate((el, selector) => {
					const items = [...el.querySelectorAll(selector)];
					return document.activeElement === items[0] && el.contains(document.activeElement);
				}, FOCUSABLE);
				if (!wrapped) throw new Error('Tab did not wrap to the first dialog control');
				await page.keyboard.press('Escape');
				await dialog.waitFor({ state: 'detached' });
				await page.waitForFunction(
					() => document.activeElement?.getAttribute('aria-label') === 'Delete server Alpha Server',
					null,
					{ timeout: 5000 }
				);
				return { ...semantics, restored: true, wrapped: true };
			});

			await page.goto(root + '/servers/srv-1', { waitUntil: 'domcontentloaded' });
			const overviewTab = page.locator('#server-detail-tabs-tab-overview');
			await overviewTab.waitFor();
			await check(`route /servers/srv-1 @${width} ${theme}: tab targets, keyboard switching and lazy files`, async () => {
				const tablist = page.getByRole('tablist').filter({ has: overviewTab });
				const initial = 'server-detail-tabs-tab-overview';
				const consoleId = 'server-detail-tabs-tab-console';
				const count = await assertTabPanels(tablist, initial);
				if (count < 10) throw new Error(`Expected the server detail tabs, got ${count}`);
				if ((state.fileListings ?? 0) !== 0) throw new Error('Inactive Files tab fetched a directory');
				await overviewTab.focus();
				await page.keyboard.press('ArrowRight');
				if (await page.evaluate(() => document.activeElement?.id) !== consoleId) {
					throw new Error('ArrowRight did not focus Console');
				}
				await assertTabPanels(tablist, consoleId);
				await tabIntoPanel(page, 'server-detail-tabs-panel-console');
				await page.keyboard.press('Shift+Tab');
				await page.keyboard.press('ArrowLeft');
				await assertTabPanels(tablist, initial);
				const listing = page.waitForResponse((response) =>
					new URL(response.url()).pathname === '/carbonpanel.v1.FileService/ListFiles'
				);
				await tablist.getByTestId('tab-files').click();
				await listing;
				await assertTabPanels(tablist, 'server-detail-tabs-tab-files');
				await overviewTab.click();
				await assertTabPanels(tablist, initial);
				state.fileListings = 0;
				return `${count} linked panels; Files fetched only on activation`;
			});

			await check(`server players @${width} ${theme}: roster filters persist and allowlist opens in a dialog`, async () => {
				await page.getByRole('tab', { name: 'Players', exact: true }).click();
				const roster = page.locator('[data-player-row]').filter({ hasText: 'SolarFox' });
				await roster.waitFor();
				const search = page.getByRole('textbox', { name: 'Search players' });
				await search.fill('unmatched-fixture');
				await page.getByText('No players match these filters.').waitFor();
				assert(await search.isVisible(), 'search must remain usable after a zero-match query');
				await page.getByRole('button', { name: 'Clear filters', exact: true }).click();
				await roster.waitFor();

				await page.keyboard.press('j');
				await page.keyboard.press('x');
				assert(await roster.getByRole('checkbox').isChecked(), 'test requires a selected roster row');
				const trigger = page.getByRole('button', { name: 'Add player to allowlist' });
				await trigger.focus();
				await trigger.press('Enter');
				const dialog = page.getByRole('dialog', { name: 'Add player to allowlist' });
				await dialog.getByRole('textbox', { name: 'Exact UUID or username' }).fill('ReviewGuest');
				const submit = dialog.getByRole('button', { name: 'Add to allowlist', exact: true });
				await submit.focus();
				await submit.press('x');
				assert(await roster.getByRole('checkbox').isChecked(), 'dialog shortcuts must leave the roster selection intact');
				await submit.press('/');
				assert(await submit.evaluate((el) => el === document.activeElement), 'dialog must retain keyboard focus');
				await submit.press('Enter');
				await dialog.getByText('Review guest added.', { exact: true }).waitFor();
				await page.keyboard.press('Escape');
				await dialog.waitFor({ state: 'hidden' });
				assert(await trigger.evaluate((el) => el === document.activeElement), 'allowlist close returns focus');
				await trigger.press('Enter');
				await dialog.getByRole('button', { name: 'Undo', exact: true }).click();
				await dialog.getByText('Allowlist entry removed.', { exact: true }).waitFor();
				await page.keyboard.press('Escape');
				await dialog.waitFor({ state: 'hidden' });
				return 'roster remains visible, and closing the scoped dialog preserves its undo action';
			});

			if (width === 1024 && theme === 'dark') {
				await check('server players @1024: allowlist dialog survives a held telemetry refresh', async () => {
					let markArrived;
					const refreshArrived = new Promise((resolve) => { markArrived = resolve; });
					let releaseRefresh;
					const refreshReleased = new Promise((resolve) => { releaseRefresh = resolve; });
					const marker = 'Telemetry Refresh Probe';
					const control = {
						hold: true,
						count: 0,
						marker,
						arrived: markArrived,
						release: refreshReleased
					};
					state.detailRefresh = control;
					let released = false;
					const release = () => {
						if (!released) {
							released = true;
							releaseRefresh();
						}
					};
					try {
						const trigger = page.getByRole('button', { name: 'Add player to allowlist' });
						await trigger.click();
						const dialog = page.getByRole('dialog', { name: 'Add player to allowlist' });
						const field = dialog.getByRole('textbox', { name: 'Exact UUID or username' });
						await field.waitFor();

						// Hold the refresh at the RPC boundary until the operator has
						// typed, then deliver a fresh object with the same id and a
						// harmless visible marker we can await in the rendered UI.
						await Promise.race([
							refreshArrived,
							new Promise((_, reject) =>
								setTimeout(() => reject(new Error('no telemetry refresh arrived within 10s')), 10000)
							)
						]);
						await field.fill('TypedDuringRefresh');
						assert.equal(await field.inputValue(), 'TypedDuringRefresh');
						assert(await dialog.isVisible(), 'dialog must stay open while the refresh is held');

						release();
						// Awaiting the rendered marker proves Svelte consumed the
						// refresh; a count or fixed sleep would pass on a stalled response.
						await page.getByText(marker, { exact: true }).first().waitFor({ timeout: 10000 });
						assert(control.count >= 1, 'a real GetServer refresh must have been delivered');
						assert(await dialog.isVisible(), 'dialog must stay open after a same-identity refresh');
						assert.equal(
							await field.inputValue(),
							'TypedDuringRefresh',
							'typed text must survive the refresh'
						);

						// The modal traps pointer events, so leave the tab the way a
						// keyboard user would: focus the tablist and arrow away.
						await page.locator('#server-detail-tabs-tab-players').focus();
						await page.keyboard.press('ArrowRight');
						await dialog.waitFor({ state: 'hidden' });
						return `dialog survived ${control.count} same-identity refresh(es) and closed on tab exit`;
					} finally {
						release();
						control.hold = false;
						delete state.detailRefresh;
					}
				});
			}

			await check(`cross-server roster @${width} ${theme}: an empty server filter remains reversible`, async () => {
				await page.goto(root + '/players', { waitUntil: 'domcontentloaded' });
				const selector = page.getByRole('combobox', { name: 'Server', exact: true });
				await selector.selectOption('srv-2');
				await page.getByText('No players match these filters.').waitFor();
				assert.equal(await selector.inputValue(), 'srv-2');
				await page.getByRole('button', { name: 'Clear filters', exact: true }).click();
				assert.equal(await selector.inputValue(), '');
				await page.getByRole('button', { name: 'SolarFox', exact: true }).waitFor();
			});

			await page.goto(root + '/settings', { waitUntil: 'domcontentloaded' });
			await page.getByRole('heading', { name: 'Settings', level: 1 }).waitFor();
			await check(`route /settings @${width} ${theme}: tabs, tabpanel relationships, hidden panels and arrow-key navigation`, async () => {
				await page.locator('[role="tablist"]').waitFor();
				const readState = () => page.evaluate(() => {
					const tabs = [...document.querySelectorAll('[role="tab"]')];
					const selected = tabs.find((tab) => tab.getAttribute('aria-selected') === 'true');
					const panels = [...document.querySelectorAll('[role="tabpanel"]')].map((panel) => ({
						id: panel.id,
						labelledBy: panel.getAttribute('aria-labelledby'),
						hidden: panel.hasAttribute('hidden') || getComputedStyle(panel).display === 'none',
						height: panel.getBoundingClientRect().height
					}));
					return {
						label: document.querySelector('[role="tablist"]')?.getAttribute('aria-label'),
						tabCount: tabs.length,
						firstTabId: tabs[0]?.id ?? null,
						selectedId: selected?.id ?? null,
						selectedControls: selected?.getAttribute('aria-controls') ?? null,
						panels,
						visiblePanels: panels.filter((panel) => !panel.hidden).map((panel) => panel.id),
						hiddenPanels: panels.filter((panel) => panel.hidden).map((panel) => panel.id)
					};
				});
				const panelIdFor = (tabId) => `settings-tabs-panel-${tabId?.replace('settings-tabs-tab-', '') ?? ''}`;
				const hiddenPanelContract = (state) =>
					state.panels.length === state.tabCount &&
					state.visiblePanels.length === 1 &&
					state.visiblePanels[0] === state.selectedControls &&
					state.hiddenPanels.length === state.tabCount - 1 &&
					state.panels.every(
						(panel) =>
							panel.labelledBy === panel.id.replace('settings-tabs-panel-', 'settings-tabs-tab-') &&
							(!panel.hidden || panel.height === 0)
					);
				const initial = await readState();
				if (
					initial.label !== 'Sections' ||
					initial.tabCount < 1 ||
					initial.firstTabId !== 'settings-tabs-tab-server-config' ||
					initial.selectedId !== 'settings-tabs-tab-server-config' ||
					initial.selectedControls !== 'settings-tabs-panel-server-config' ||
					!hiddenPanelContract(initial)
				) {
					throw new Error(`settings tab semantics incomplete: ${JSON.stringify(initial)}`);
				}
				const secondTabId = await page.evaluate(() => document.querySelectorAll('[role="tab"]')[1]?.id ?? null);
				await page.locator('#settings-tabs-tab-server-config').focus();
				await page.keyboard.press('ArrowRight');
				const focused = await page.evaluate(() => document.activeElement?.id);
				if (!secondTabId || focused !== secondTabId) {
					throw new Error(`ArrowRight did not move settings tab focus (got ${focused}, expected ${secondTabId})`);
				}
				const after = await readState();
				if (
					after.selectedId !== secondTabId ||
					after.selectedControls !== panelIdFor(secondTabId) ||
					!hiddenPanelContract(after) ||
					!after.hiddenPanels.includes('settings-tabs-panel-server-config') ||
					after.panels.find((panel) => panel.id === 'settings-tabs-panel-server-config')?.height !== 0
				) {
					throw new Error(`settings ArrowRight did not update tab/tabpanel relationship: ${JSON.stringify(after)}`);
				}
				await assertTabPanels(page.getByRole('tablist'), secondTabId);
				await tabIntoPanel(page, after.selectedControls);
				await page.keyboard.press('Shift+Tab');
				await page.keyboard.press('ArrowLeft');
				return { initial, focusedAfterArrowRight: focused, after };
			});
      await page.goto(root + "/servers/srv-1", {
        waitUntil: "domcontentloaded",
      });
      await page.locator("#server-detail-tabs-tab-world").click();
      // Every visited tab mounts its own server tab panel, so the bare
      // [data-server-tab-panel] selector matches more than one element and
      // Playwright's strict mode refuses it. Scope the wait to the world
      // panel that the click above opened.
      await page
        .locator("#server-detail-tabs-panel-world [data-server-tab-panel]")
        .first()
        .waitFor();
		}

		await page.setViewportSize({ width: 1440, height: 900 });
		await check(`route /servers ${theme}: empty state is exposed`, async () => {
			state.servers = 'empty';
			await page.goto(root + '/servers', { waitUntil: 'domcontentloaded' });
			await page.getByRole('heading', { name: 'No servers configured' }).waitFor();
			state.servers = 'populated';
			return { empty: true };
		});
		await check(`route /servers ${theme}: error state is exposed`, async () => {
			state.servers = 'error';
			await page.goto(root + '/servers', { waitUntil: 'domcontentloaded' });
			await page.getByRole('alert').waitFor();
			await page.getByRole('heading', { name: 'Failed to load servers' }).waitFor();
			state.servers = 'populated';
			return { error: true };
		});
		await check(`route / ${theme}: error state is exposed`, async () => {
			state.servers = 'error';
			await page.goto(root + '/', { waitUntil: 'domcontentloaded' });
			await page.getByRole('alert').waitFor();
			await page.getByRole('heading', { name: 'Failed to load dashboard' }).waitFor();
			state.servers = 'populated';
			return { error: true };
		});
		await check(`route /servers/srv-1 ${theme}: error state is exposed`, async () => {
			// An empty inventory forces the real detail loader to call GetServer;
			// a cached row can render the detail without that request.
			state.servers = 'empty';
			state.detail = 'error';
			await page.goto(root + '/servers/srv-1', { waitUntil: 'domcontentloaded' });
			const alert = page.getByRole('alert').filter({ hasText: 'Failed to load server' });
			await alert.waitFor();
			state.detail = 'populated';
			state.servers = 'populated';
			return { error: (await alert.textContent())?.trim().slice(0, 80) };
		});
	} finally {
		await context.close();
	}

	// The login route needs a separate context: with authentication disabled the
	// shell correctly redirects /login to the dashboard.
	const loginContext = await browser.newContext({ viewport: { width: 1440, height: 900 } });
	try {
		const page = await loginContext.newPage();
		await seedPackagedRoutes(page, { servers: 'populated', detail: 'populated' }, 'login-failure');
		for (const width of [1440, 768, 360]) {
			await page.setViewportSize({ width, height: 900 });
			await page.goto(root + '/login', { waitUntil: 'domcontentloaded' });
			await check(`route /login @${width} ${theme}: form semantics, names and error state`, async () => {
				await page.getByRole('heading', { name: 'Carbon Panel', level: 1 }).waitFor();
				await page.getByLabel('Username', { exact: true }).waitFor();
				await page.getByLabel('Password', { exact: true }).waitFor();
				await page.getByRole('button', { name: 'Show password' }).waitFor();
				await page.getByRole('button', { name: 'Sign in', exact: true }).waitFor();
				const metrics = await page.evaluate(() => ({
					scrollWidth: document.documentElement.scrollWidth,
					innerWidth: window.innerWidth
				}));
				if (metrics.scrollWidth > metrics.innerWidth + 1) {
					throw new Error(`login overflow ${metrics.scrollWidth} > ${metrics.innerWidth}`);
				}
				await page.getByLabel('Username', { exact: true }).fill('review');
				await page.getByLabel('Password', { exact: true }).fill('wrong-password');
				await page.getByRole('button', { name: 'Sign in', exact: true }).click();
				const announced = page
					.getByRole('alert')
					.filter({ hasText: 'Invalid username or password' });
				await announced.first().waitFor();
				if (!(await announced.first().evaluate((node) => node === document.activeElement))) {
					throw new Error('Failed sign-in did not focus the error message');
				}
				const recovery = page.getByRole('button', { name: 'Lost access? Use a recovery key' });
				await recovery.click();
				if (!(await page.getByLabel('Recovery Key', { exact: true }).evaluate((node) => node === document.activeElement))) {
					throw new Error('Opening recovery did not focus the recovery key field');
				}
				await page.getByRole('button', { name: 'Cancel', exact: true }).click();
				if (!(await recovery.evaluate((node) => node === document.activeElement))) {
					throw new Error('Canceling recovery did not return focus');
				}
				return {
					path: await page.evaluate(() => location.pathname),
					error: true,
					announced: await announced.count(),
					metrics
				};
			});
		}
	} finally {
		await loginContext.close();
	}
}

// OSS-406 named recurring modpack journey, driven on the real packaged build.
// It executes the same selectors as scripts/review/plan-modpacks-library.json
// and plan-modpacks-studio-library.json: the library shell, its tab strip and
// search control, then a public-seam refusal (SearchModpacks -> 503) followed
// by reconciliation (Retry -> seeded catalog). No world, credential or indexer
// state is mutated; the only stub is the external RPC response.
async function assertTabPanels(tablist, selectedId) {
	await tablist.getByRole('tab').first().waitFor();
	const tabs = await tablist.getByRole('tab').evaluateAll((buttons) =>
		buttons.map((button) => {
			const id = button.getAttribute('aria-controls');
			const panels = id ? [...document.querySelectorAll('#' + CSS.escape(id))] : [];
			const panel = panels[0];
			return {
				tabId: button.id,
				tabIdCount: button.id ? document.querySelectorAll('#' + CSS.escape(button.id)).length : 0,
				tabIndex: button.tabIndex,
				selected: button.getAttribute('aria-selected') === 'true',
				id,
				count: panels.length,
				role: panel?.getAttribute('role'),
				labelledBy: panel?.getAttribute('aria-labelledby'),
				visible: Boolean(panel?.getClientRects().length),
				hidden: panel?.hidden,
				panelTabIndex: panel?.tabIndex,
				children: panel?.childElementCount
			};
		})
	);
	if (
		tabs.length < 2 ||
		new Set(tabs.map((tab) => tab.id)).size !== tabs.length ||
		tabs.filter((tab) => tab.selected).length !== 1 ||
		tabs.find((tab) => tab.selected)?.tabId !== selectedId ||
		tabs.some((tab) => {
			if (
				tab.tabIdCount !== 1 ||
				!tab.id ||
				tab.count !== 1 ||
				tab.role !== 'tabpanel' ||
				tab.labelledBy !== tab.tabId ||
				tab.visible !== tab.selected ||
				tab.tabIndex !== (tab.selected ? 0 : -1)
			) {
				return true;
			}
			if (tab.selected) {
				return tab.hidden || tab.panelTabIndex !== 0;
			}
			// Inactive panels must be hidden. Transient panels are unmounted
			// placeholders (children 0, panelTabIndex -1); OSS-464 persistent
			// editable panels stay mounted behind hidden with tabindex=0 so
			// unsaved edits survive tab switches.
			return (
				!tab.hidden ||
				(tab.panelTabIndex !== -1 && tab.panelTabIndex !== 0) ||
				(tab.children !== 0 && tab.panelTabIndex !== 0)
			);
		})
	) {
		throw new Error(`Tab panels for ${selectedId}: ${JSON.stringify(tabs)}`);
	}
	return tabs.length;
}

async function tabIntoPanel(page, panelId) {
	await page.keyboard.press('Tab');
	const focusedId = await page.evaluate(() => document.activeElement?.id);
	if (focusedId !== panelId) {
		throw new Error(`Tab focused ${focusedId}; expected ${panelId}`);
	}
}

async function runModpacksJourney(browser, base, theme = 'light', width = 1440) {
	const root = base.replace(/\/$/, '');
	const context = await browser.newContext({ viewport: { width, height: 900 } });
	await context.addInitScript((mode) => {
		try {
			localStorage.setItem('carbon-theme', mode);
		} catch {
			// The per-route theme check reports the effective theme from the DOM.
		}
	}, theme);
	try {
		const page = await context.newPage();
		const state = { servers: 'populated', detail: 'populated', modpacks: 'populated' };
		await seedPackagedRoutes(page, state, 'anonymous');

		await check(`modpacks @${width} ${theme}: library shell and tab controls render`, async () => {
			await page.goto(root + '/modpacks', { waitUntil: 'domcontentloaded' });
			await page.getByRole('heading', { name: 'Modpacks', level: 1 }).waitFor();
			await page.getByText('Browse, import and manage modpack packages').first().waitFor();
			await page.getByTestId('showcase-modpacks-content').first().waitFor();
			for (const tab of ['Browse', 'Imported', 'Favorites']) {
				await page.getByRole('tab', { name: new RegExp(tab) }).first().waitFor({ timeout: 10000 });
			}
			await page.getByPlaceholder('Search modpacks by name or description…').waitFor();
			return 'description, content area, three tabs and search control visible';
		});

		await check(`modpacks @${width} ${theme}: library and import tab targets, keyboard switching and lazy controls`, async () => {
			const browse = page.getByTestId('tab-browse');
			const library = page.getByRole('tablist').filter({ has: browse });
			const search = page.getByPlaceholder('Search modpacks by name or description…');
			await assertTabPanels(library, 'modpacks-tab-browse');
			await browse.focus();
			await page.keyboard.press('ArrowRight');
			await assertTabPanels(library, 'modpacks-tab-imported');
			if (await search.count()) throw new Error('Inactive browse controls are still mounted');
			await tabIntoPanel(page, 'modpacks-panel-imported');
			await page.keyboard.press('Shift+Tab');
			await page.keyboard.press('End');
			await assertTabPanels(library, 'modpacks-tab-favorites');
			await tabIntoPanel(page, 'modpacks-panel-favorites');
			await page.keyboard.press('Shift+Tab');
			await page.keyboard.press('Home');
			await assertTabPanels(library, 'modpacks-tab-browse');
			await search.waitFor();
			await tabIntoPanel(page, 'modpacks-panel-browse');

			await page.getByRole('button', { name: 'Import modpack', exact: true }).click();
			const dialog = page.getByRole('dialog', { name: 'Import modpack' });
			try {
				await page.waitForFunction(() =>
					document.activeElement?.matches('[role="dialog"] button[aria-label="Close modal"]')
				);
				const methods = dialog.getByRole('tablist');
				await assertTabPanels(methods, 'import-method-tab-zip');
				const urlInput = dialog.getByLabel('Modpack archive URL *', { exact: true });
				if (await urlInput.count()) throw new Error('Inactive URL import form is mounted');
				await methods.getByTestId('tab-zip').focus();
				await page.keyboard.press('ArrowRight');
				await assertTabPanels(methods, 'import-method-tab-url');
				await urlInput.waitFor();
				await tabIntoPanel(page, 'import-method-panel-url');
			} finally {
				await page.keyboard.press('Escape');
				await dialog.waitFor({ state: 'hidden' });
			}
			return 'Two linked tab lists; hidden controls stayed unmounted';
		});

		await check(`modpacks @${width} ${theme}: browse filters have accessible names on desktop and mobile`, async () => {
			const previousViewport = page.viewportSize();
			try {
				for (const width of [1440, 360]) {
					await page.setViewportSize({ width, height: 900 });
					for (const name of ['Minecraft version', 'Mod loader', 'Modpack provider']) {
						await page.getByRole('combobox', { name, exact: true }).waitFor({ timeout: 3000 });
					}
				}
			} finally {
				await page.setViewportSize(previousViewport);
			}
			return 'three filters keep their accessible names at both widths';
		});

		await check(`modpacks @${width} ${theme}: seeded catalog renders from the RPC boundary`, async () => {
			await page.getByTestId('showcase-modpack-card').first().waitFor({ timeout: 10000 });
			return page.getByTestId('showcase-modpack-card').count();
		});

		await check(`modpacks @${width} ${theme}: compact table opens complete pack details with keyboard`, async () => {
			const table = page.getByRole('table').filter({ hasText: 'Seeded Fabric Pack' });
			const details = table.getByRole('button', { name: 'View details for Seeded Fabric Pack' });
			await details.focus();
			await details.press('Enter');
			const dialog = page.getByRole('dialog', { name: 'Seeded Fabric Pack' });
			await dialog.getByRole('heading', { name: 'Compatibility' }).waitFor();
			await dialog.getByText('2 GB', { exact: true }).waitFor();
			await dialog.getByText('Fabric pack 1.0', { exact: true }).waitFor();
			await dialog.getByRole('button', { name: 'Use in Server' }).waitFor();
			await page.keyboard.press('Escape');
			await dialog.waitFor({ state: 'hidden' });
			assert(await details.evaluate((el) => el === document.activeElement), 'closing returns focus to the pack');
			return 'description, compatibility, indexed files and create action present';
		});

		await check(`modpacks @${width} ${theme}: refused search exposes the error state and Retry`, async () => {
			state.modpacks = 'refused';
			await page.getByPlaceholder('Search modpacks by name or description…').fill('refusal-probe');
			await page.getByRole('button', { name: 'Search', exact: true }).first().click();
			await page.getByText('Failed to search modpacks').first().waitFor({ timeout: 10000 });
			await page.getByRole('button', { name: 'Retry', exact: true }).first().waitFor();
			return 'error title and Retry control visible';
		});

		await check(`modpacks @${width} ${theme}: Retry reconciles back to the populated catalog`, async () => {
			state.modpacks = 'populated';
			await page.getByRole('button', { name: 'Retry', exact: true }).first().click();
			await page.getByText('Failed to search modpacks').first().waitFor({ state: 'hidden', timeout: 10000 });
			await page.getByTestId('showcase-modpack-card').first().waitFor({ timeout: 10000 });
			return 'error cleared and seeded card restored';
		});

		await check(`modpacks @${width} ${theme}: studio library shell controls render`, async () => {
			state.modpacks = 'populated';
			await page.goto(root + '/modpacks/studio', { waitUntil: 'domcontentloaded' });
			await page.getByRole('heading', { name: 'Modpack Studio', level: 1 }).waitFor();
			await page.getByText('Visual workspace for custom modpacks:', { exact: false }).first().waitFor();
			await page.getByRole('button', { name: 'Create Modpack' }).first().waitFor();
			await page.getByRole('button', { name: 'Import modpack' }).first().waitFor();
			await page.getByRole('link', { name: 'Browse Modpacks' }).first().waitFor();
			await page.getByPlaceholder('Search modpack projects...').waitFor();
			return 'studio heading, description, three controls and project search visible';
		});
	} finally {
		await context.close();
	}
}

// Cross-engine smoke: a deliberately small slice of the packaged-route journey
// (the three high-value routes, both themes, two widths) run in Firefox or
// WebKit when CI has installed those browsers into the owned cache.
// Check nested clipping directly. A whole-page scrollWidth check can pass
// because an ancestor silently clips a settings control or part of its label.
async function assertHorizontallyVisible(locator) {
	await locator.waitFor();
	const clipped = await locator.evaluate((element) => {
		let left = 0;
		let right = document.documentElement.clientWidth;
		const clips = (container) =>
			['auto', 'scroll', 'hidden', 'clip'].includes(getComputedStyle(container).overflowX);
		const intersect = (container) => {
			const clientLeft = container.getBoundingClientRect().left + container.clientLeft;
			left = Math.max(left, clientLeft);
			right = Math.min(right, clientLeft + container.clientWidth);
		};
		const outside = (bounds) => bounds.width && (bounds.left < left - 1 || bounds.right > right + 1);

		for (let parent = element.parentElement; parent; parent = parent.parentElement) {
			if (clips(parent)) intersect(parent);
		}
		const box = element.getBoundingClientRect();
		if (outside(box)) return box.toJSON();

		// Own overflow can clip content even when the entire element box is visible.
		if (clips(element)) intersect(element);
		const text = document.createRange();
		text.selectNodeContents(element);
		return [...text.getClientRects()].find(outside)?.toJSON();
	});
	if (clipped) throw new Error(String(locator) + ' is clipped: ' + JSON.stringify(clipped));
}

async function runClippingProbe(browser) {
	await check('visibility probe detects self-clipped text inside a wider parent', async () => {
		const context = await browser.newContext();
		try {
			const page = await context.newPage();
			await page.setContent(
				'<div style="width:480px;overflow:hidden">' +
					'<button style="width:180px;overflow:hidden;border:4px solid">Save settings</button>' +
					'<p style="width:80px;overflow:hidden;white-space:nowrap">A much longer settings label</p>' +
				'</div>'
			);
			await assertHorizontallyVisible(page.getByRole('button', { name: 'Save settings' }));
			await assert.rejects(
				() => assertHorizontallyVisible(page.getByText('A much longer settings label')),
				/is clipped:/
			);
		} finally {
			await context.close();
		}
	});
}

async function runNarrowSettingsJourney(browser, base) {
	const root = base.replace(/\/$/, '');
	for (const theme of ['light', 'dark']) {
		for (const density of ['comfortable', 'compact']) {
			const context = await browser.newContext({ reducedMotion: 'reduce' });
			await context.addInitScript(({ theme, density }) => {
				localStorage.setItem('carbon-theme', theme);
				localStorage.setItem('carbon-density', density);
			}, { theme, density });
			try {
				const page = await context.newPage();
				const state = { servers: 'populated', detail: 'populated', narrowSettings: true };
				await seedPackagedRoutes(page, state, 'anonymous');
				for (const width of [360, 1440]) {
					await page.setViewportSize({ width, height: 900 });
					const capture = async (screen) => {
						if (process.env.UI_PASS_SHOTS)
							await page.screenshot({
								path: `${process.env.UI_PASS_SHOTS}/settings-${screen}-${width}-${theme}-${density}.png`,
								animations: 'disabled'
							});
					};
					const label = `@${width} ${theme} ${density}`;
					await check(`settings defaults ${label}: labelled fields and enabled actions fit`, async () => {
						await page.goto(root + '/settings', { waitUntil: 'domcontentloaded' });
						const motd = page.getByRole('textbox', { name: 'Message of the day' });
						await motd.waitFor();
						const preferences = await page.locator('html').evaluate((element) => [
							element.getAttribute('data-carbon-theme'), element.getAttribute('data-carbon-density')
						]);
						if (preferences.join('/') !== `${theme}/${density}`)
							throw new Error(`Unexpected theme or density: ${preferences}`);
						const actions = page.locator('.server-tab-actions');
						const save = actions.getByRole('button', { name: 'Save', exact: true });
						if (width === 360) {
							const chooser = page.getByRole('combobox', { name: 'Configuration category' });
							await chooser.selectOption('networking');
							await page.getByRole('spinbutton', { name: 'Game port' }).waitFor();
							await chooser.selectOption('game');
						}
						if ((await page.locator('#motd').count()) !== 1 || (await motd.getAttribute('id')) === 'motd')
							throw new Error('The MOTD link and form control need distinct IDs');
						await assertHorizontallyVisible(motd);
						await assertHorizontallyVisible(page.getByRole('combobox', { name: 'Difficulty' }));
						await motd.fill('Ready to save');
						if (!(await save.isEnabled())) throw new Error('Save did not enable after editing MOTD');
						for (const button of await actions.getByRole('button').all())
							await assertHorizontallyVisible(button);
						await capture('defaults');
						const navigation = page.locator('button[aria-controls="carbon-sidenav"]');
						await navigation.press('Enter');
						const toggled = await navigation.getAttribute('aria-expanded');
						if (toggled !== (width === 360 ? 'true' : 'false'))
							throw new Error('Keyboard navigation toggle did not change state');
						if (width === 360) await page.keyboard.press('Escape');
						await assertHorizontallyVisible(save);
						state.savedMotd = undefined;
						await save.press('Enter');
						await page.getByText('Global settings saved', { exact: true }).waitFor();
						if (state.savedMotd !== 'Ready to save') throw new Error('Keyboard Save did not submit MOTD');
					});
					await check(`server configuration ${label}: find, edit and stage across categories`, async () => {
						await page.goto(root + '/servers/srv-1', { waitUntil: 'domcontentloaded' });
						const metrics = page.getByRole('button', { name: 'Runtime metrics' });
						if (await metrics.getAttribute('aria-expanded') === 'true') await metrics.click();
						const tab = page.getByRole('tab', { name: 'Configuration', exact: true });
						await tab.click();
						assert.equal(new URL(page.url()).searchParams.get('tab'), 'configuration');
						const search = page.getByRole('searchbox', { name: 'Search settings' });
						await search.waitFor();
						await capture('configuration');
						await search.fill('tcp SERVER_PORT');
						const port = page.getByRole('spinbutton', { name: 'Game port' });
						await port.waitFor();
						assert.equal(await page.locator('[data-field]').count(), 1);
						const details = page.getByRole('button', { name: 'Details for Game port' });
						await details.press('Enter');
						assert.equal(await details.getAttribute('aria-expanded'), 'true');
						await page.getByText('TCP listener for the game.', { exact: true }).waitFor();
						await assertHorizontallyVisible(details);
						await assertHorizontallyVisible(page.getByText('TCP listener for the game.', { exact: true }));
						await page.getByRole('button', { name: 'Copy link', exact: true }).scrollIntoViewIfNeeded();
						await capture('configuration-details');
						await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: root });
						await page.getByRole('button', { name: 'Copy link', exact: true }).click();
						const link = await page.evaluate(() => navigator.clipboard.readText());
						assert.equal(link, root + '/servers/srv-1?tab=configuration#port');
						await page.goto(root + '/servers', { waitUntil: 'domcontentloaded' });
						await page.goto(link, { waitUntil: 'domcontentloaded' });
						await port.waitFor();
						await page.locator('#port.highlighted').waitFor();
						assert.equal(await tab.getAttribute('aria-selected'), 'true');
						await page.goto(root + '/servers/srv-1#port');
						await page.locator('#port.highlighted').waitFor();
						assert.equal(await tab.getAttribute('aria-selected'), 'true');
						await page.getByRole('tab', { name: 'Overview', exact: true }).click();
						await page.getByRole('heading', { name: 'Server Settings', exact: true }).waitFor();
						assert.equal(new URL(page.url()).hash, '');
						await tab.click();
						await port.fill('25568');
						await search.fill('');
						await page.getByRole('button', { name: 'Unsaved (1)' }).click();
						assert.equal(await page.locator('[data-field]').count(), 1);
						await assertHorizontallyVisible(port);
						await page.getByRole('button', { name: 'Stage', exact: true }).click();
						await page.getByText('Configuration staged', { exact: true }).waitFor();
						assert.equal(state.configRequest.stage, true);
						assert.equal(state.configRequest.serverId, 'srv-1');
						assert.deepEqual(state.configRequest.updates, { port: '25568' });
					});
					await check(`settings auth ${label}: OIDC names wrap inside their panel`, async () => {
						await page.goto(root + '/settings?tab=auth', { waitUntil: 'domcontentloaded' });
						await page.getByRole('button', { name: 'Environment Variables' }).press('Enter');
						const names = page.locator('main code').filter({ hasText: /^CARBONPANEL_AUTH_OIDC_/ });
						await names.last().waitFor();
						if ((await names.count()) !== 7) throw new Error('Expected all seven OIDC variable names');
						for (const name of await names.all()) await assertHorizontallyVisible(name);
						await names.last().scrollIntoViewIfNeeded();
						await capture('oidc');
					});
					await check(`server settings ${label}: migration keyboard selection and memory advisory fit`, async () => {
						let releaseNodes;
						state.nodesReady = new Promise((resolve) => { releaseNodes = resolve; });
						const target = page.getByRole('combobox', { name: 'Migrate to Target Node' });
						try {
							await page.goto(root + '/servers/srv-1', { waitUntil: 'domcontentloaded' });
							await target.and(page.locator(':disabled')).waitFor();
						} finally {
							releaseNodes();
							delete state.nodesReady;
						}
						// Locator.press does not wait for an asynchronously enabled control.
						await target.and(page.locator(':enabled')).waitFor();
						await target.press('Enter');
						const option = page.getByRole('option', { name: new RegExp(SETTINGS_NODE) });
						await assertHorizontallyVisible(option);
						await capture('migration-options');
						await page.keyboard.press('End');
						await page.keyboard.press('Enter');
						await target.getByText(SETTINGS_NODE, { exact: true }).waitFor();
						await assertHorizontallyVisible(target);
						const migrate = page.getByRole('button', { name: 'Migrate Node', exact: true });
						if (!(await migrate.isEnabled())) throw new Error('Migration did not enable after selecting target');
						await assertHorizontallyVisible(migrate);
						await capture('migration');
						await page.getByRole('button', { name: /Docker Overrides/ }).press('Enter');
						const memory = page.getByRole('spinbutton', { name: 'Memory request (MB)' });
						await assertHorizontallyVisible(memory);
						const advisory = page.locator('#memory-limit-advisory');
						if ((await memory.getAttribute('aria-describedby')) !== 'memory-limit-advisory')
							throw new Error('Memory field has no advisory description');
						await assertHorizontallyVisible(advisory);
						await advisory.scrollIntoViewIfNeeded();
						await capture('memory');
					});
				}
			} finally {
				await context.close();
			}
		}
	}
}

async function runCrossEngineSmoke(browser, base, engine) {
	const root = base.replace(/\/$/, '');
	const highValue = PACKAGED_ROUTES.filter((route) =>
		['/', '/players', '/library'].includes(route.path)
	);
	for (const theme of ['light', 'dark']) {
		const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
		try {
			await context.addInitScript((mode) => {
				try {
					localStorage.setItem('carbon-theme', mode);
				} catch {
					// The theme check reports the effective theme from the DOM.
				}
			}, theme);
			const page = await context.newPage();
			await seedPackagedRoutes(page, { servers: 'populated', detail: 'populated' }, 'anonymous');
			for (const width of [1440, 360]) {
				await page.setViewportSize({ width, height: 900 });
				for (const route of highValue) {
					await page.goto(root + route.path, { waitUntil: 'domcontentloaded' });
					await page.getByRole('heading', { name: route.h1, level: 1 }).waitFor();
					const state = await page.evaluate(() => {
						const html = document.documentElement;
						const active = document.querySelector('a[href], button:not([disabled]), input:not([disabled]), select');
						const style = active ? getComputedStyle(active) : null;
						return {
							scrollWidth: html.scrollWidth,
							innerWidth: window.innerWidth,
							carbonTheme: html.getAttribute('data-carbon-theme'),
							dark: html.classList.contains('dark'),
							focusStyle: style
								? { outlineStyle: style.outlineStyle, outlineWidth: style.outlineWidth }
								: null
						};
					});
					await check(`smoke ${engine} ${route.path} @${width} ${theme}: reflow and theme`, async () => {
						if (state.scrollWidth > state.innerWidth + 1) {
							throw new Error(`overflow ${state.scrollWidth} > ${state.innerWidth}`);
						}
						if (state.carbonTheme !== theme || state.dark !== (theme === 'dark')) {
							throw new Error(`effective theme ${JSON.stringify(state)}, expected ${theme}`);
						}
						return state;
					});
				}
			}
		} finally {
			await context.close();
		}
	}
}

const engine = requestedEngine();
const dashboardOnly = process.argv.includes('--dashboard-telemetry-only');
const shellOnly = process.argv.includes('--shell-only');
const fixture = dashboardOnly || shellOnly ? null : startFixture();
const packaged = startPackagedServer();
let browser;
try {
	const [fixtureBase, packagedBase] = await Promise.all([fixture?.url, packaged.url]);
	const launched = await launchEngine(engine);
	browser = launched.browser;
	console.log(JSON.stringify({ check: 'a11y-browser', ...launched.metadata }));
	if (shellOnly) {
		await runShellJourney(browser, packagedBase);
	} else if (dashboardOnly) {
		await runDashboardTelemetry(browser, packagedBase);
	} else if (engine !== 'chromium') {
		await runCrossEngineSmoke(browser, packagedBase, engine);
	} else {
		await runDashboardTelemetry(browser, packagedBase);
		await runComponentJourney(browser, fixtureBase);
		await runShellJourney(browser, packagedBase);
		await runPackagedRouteJourney(browser, packagedBase, 'light');
		for (const theme of ['light', 'dark']) {
			for (const width of [360, 768, 1440]) {
				await runModpacksJourney(browser, packagedBase, theme, width);
			}
		}
		await runPackagedRouteJourney(browser, packagedBase, 'dark');
		await runClippingProbe(browser);
		await runNarrowSettingsJourney(browser, packagedBase);
		await check('route themes: light and dark paint different backgrounds', async () => {
			if (!themeBackgrounds.light || !themeBackgrounds.dark) {
				throw new Error('theme backgrounds were not captured');
			}
			if (themeBackgrounds.light === themeBackgrounds.dark) {
				throw new Error(`identical backgrounds: ${themeBackgrounds.light}`);
			}
			return themeBackgrounds;
		});
	}
} catch (error) {
	failures += 1;
	checks.push({ name: 'journey-setup', status: 'failed', detail: String(error?.message ?? error) });
	console.error(`FAIL journey-setup: ${error?.message ?? error}`);
} finally {
	// Always stop both servers even if browser shutdown fails.
	try {
		if (browser) await browser.close();
	} finally {
		fixture?.child.kill('SIGTERM');
		packaged.child.kill('SIGTERM');
	}
	console.log(JSON.stringify({ check: 'a11y-browser-summary', total: checks.length, failures, checks }));
	process.exitCode = failures === 0 ? 0 : 1;
}
