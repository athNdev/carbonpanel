// SPDX-License-Identifier: MPL-2.0
// Real-DOM coverage for the auth settings session-timeout field: clearing the
// field must surface the existing validation state, not throw a page error.

import test, { after } from 'node:test';
import assert from 'node:assert/strict';
// @ts-expect-error -- Bun supplies this test-only module; the app tsconfig omits Bun types.
import { mock } from 'bun:test';
import '../../../test/dom-setup.mjs';

mock.module('$app/navigation', () => ({
	goto: () => Promise.resolve(),
	invalidateAll: () => Promise.resolve(),
	onNavigate: () => {},
	beforeNavigate: () => {},
	afterNavigate: () => {},
	pushState: () => {},
	replaceState: () => {},
	preloadData: () => Promise.resolve()
}));
mock.module('$app/paths', () => ({
	resolve: (path: string) => path,
	asset: (path: string) => path
}));
mock.module('$app/env', () => ({ browser: true }));
mock.module('$app/state', () => ({
	page: { url: new URL('http://localhost/'), params: {}, data: {}, state: {} }
}));

const errorToasts: string[] = [];
mock.module('svelte-sonner', () => ({
	toast: {
		error: (message: unknown) => errorToasts.push(String(message)),
		success: () => {},
		info: () => {}
	}
}));

const authConfig = {
	localAuthEnabled: true,
	allowRegistration: false,
	anonymousAccess: false,
	sessionTimeout: 86400,
	oidcEnabled: true
};

const updateRequests: Array<{ sessionTimeout?: number }> = [];

// Keep the real RPC singleton and auth store, replacing only the methods and
// state this test drives. Module-level mocks are process-global in Bun and
// would otherwise leak into later test files.
const { rpcClient } = await import('$lib/api/rpc-client');
const { authStore } = await import('$lib/stores/auth');
const authClient = rpcClient.auth as unknown as {
	getAuthConfig: () => Promise<typeof authConfig>;
	updateAuthSettings: (req: { sessionTimeout?: number }) => Promise<{ config: typeof authConfig }>;
	getAuthStatus: () => Promise<{
		localAuthEnabled: boolean;
		oidcEnabled: boolean;
		firstUserSetup: boolean;
		allowRegistration: boolean;
		anonymousAccessEnabled: boolean;
	}>;
	getCurrentUser: () => Promise<{
		user: null;
		permissions: Array<{ resource: string; action: string }>;
		canLinkOidc: boolean;
	}>;
};
const realAuthMethods = {
	getAuthConfig: authClient.getAuthConfig.bind(authClient),
	updateAuthSettings: authClient.updateAuthSettings.bind(authClient),
	getAuthStatus: authClient.getAuthStatus.bind(authClient),
	getCurrentUser: authClient.getCurrentUser.bind(authClient)
};
Object.assign(authClient, {
	getAuthConfig: async () => authConfig,
	updateAuthSettings: async (req: { sessionTimeout?: number }) => {
		updateRequests.push(req);
		return { config: authConfig };
	},
	getAuthStatus: async () => ({
		localAuthEnabled: false,
		oidcEnabled: false,
		firstUserSetup: false,
		allowRegistration: false,
		anonymousAccessEnabled: true
	}),
	getCurrentUser: async () => ({
		user: null,
		permissions: [{ resource: 'settings', action: 'update' }],
		canLinkOidc: false
	})
});

// The component gates its editors on canUpdateSettings; load the real store
// with the settings-update permission instead of replacing the module.
await authStore.checkAuthStatus();

after(async () => {
	try {
		await authStore.logout();
	} finally {
		Object.assign(authClient, realAuthMethods);
	}
});

const { mount, unmount } = await import('svelte');
const { default: AuthSettings } = await import('./auth-settings.svelte');

const flush = async (ms = 80) => {
	await new Promise((resolve) => setTimeout(resolve, ms));
};

function mountUi() {
	const target = document.createElement('div');
	document.body.appendChild(target);
	const instance = mount(AuthSettings as never, { target });
	return {
		target,
		unmount() {
			unmount(instance);
			target.remove();
		}
	};
}

test('clearing the session timeout shows validation instead of throwing', async () => {
	errorToasts.length = 0;
	const pageErrors: unknown[] = [];
	const onError = (event: ErrorEvent) => pageErrors.push(event.error ?? event.message);
	window.addEventListener('error', onError);
	const ui = mountUi();
	try {
		await flush();
		const input = ui.target.querySelector<HTMLInputElement>('#session-timeout-hours');
		assert.ok(input, 'the editable session timeout field must render for settings editors');
		assert.equal(input.value, '24', 'the timeout loads as whole hours');

		input.value = '';
		input.dispatchEvent(new Event('input', { bubbles: true }));
		await flush();

		assert.deepEqual(pageErrors, [], 'clearing the field must not throw a page error');
		assert.equal(
			ui.target.querySelector('#session-timeout-hours-desc')?.textContent?.trim(),
			'Enter the timeout in hours',
			'the existing validation message must explain the empty field'
		);
		const save = Array.from(ui.target.querySelectorAll('button')).find((button) =>
			button.textContent?.includes('Save Changes')
		);
		assert.ok(save, 'the save button must stay rendered');
		assert.equal(save.disabled, true, 'an invalid timeout must keep saving disabled');
	} finally {
		window.removeEventListener('error', onError);
		ui.unmount();
	}
});

test('typing a session timeout parses as hours instead of throwing', async () => {
	errorToasts.length = 0;
	updateRequests.length = 0;
	const pageErrors: unknown[] = [];
	const onError = (event: ErrorEvent) => pageErrors.push(event.error ?? event.message);
	window.addEventListener('error', onError);
	const ui = mountUi();
	try {
		await flush();
		const input = ui.target.querySelector<HTMLInputElement>('#session-timeout-hours');
		assert.ok(input, 'the editable session timeout field must render for settings editors');
		assert.equal(input.value, '24', 'the timeout loads as whole hours');

		input.value = '12';
		input.dispatchEvent(new Event('input', { bubbles: true }));
		await flush();

		assert.deepEqual(pageErrors, [], 'typing a number must not throw a page error');
		assert.equal(input.value, '12', 'the typed timeout must stay on screen');
		assert.equal(
			ui.target.querySelector('#session-timeout-hours-desc'),
			null,
			'a valid timeout must clear the validation message'
		);
		const save = Array.from(ui.target.querySelectorAll('button')).find((button) =>
			button.textContent?.includes('Save Changes')
		);
		assert.ok(save, 'the save button must stay rendered');
		assert.equal(save.disabled, false, 'a changed, valid timeout must allow saving');

		save.click();
		await flush();
		assert.equal(updateRequests.length, 1, 'saving must send one update');
		assert.equal(
			updateRequests[0].sessionTimeout,
			12 * 3600,
			'the typed 12 hours must parse to 43200 seconds'
		);
	} finally {
		window.removeEventListener('error', onError);
		ui.unmount();
	}
});
