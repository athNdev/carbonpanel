<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import type { Snippet } from 'svelte';
	import { Inbox } from '@lucide/svelte';
	import CarbonButton from './CarbonButton.svelte';

	interface Props {
		title?: string;
		description?: string;
		icon?: Snippet;
		actionLabel?: string;
		onaction?: () => void;
		class?: string;
	}

	let {
		title = 'Nothing to show',
		description = '',
		icon,
		actionLabel,
		onaction,
		class: className = ''
	}: Props = $props();
</script>

<div
	class="flex flex-col items-center justify-center gap-4 border border-border bg-card p-12 text-center {className}"
>
	<div
		class="flex h-12 w-12 items-center justify-center border border-border bg-background text-muted-foreground"
	>
		{#if icon}
			{@render icon()}
		{:else}
			<Inbox class="h-6 w-6" strokeWidth={1.5} />
		{/if}
	</div>
	<div class="space-y-1">
		<h3 class="text-sm font-semibold text-foreground">{title}</h3>
		{#if description}
			<p class="max-w-md text-xs text-muted-foreground">{description}</p>
		{/if}
	</div>
	{#if actionLabel && onaction}
		<CarbonButton kind="primary" size="sm" onclick={onaction}>{actionLabel}</CarbonButton>
	{/if}
</div>
