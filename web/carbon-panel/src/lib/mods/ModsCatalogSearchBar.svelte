<!--
SPDX-License-Identifier: MPL-2.0
-->
<script lang="ts">
	import { Search } from '@lucide/svelte';
	import CarbonButton from '$lib/components/carbon/CarbonButton.svelte';

	interface PlatformOption {
		id: string;
		label: string;
	}

	interface Props {
		/** Platform tabs; empty hides the switcher (the host owns it then). */
		platforms?: PlatformOption[];
		/** Bindable only when tabs are shown. */
		platform?: string;
		/** When provided, tab clicks dispatch here instead of assigning `platform`. */
		onplatformchange?: (id: string) => void;
		query?: string;
		searching?: boolean;
		placeholder?: string;
		searchLabel?: string;
		loadingLabel?: string;
		onsubmit: () => void;
	}

	let {
		platforms = [],
		platform = $bindable('modrinth'),
		onplatformchange = undefined,
		query = $bindable(''),
		searching = false,
		placeholder = 'Search mods...',
		searchLabel = 'Search',
		loadingLabel = 'Searching',
		onsubmit
	}: Props = $props();

	function selectPlatform(id: string) {
		if (onplatformchange) {
			onplatformchange(id);
		} else {
			platform = id;
		}
	}
</script>

<div class="flex flex-wrap items-center gap-2">
	{#if platforms.length > 1}
		<div class="flex items-center" role="group" aria-label="Catalog source">
			{#each platforms as option (option.id)}
				<button
					type="button"
					aria-pressed={platform === option.id}
					onclick={() => selectPlatform(option.id)}
					class="h-8 cursor-pointer rounded-none border px-3 font-mono text-xs uppercase transition-colors {platform ===
					option.id
						? 'border-primary bg-primary text-primary-foreground'
						: 'border-border bg-card text-muted-foreground hover:bg-secondary'}"
				>
					{option.label}
				</button>
			{/each}
		</div>
	{/if}
	<div class="relative min-w-0 flex-1">
		<Search
			class="pointer-events-none absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2 text-muted-foreground"
			aria-hidden="true"
		/>
		<input
			type="search"
			bind:value={query}
			{placeholder}
			onkeydown={(event) => {
				if (event.key === 'Enter') {
					event.preventDefault();
					onsubmit();
				}
			}}
			class="h-9 w-full rounded-none border-b border-border bg-card pr-3 pl-9 text-sm text-foreground placeholder:text-muted-foreground focus:border-b-2 focus:border-primary focus:outline-none"
		/>
	</div>
	<CarbonButton kind="primary" onclick={onsubmit} loading={searching} disabled={searching}>
		{#if searching}{loadingLabel}{:else}{searchLabel}{/if}
	</CarbonButton>
</div>
