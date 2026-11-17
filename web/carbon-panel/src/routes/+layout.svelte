<!--
SPDX-License-Identifier: MIT AND MPL-2.0
This file includes DiscoPanel material under the MIT License,
Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
and still applies to that inherited material.
Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
If a copy of the MPL was not distributed with this file, You can obtain
one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import '../app.css';
	import CarbonShell from '$lib/components/carbon/CarbonShell.svelte';
	import { ModeWatcher } from 'mode-watcher';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { resolve as resolvePath } from '$app/paths';
	import { get } from 'svelte/store';
	import { serverInventory } from '$lib/stores/servers';
	import { authStore } from '$lib/stores/auth';
	import { onMount } from 'svelte';
	import { onNavigate } from '$app/navigation';
	import { Toaster } from '$lib/components/ui/sonner';
	import GlobalLoading from '$lib/components/global-loading.svelte';
	import * as Tooltip from '$lib/components/ui/tooltip/index.js';
	import { prefersReducedMotion } from '$lib/motion';

	let { children } = $props();

	let loading = $state(true);

	let activeViewTransition: ViewTransition | null = null;

	// Animate only client-side route changes. Resolve after SvelteKit updates the
	// DOM so the browser captures the correct new view; skip and interrupt
	// animation for users who request reduced motion or navigate again.
	onNavigate(() => {
		if (!document.startViewTransition || prefersReducedMotion()) {
			activeViewTransition?.skipTransition();
			activeViewTransition = null;
			return;
		}

		activeViewTransition?.skipTransition();

		let finishTransition!: () => void;
		const updated = new Promise<void>((resolve) => {
			finishTransition = resolve;
		});
		const next = document.startViewTransition(() => updated);
		activeViewTransition = next;
		void next.finished.finally(() => {
			if (activeViewTransition === next) {
				activeViewTransition = null;
			}
		});
		return () => finishTransition();
	});

	async function initialize() {
		try {
			const authStatus = await authStore.checkAuthStatus();
			loading = false;

			let shouldFetch = true;
			if (authStatus.enabled) {
				if (authStatus.firstUserSetup) {
					goto(resolvePath('/login'));
					shouldFetch = false;
				} else {
					const isValid = await authStore.validateSession();
					if (!isValid) {
						// If anonymous access is enabled, allow browsing without login
						const state = get(authStore);
						if (!state.anonymousAccessEnabled) {
							goto(resolvePath('/login'));
							shouldFetch = false;
						}
					}
				}
			}

			if (!shouldFetch || page.url.pathname === '/login') {
				return;
			}
		} catch (err) {
			// Never reject the mount lifecycle: that would skip cleanup and leak
			// the polling interval.
			console.debug(`Carbon Panel caught a polling error: ${err}`);
			loading = false;
		}
	}

	onMount(() => {
		initialize();
	});

	$effect(() => {
		if (loading) return;
		const auth = $authStore;
		if (
			page.url.pathname === '/login' ||
			auth.isLoading ||
			auth.firstUserSetup ||
			((auth.localAuthEnabled || auth.oidcEnabled) &&
				!auth.isAuthenticated &&
				!auth.anonymousAccessEnabled)
		) {
			serverInventory.reset();
			return;
		}
		void serverInventory
			.refresh()
			.catch((error) => console.debug('Inventory hydration failed:', error));
		return serverInventory.watch();
	});
</script>

<svelte:head>
	<title>Carbon Panel - Minecraft Server Management</title>
</svelte:head>

<!--
	Global Tooltip.Provider.

	`Tooltip.Root` reads the tooltip context via `getContext`, which THROWS
	`Context "Tooltip.Provider" not found` when no provider is an ancestor. The
	only provider in the tree lived inside the shadcn sidebar provider, so any
	`Tooltip.Root` rendered outside the sidebar threw during render.

	server-console.svelte uses `Tooltip.Root`, and the server detail page uses
	the Carbon shell rather than the shadcn sidebar, so it had no provider. The
	throw aborted Svelte's whole effect-flush batch: the tab indicator still
	moved to "Console" while the panel below kept rendering the previous tab.
	That is precisely the long-reported "Console tab is selected but shows the
	Settings panel" defect.

	Providing it at the layout root fixes the class of bug, not just this one
	instance. The sidebar provider nests its own inside this one and still wins
	for its subtree.
-->
<Tooltip.Provider>
	<ModeWatcher />
	<Toaster position="bottom-center" expand={true} richColors />
	<GlobalLoading />

	{#if page.url.pathname === '/login'}
		{@render children?.()}
	{:else if loading}
		<div class="flex min-h-screen items-center justify-center bg-[#161616]" role="status">
			<div class="h-10 w-10 animate-spin border-4 border-[#0f62fe] border-t-transparent"></div>
			<span class="sr-only">Loading Carbon Panel…</span>
		</div>
	{:else}
		<CarbonShell>
			{@render children?.()}
		</CarbonShell>
	{/if}
</Tooltip.Provider>
