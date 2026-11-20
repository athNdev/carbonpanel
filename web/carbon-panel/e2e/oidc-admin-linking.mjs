// SPDX-License-Identifier: MPL-2.0
// Run with the repository review browser and the production frontend build.
// node web/carbon-panel/e2e/oidc-admin-linking.mjs
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { createInterface } from 'node:readline';
import { fileURLToPath } from 'node:url';
import { launchReviewBrowser } from '../../../scripts/review/browser.mjs';

const serverPath = fileURLToPath(new URL('../test/a11y-routes/serve.mjs', import.meta.url));
const server = spawn(process.execPath, [serverPath], { stdio: ['ignore', 'pipe', 'inherit'] });
const serverExit = once(server, 'exit');
let browser;
try {
	const output = createInterface({ input: server.stdout });
	const ready = await Promise.race([
		once(output, 'line', { signal: AbortSignal.timeout(30_000) }).then(([line]) => line),
		serverExit.then(() => '')
	]);
	output.close();
	const origin = /^A11Y_ROUTES_URL=(\S+)$/.exec(ready)?.[1];
	assert.ok(origin, 'The packaged route server did not publish its loopback URL');
	({ browser } = await launchReviewBrowser());
	const context = await browser.newContext();
	const page = await context.newPage();
	page.setDefaultTimeout(90_000);
	const errors = [];
	page.on('pageerror', (error) => errors.push(error.message));

	// Seed only the network boundary. The app logs in, calls its generated Connect
	// client, navigates with the browser cookie and renders the returned status.
	const user = { id: 'fixture', username: 'fixture-admin', authProvider: 'local', isActive: true };
	const token = 'fixture-local-session';
	let linkAttempts = 0;
	let callbackAttempts = 0;
	let linked = false;
	await context.route(/\/carbonpanel\.v1\.[^/]+\/[^/?]+/, async (route) => {
		const request = route.request();
		const method = new URL(request.url()).pathname.split('/').at(-1);
		const reply = (body, headers) =>
			route.fulfill({ status: 200, contentType: 'application/json', headers, json: body });
		switch (method) {
			case 'GetAuthStatus':
				return reply({ localAuthEnabled: true, oidcEnabled: true });
			case 'Login':
				return reply({ user, token });
			case 'GetCurrentUser':
				if ((await request.headerValue('authorization')) !== `Bearer ${token}`) return reply({});
				return reply({
					user,
					permissions: [{ resource: '*', action: '*', objectId: '*' }],
					canLinkOidc: !linked
				});
			case 'GetAuthConfig':
				return reply({ localAuthEnabled: true, oidcEnabled: true, sessionTimeout: 86400 });
			case 'BeginOIDCLink':
				assert.equal(await request.headerValue('authorization'), `Bearer ${token}`);
				assert.deepEqual(request.postDataJSON(), {});
				linkAttempts++;
				return reply(
					{ linkToken: `fixture-link-${linkAttempts}` },
					{ 'set-cookie': 'fixture_binding=owned; Path=/api/v1/auth/oidc; HttpOnly; SameSite=Lax' }
				);
			default:
				return reply({});
		}
	});
	await context.route(/\/api\/v1\/auth\/oidc\/login\?/, async (route) => {
		const request = route.request();
		assert.equal(
			new URL(request.url()).searchParams.get('link_token'),
			`fixture-link-${linkAttempts}`
		);
		assert.match((await request.headerValue('cookie')) ?? '', /fixture_binding=owned/);
		callbackAttempts++;
		linked = callbackAttempts === 2;
		await route.fulfill({
			status: 302,
			headers: {
				location: `/settings?tab=auth&oidc_link=${linked ? 'success' : 'access_denied'}`
			}
		});
	});

	await page.goto(new URL('login', origin).href);
	await page.getByLabel('Username', { exact: true }).fill(user.username);
	await page.getByLabel('Password', { exact: true }).fill('disposable-fixture-password');
	await page.getByLabel('Password', { exact: true }).press('Enter');
	await page.getByRole('link', { name: 'Settings', exact: true }).click();
	await page.getByRole('tab', { name: 'Auth', exact: true }).click();
	const link = page.getByRole('button', { name: 'Link SSO account', exact: true });

	await link.click();
	await page
		.getByRole('alert')
		.filter({ hasText: 'The identity provider denied access.' })
		.waitFor();
	assert.equal(new URL(page.url()).searchParams.has('oidc_link'), false);
	await link.click();
	await page.getByRole('status').filter({ hasText: 'SSO is linked to this account.' }).waitFor();
	await link.waitFor({ state: 'hidden' });
	assert.equal(new URL(page.url()).searchParams.has('oidc_link'), false);
	assert.equal(await page.evaluate(() => localStorage.getItem('auth_token')), token);
	assert.equal(linkAttempts, 2);
	assert.equal(callbackAttempts, 2);
	assert.deepEqual(errors, []);
	console.log(
		'PASS: Local admin linking sends the browser binding, handles denial, and keeps the local session on success.'
	);
} finally {
	await browser?.close();
	server.kill();
	await serverExit;
}
