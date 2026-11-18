// SPDX-License-Identifier: MPL-2.0
import { writable } from 'svelte/store';

const store = writable({ user: null, isAuthenticated: true, permissions: [] });

const state = { permissions: [] };

function hasPermission(resource, action, objectId) {
	return state.permissions.some(
		(p) =>
			(p.resource === '*' || p.resource === resource) &&
			(p.action === '*' || p.action === action) &&
			(p.objectId === '*' || p.objectId === objectId)
	);
}

export const authStore = {
	subscribe: store.subscribe,
	hasPermission,
	setPermissions(permissions) {
		state.permissions = permissions;
		store.update((s) => ({ ...s, permissions: [...permissions] }));
	}
};

globalThis.__qaAuth = {
	set(permissions) {
		authStore.setPermissions(permissions);
	}
};
