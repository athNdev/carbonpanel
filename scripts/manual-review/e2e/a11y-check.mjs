#!/usr/bin/env node
// SPDX-License-Identifier: MPL-2.0
//
// Real-Chromium keyboard/accessibility journey over the shared fixture that
// mounts the actual Carbon components. Uses the existing owned review browser
// (scripts/review/browser.mjs) and the locked Playwright package in this
// directory. Nothing here claims coverage of Firefox, WebKit or mobile
// engines: the only engine exercised is Chromium.
//
// Usage: node a11y-check.mjs
//   REVIEW_CHROME may point at an explicit owned Chromium executable.

import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
import { launchReviewBrowser } from '../../review/browser.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const repo = resolve(here, '../../..');
const webDir = resolve(repo, 'web/carbon-panel');

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

function startFixture() {
	const child = spawn('bun', ['test/a11y-fixture/serve.mjs'], {
		cwd: webDir,
		stdio: ['ignore', 'pipe', 'pipe']
	});
	const url = new Promise((resolveUrl, reject) => {
		const timer = setTimeout(() => reject(new Error('Fixture server did not start in 30s')), 30000);
		let stderr = '';
		child.stderr.on('data', (chunk) => (stderr += chunk.toString()));
		child.stdout.on('data', (chunk) => {
			const match = chunk.toString().match(/A11Y_FIXTURE_URL=(\S+)/);
			if (match) {
				clearTimeout(timer);
				resolveUrl(match[1].trim());
			}
		});
		child.once('exit', (code) => {
			clearTimeout(timer);
			reject(new Error(`Fixture server exited ${code}: ${stderr.slice(0, 500)}`));
		});
	});
	return { child, url };
}

const fixture = startFixture();
let browser;
try {
	const base = await fixture.url;
	const launched = await launchReviewBrowser();
	browser = launched.browser;
	console.log(JSON.stringify({ check: 'a11y-browser', ...launched.metadata }));

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
} catch (error) {
	failures += 1;
	checks.push({ name: 'journey-setup', status: 'failed', detail: String(error?.message ?? error) });
	console.error(`FAIL journey-setup: ${error?.message ?? error}`);
} finally {
	// Always stop the fixture even if browser shutdown fails.
	try {
		if (browser) await browser.close();
	} finally {
		fixture.child.kill('SIGTERM');
	}
	console.log(JSON.stringify({ check: 'a11y-browser-summary', total: checks.length, failures, checks }));
	process.exitCode = failures === 0 ? 0 : 1;
}
