<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import type { HTMLInputAttributes } from 'svelte/elements';
	import { Search, X } from '@lucide/svelte';
	import { fade } from 'svelte/transition';
	import { motionDuration } from '$lib/motion';

	interface Props extends Omit<HTMLInputAttributes, 'size'> {
		value?: string;
		placeholder?: string;
		size?: 'sm' | 'md' | 'lg';
		disabled?: boolean;
		class?: string;
		onclear?: () => void;
		onkeydown?: (e: KeyboardEvent) => void;
	}

	let {
		value = $bindable(''),
		placeholder = 'Search...',
		size = 'md',
		disabled = false,
		class: className = '',
		onclear,
		onkeydown,
		...restProps
	}: Props = $props();

	const sizeClasses = {
		sm: 'h-[var(--carbon-control-sm)] text-xs pl-9 pr-7',
		md: 'h-[var(--carbon-control-md)] text-sm pl-10 pr-9',
		lg: 'h-[var(--carbon-control-lg)] text-base pl-12 pr-10'
	};

	const iconSizeClasses = {
		sm: 'h-3.5 w-3.5',
		md: 'h-4 w-4',
		lg: 'h-4.5 w-4.5'
	};

	function handleClear() {
		value = '';
		onclear?.();
	}
</script>

<div class="relative w-full rounded-none font-sans {className}">
	<div
		class="pointer-events-none absolute inset-y-0 left-0 flex items-center pl-3 text-muted-foreground"
	>
		<Search class={iconSizeClasses[size]} strokeWidth={1.5} aria-hidden="true" />
	</div>
	<input
		type="text"
		bind:value
		{placeholder}
		{disabled}
		{onkeydown}
		aria-label={restProps['aria-label'] ?? placeholder ?? 'Search'}
		class="focus-ring w-full rounded-none border-b border-input bg-background text-foreground transition-colors placeholder:text-muted-foreground focus:border-b-2 focus:border-ring focus:bg-accent focus:outline-none disabled:border-border disabled:bg-secondary disabled:text-muted-foreground {sizeClasses[
			size
		]}"
		{...restProps}
	/>
	{#if value}
		<button
			type="button"
			onclick={handleClear}
			aria-label="Clear search"
			transition:fade={{ duration: motionDuration(100) }}
			class="focus-ring motion-press absolute inset-y-0 right-0 flex cursor-pointer items-center rounded-none px-2.5 text-muted-foreground hover:text-foreground"
		>
			<X class="h-4 w-4" strokeWidth={1.5} aria-hidden="true" />
		</button>
	{/if}
</div>
