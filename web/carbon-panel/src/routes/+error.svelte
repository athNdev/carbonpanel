<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { CarbonButton } from '$lib/components/carbon';
	import { RefreshCw, Home } from '@lucide/svelte';

	// A client-side error swap replaces the page without moving focus, so a
	// keyboard or screen-reader user stays on the trigger and never hears the
	// new heading. Focus it explicitly on mount.
	let heading = $state<HTMLHeadingElement | null>(null);
	onMount(() => heading?.focus());
</script>

<div class="flex min-h-[60vh] flex-col items-center justify-center gap-4 p-8 text-center">
	<h1
		bind:this={heading}
		tabindex="-1"
		class="text-2xl font-semibold text-foreground focus:outline-none"
	>
		{page.status === 404 ? 'Page not found' : 'Page failed to load'}
	</h1>
	<p class="max-w-md text-sm text-muted-foreground">
		{page.error?.message ?? 'An unexpected error occurred while loading this page.'}
	</p>
	<div class="flex gap-2">
		<CarbonButton kind="secondary" size="sm" onclick={() => location.reload()}>
			<RefreshCw class="h-4 w-4" strokeWidth={1.5} />
			<span>Retry</span>
		</CarbonButton>
		<CarbonButton kind="primary" size="sm" href={resolve('/')}>
			<Home class="h-4 w-4" strokeWidth={1.5} />
			<span>Home</span>
		</CarbonButton>
	</div>
</div>
