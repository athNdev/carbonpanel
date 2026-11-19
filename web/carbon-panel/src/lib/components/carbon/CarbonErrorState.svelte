<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import { RefreshCw, AlertTriangle } from '@lucide/svelte';
	import CarbonButton from './CarbonButton.svelte';

	interface Props {
		title?: string;
		message?: string;
		retryLabel?: string;
		onretry?: () => void | Promise<void>;
		class?: string;
	}

	let {
		title = 'Something went wrong',
		message = 'An unexpected error occurred. Please try again.',
		retryLabel = 'Retry',
		onretry,
		class: className = ''
	}: Props = $props();

	let retrying = $state(false);

	async function handleRetry() {
		if (!onretry || retrying) return;
		retrying = true;
		try {
			await onretry();
		} finally {
			retrying = false;
		}
	}
</script>

<div
	class="flex flex-col items-center justify-center gap-4 border border-border bg-card p-12 text-center {className}"
	role="alert"
>
	<div
		class="flex h-12 w-12 items-center justify-center border border-destructive/40 bg-destructive/10 text-destructive-text"
	>
		<AlertTriangle class="h-6 w-6" strokeWidth={1.5} />
	</div>
	<div class="space-y-1">
		<h3 class="text-sm font-semibold text-foreground">{title}</h3>
		<p class="max-w-md text-xs text-muted-foreground">{message}</p>
	</div>
	{#if onretry}
		<CarbonButton kind="secondary" size="sm" onclick={handleRetry} loading={retrying}>
			<RefreshCw class="h-3.5 w-3.5" strokeWidth={1.5} />
			<span>{retryLabel}</span>
		</CarbonButton>
	{/if}
</div>
