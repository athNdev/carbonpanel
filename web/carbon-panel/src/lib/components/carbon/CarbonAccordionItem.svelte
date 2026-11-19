<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import type { Snippet } from 'svelte';
	import { ChevronRight } from '@lucide/svelte';

	interface Props {
		title: string;
		subtitle?: string;
		open?: boolean;
		disabled?: boolean;
		class?: string;
		children?: Snippet;
	}

	let {
		title,
		subtitle = '',
		open = $bindable(false),
		disabled = false,
		class: className = '',
		children
	}: Props = $props();

	const uid = $props.id();
	const panelId = `${uid}-panel`;
</script>

<li class="list-none rounded-none border-b border-border {className}">
	<button
		type="button"
		{disabled}
		onclick={() => (open = !open)}
		aria-expanded={open}
		aria-controls={panelId}
		class="focus-ring flex h-[var(--carbon-control-lg)] w-full cursor-pointer items-center justify-between rounded-none bg-transparent px-4 text-left text-sm font-medium text-foreground transition-colors select-none hover:bg-card active:bg-accent disabled:cursor-not-allowed disabled:opacity-50"
	>
		<div class="flex min-w-0 items-center gap-3 pr-4">
			<span class="truncate">{title}</span>
			{#if subtitle}
				<span class="truncate text-xs font-normal text-muted-foreground">{subtitle}</span>
			{/if}
		</div>
		<ChevronRight
			class="h-4 w-4 shrink-0 text-muted-foreground transition-transform duration-[var(--motion-base)] ease-[var(--ease-out-quart)] {open
				? 'rotate-90'
				: ''}"
			strokeWidth={1.5}
			aria-hidden="true"
		/>
	</button>
	<div
		id={panelId}
		inert={!open}
		class="grid transition-[grid-template-rows] duration-[var(--motion-slow)] ease-[var(--ease-in-out-standard)] {open
			? 'grid-rows-[1fr]'
			: 'grid-rows-[0fr]'}"
	>
		<div class="overflow-hidden">
			<div class="rounded-none bg-card/40 p-4 text-sm text-muted-foreground">
				{@render children?.()}
			</div>
		</div>
	</div>
</li>
