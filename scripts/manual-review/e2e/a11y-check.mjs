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
	'Overview',
	/^Servers\b/,
	'Create Server'
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
		properties: [{ key: 'port', label: 'Game port', value: '25565', type: 'number' }]
	}
];

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
							permissions: [{ resource: '*', action: '*', objectId: '*' }]
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
			const server = FLEET.find((server) => server.id === 'srv-1');
			return reply({
				server: state.narrowSettings
					? { ...server, nodeId: 'local', containerId: 'fixture-container', mcVersion: '1.21.1' }
					: server
			});
		}
		if (state.narrowSettings) {
			if (method === 'ListNodes')
				return reply({
					nodes: [
						{ id: 'local', name: 'Local Controller', isLocal: true, enabled: true, status: 1 },
						{ id: 'target', name: SETTINGS_NODE, enabled: true, status: 1, availableMemoryMb: '8192' }
					]
				});
			if (method === 'GetGlobalSettings') return reply({ categories: SETTINGS_CATEGORIES });
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
		if (method === 'ListNodes') return reply({ nodes: [] });
		if (method === 'ListPlayers') {
			return reply({ players: PLAYERS, sourceDetail: 'Synthetic a11y route fixture' });
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
			['textbox', 'Exact UUID or username'],
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
		h1: 'Worlds',
		controls: [
			['heading', 'Worlds'],
			['textbox', 'Search viewable worlds'],
			['link', 'Renderer modules'],
			['button', 'Refresh'],
			['link', 'Alpha Overworld']
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
			['button', 'Copy Address']
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

async function runPackagedRouteJourney(browser, base, theme = 'light') {
	const root = base.replace(/\/$/, '');
	const state = { servers: 'populated', detail: 'populated' };
	const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
	// `carbon-theme` is the app's persisted preference key (see
	// display-preferences.svelte.ts) and drives `data-carbon-theme` on <html>.
	// mode-watcher-mode is also set so the two theme systems stay consistent.
	await context.addInitScript((mode) => {
		try {
			localStorage.setItem('carbon-theme', mode);
			localStorage.setItem('mode-watcher-mode', mode);
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
			}

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
			await check(`route /servers/srv-1 @${width} ${theme}: tabs roles, names and arrow-key navigation`, async () => {
				const semantics = await page.evaluate(() => {
					const list = document.querySelector('[role="tablist"]');
					const tabs = [...document.querySelectorAll('[role="tab"]')];
					return {
						label: list?.getAttribute('aria-label'),
						count: tabs.length,
						first: tabs[0]?.id,
						selected: tabs.filter((tab) => tab.getAttribute('aria-selected') === 'true').map((tab) => tab.id),
						panels: document.querySelectorAll('[role="tabpanel"]').length
					};
				});
				if (
					semantics.label !== 'Sections' ||
					semantics.count < 10 ||
					semantics.first !== 'server-detail-tabs-tab-overview' ||
					semantics.selected.length !== 1 ||
					semantics.selected[0] !== 'server-detail-tabs-tab-overview' ||
					semantics.panels !== 1
				) {
					throw new Error(`tab semantics incomplete: ${JSON.stringify(semantics)}`);
				}
				await overviewTab.focus();
				await page.keyboard.press('ArrowRight');
				const focused = await page.evaluate(() => document.activeElement?.id);
				if (focused !== 'server-detail-tabs-tab-console') {
					throw new Error(`ArrowRight did not move tab focus (got ${focused})`);
				}
				const selected = await page.evaluate(() =>
					[...document.querySelectorAll('[role="tab"]')]
						.filter((tab) => tab.getAttribute('aria-selected') === 'true')
						.map((tab) => tab.id)
				);
				if (selected.length !== 1 || selected[0] !== 'server-detail-tabs-tab-console') {
					throw new Error(`ArrowRight did not move tab selection: ${JSON.stringify(selected)}`);
				}
				await page.keyboard.press('ArrowLeft');
				return { ...semantics, focused, selectedAfter: selected };
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
				await page.keyboard.press('ArrowLeft');
				return { initial, focusedAfterArrowRight: focused, after };
			});
      await page.goto(root + "/servers/srv-1", {
        waitUntil: "domcontentloaded",
      });
      await page.locator("#server-detail-tabs-tab-world").click();
      await page.locator("[data-server-tab-panel]").waitFor();		}

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
				await page.getByRole('button', { name: 'Sign In', exact: true }).waitFor();
				const metrics = await page.evaluate(() => ({
					scrollWidth: document.documentElement.scrollWidth,
					innerWidth: window.innerWidth
				}));
				if (metrics.scrollWidth > metrics.innerWidth + 1) {
					throw new Error(`login overflow ${metrics.scrollWidth} > ${metrics.innerWidth}`);
				}
				await page.getByLabel('Username', { exact: true }).fill('review');
				await page.getByLabel('Password', { exact: true }).fill('wrong-password');
				await page.getByRole('button', { name: 'Sign In', exact: true }).click();
				const announced = page
					.getByRole('alert')
					.filter({ hasText: 'Invalid username or password' });
				await announced.first().waitFor();
				if (!(await announced.first().evaluate((node) => node === document.activeElement))) {
					throw new Error('Failed sign-in did not focus the error message');
				}
				const recovery = page.getByRole('button', { name: 'Forgot access? Recovery' });
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
async function runModpacksJourney(browser, base, theme = 'light') {
	const root = base.replace(/\/$/, '');
	const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
	await context.addInitScript((mode) => {
		try {
			localStorage.setItem('carbon-theme', mode);
			localStorage.setItem('mode-watcher-mode', mode);
		} catch {
			// The per-route theme check reports the effective theme from the DOM.
		}
	}, theme);
	try {
		const page = await context.newPage();
		const state = { servers: 'populated', detail: 'populated', modpacks: 'populated' };
		await seedPackagedRoutes(page, state, 'anonymous');

		await check(`modpacks ${theme}: library shell and tab controls render`, async () => {
			await page.goto(root + '/modpacks', { waitUntil: 'domcontentloaded' });
			await page.getByRole('heading', { name: 'Modpacks', level: 1 }).waitFor();
			await page.getByText('Browse, import and manage modpack packages').first().waitFor();
			await page.getByTestId('showcase-modpacks-content').first().waitFor();
			for (const tab of ['Browse', 'Imported', 'Favorites']) {
				await page.getByRole('tab', { name: new RegExp(tab) }).first().waitFor({ timeout: 10000 });
			}
			await page.getByPlaceholder('Search modpacks by name or description...').waitFor();
			return 'description, content area, three tabs and search control visible';
		});

		await check(`modpacks ${theme}: browse filters have accessible names on desktop and mobile`, async () => {
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

		await check(`modpacks ${theme}: seeded catalog renders from the RPC boundary`, async () => {
			await page.getByTestId('showcase-modpack-card').first().waitFor({ timeout: 10000 });
			return page.getByTestId('showcase-modpack-card').count();
		});

		await check(`modpacks ${theme}: refused search exposes the error state and Retry`, async () => {
			state.modpacks = 'refused';
			await page.getByPlaceholder('Search modpacks by name or description...').fill('refusal-probe');
			await page.getByRole('button', { name: 'Search', exact: true }).first().click();
			await page.getByText('Failed to search modpacks').first().waitFor({ timeout: 10000 });
			await page.getByRole('button', { name: 'Retry', exact: true }).first().waitFor();
			return 'error title and Retry control visible';
		});

		await check(`modpacks ${theme}: Retry reconciles back to the populated catalog`, async () => {
			state.modpacks = 'populated';
			await page.getByRole('button', { name: 'Retry', exact: true }).first().click();
			await page.getByText('Failed to search modpacks').first().waitFor({ state: 'hidden', timeout: 10000 });
			await page.getByTestId('showcase-modpack-card').first().waitFor({ timeout: 10000 });
			return 'error cleared and seeded card restored';
		});

		await check(`modpacks ${theme}: studio library shell controls render`, async () => {
			state.modpacks = 'populated';
			await page.goto(root + '/modpacks/studio', { waitUntil: 'domcontentloaded' });
			await page.getByRole('heading', { name: 'Modpack Studio', level: 1 }).waitFor();
			await page.getByText('Visual workspace for custom modpacks:', { exact: false }).first().waitFor();
			await page.getByRole('button', { name: 'Create Modpack' }).first().waitFor();
			await page.getByRole('button', { name: 'Import Modpack' }).first().waitFor();
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
				localStorage.setItem('mode-watcher-mode', theme);
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
						await page.getByText('Global settings saved successfully', { exact: true }).waitFor();
						if (state.savedMotd !== 'Ready to save') throw new Error('Keyboard Save did not submit MOTD');
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
						await page.goto(root + '/servers/srv-1', { waitUntil: 'domcontentloaded' });
						const target = page.getByRole('combobox', { name: 'Migrate to Target Node' });
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
					localStorage.setItem('mode-watcher-mode', mode);
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
const fixture = dashboardOnly ? null : startFixture();
const packaged = startPackagedServer();
let browser;
try {
	const [fixtureBase, packagedBase] = await Promise.all([fixture?.url, packaged.url]);
	const launched = await launchEngine(engine);
	browser = launched.browser;
	console.log(JSON.stringify({ check: 'a11y-browser', ...launched.metadata }));
	if (dashboardOnly) {
		await runDashboardTelemetry(browser, packagedBase);
	} else if (engine !== 'chromium') {
		await runCrossEngineSmoke(browser, packagedBase, engine);
	} else {
		await runDashboardTelemetry(browser, packagedBase);
		await runComponentJourney(browser, fixtureBase);
		await runPackagedRouteJourney(browser, packagedBase, 'light');
		await runModpacksJourney(browser, packagedBase, 'light');
		await runModpacksJourney(browser, packagedBase, 'dark');
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
