// SPDX-License-Identifier: MPL-2.0
import { mount } from 'svelte';
import App from './App.svelte';
import { authStore } from '$lib/stores/auth';
import '$lib/../app.css';

authStore.setPermissions([
	{ resource: 'players', action: 'read', objectId: '*' },
	{ resource: 'players', action: 'access:write', objectId: 'srv-1' }
]);

mount(App, { target: document.getElementById('app') });
