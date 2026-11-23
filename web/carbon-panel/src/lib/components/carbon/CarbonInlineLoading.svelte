<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import CarbonSpinner from './CarbonSpinner.svelte';

	interface Props {
		description?: string;
		status?: 'active' | 'finished' | 'error';
		class?: string;
	}

	let { description = 'Loading…', status = 'active', class: className = '' }: Props = $props();
</script>

<div class="inline-flex items-center gap-2 font-sans text-xs text-muted-foreground {className}">
	{#if status === 'active'}
		<!-- text-primary: the root is muted-foreground; the active arc keeps the Carbon blue. -->
		<span class="text-primary">
			<CarbonSpinner size="sm" label="Loading" />
		</span>
	{:else if status === 'finished'}
		<svg
			class="motion-scale-in h-4 w-4 text-success"
			fill="none"
			stroke="currentColor"
			viewBox="0 0 24 24"
		>
			<path
				stroke-linecap="square"
				stroke-linejoin="miter"
				stroke-width="2"
				d="M5 13l4 4L19 7"
				style="stroke-dasharray: 24; animation: carbon-check-draw var(--motion-slow) var(--ease-out-quart) both"
			/>
		</svg>
	{:else}
		<svg
			class="motion-fade-in h-4 w-4 text-destructive"
			fill="none"
			stroke="currentColor"
			viewBox="0 0 24 24"
		>
			<path
				stroke-linecap="square"
				stroke-linejoin="miter"
				stroke-width="2"
				d="M6 18L18 6M6 6l12 12"
			/>
		</svg>
	{/if}
	<span>{description}</span>
</div>
