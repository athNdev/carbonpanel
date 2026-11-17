// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const shell = readFileSync(
	new URL('../../lib/components/carbon/CarbonShell.svelte', import.meta.url),
	'utf8'
);
const prefs = readFileSync(
	new URL('../../lib/components/PreferencesDialog.svelte', import.meta.url),
	'utf8'
);

test('profile menu opens popups instead of just closing', () => {
	assert.ok(shell.includes('openProfileDialog'), 'Profile menu must open the profile dialog');
	assert.ok(
		shell.includes('openPreferencesDialog'),
		'Preferences menu must open the preferences dialog'
	);
	assert.ok(shell.includes('handleSignOut'), 'Sign out must call the sign-out handler');
	assert.doesNotMatch(
		shell,
		/>\s*Profile\s*<\/button>\s*\n\s*<button[^>]*onclick=\{closeProfile\}/,
		'Profile entry must not just close the menu'
	);
});

test('sign out goes through authStore.logout', () => {
	assert.ok(shell.includes('authStore.logout'), 'sign-out must call authStore.logout');
});

test('profile dialog shows a read-only summary with a /profile link', () => {
	assert.ok(shell.includes('profileDialogOpen'), 'shell must own the profile dialog state');
	assert.ok(shell.includes("resolve('/profile')"), 'profile dialog must link to /profile');
	assert.ok(shell.includes('Manage profile'), 'profile dialog must offer profile management');
	assert.ok(shell.includes('Roles'), 'profile dialog must summarize roles');
});

test('profile dialog changes passwords via authStore.changePassword', () => {
	assert.ok(
		shell.includes('authStore.changePassword'),
		'profile dialog must change passwords via authStore.changePassword'
	);
	assert.ok(shell.includes('Update password'), 'profile dialog must offer a password update');
});

test('preferences dialog delegates storage and attributes to the shared owner', () => {
	assert.ok(
		prefs.includes('displayPreferences'),
		'dialog must consume the shared preference owner'
	);
	for (const setter of ['setTheme', 'setMotion', 'setDensity', 'reset']) {
		assert.ok(
			prefs.includes(`displayPreferences.${setter}`),
			`dialog must call ${setter} on the owner`
		);
	}
	assert.ok(prefs.includes('data-carbon-theme'), 'shared owner applies data-carbon-theme');
	assert.ok(prefs.includes('CarbonModal'), 'preferences must open via CarbonModal');
	assert.ok(prefs.includes('Reset defaults'), 'preferences must offer a reset');
	assert.doesNotMatch(prefs, /localStorage/, 'storage belongs to the owner, not the dialog');
});

test('preferences dialog offers light/dark without a backend', () => {
	assert.ok(prefs.includes('"dark"') || prefs.includes("'dark'"), 'dark theme must exist');
	assert.ok(prefs.includes('"light"') || prefs.includes("'light'"), 'light theme must exist');
	assert.doesNotMatch(prefs, /fetch\(|rpcClient/, 'preferences must not call any backend');
});

test('Done applies the choices and closes the dialog', () => {
	assert.match(
		prefs,
		/function handleClose\(\) \{[^}]*open = false/s,
		'Done must close the dialog, not just persist'
	);
});

test('shell hydrates shared preferences on boot', () => {
	assert.ok(
		shell.includes('displayPreferences.hydrate()'),
		'shell must hydrate the shared owner on mount'
	);
	assert.doesNotMatch(
		shell,
		/localStorage\.getItem\('carbon/,
		'shell must not hydrate preferences independently of the owner'
	);
});

test('shell hosts both popups', () => {
	assert.ok(shell.includes('PreferencesDialog'), 'shell must render the PreferencesDialog popup');
	assert.ok(shell.includes('bind:open={prefsOpen}'), 'preferences open state must be bound');
	assert.ok(shell.includes('bind:open={profileDialogOpen}'), 'profile open state must be bound');
});
