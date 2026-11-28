// SPDX-License-Identifier: MPL-2.0
// Real-DOM coverage for the auth settings session-timeout field: clearing the
// field must surface the existing validation state, not throw a page error.

import test from 'node:test';
import assert from 'node:assert/strict';
// @ts-expect-error -- Bun supplies this test-only module; the app tsconfig omits Bun types.
import { mock } from 'bun:test';
import { readable } from 'svelte/store';
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

mock.module('$lib/api/rpc-client', () => ({
	rpcClient: {
		auth: {
			getAuthConfig: async () => authConfig,
			updateAuthSettings: async () => ({ config: authConfig })
		}
	},
	silentCallOptions: {}
}));

const authStoreValue = { canLinkOidc: false, user: null, permissions: [] };
mock.module('$lib/stores/auth', () => ({
	authStore: Object.assign(readable(authStoreValue), { getSessionRevision: () => 0 }),
	currentUser: readable(null),
	canAccessSettings: readable(true),
	canUpdateSettings: readable(true)
}));

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
