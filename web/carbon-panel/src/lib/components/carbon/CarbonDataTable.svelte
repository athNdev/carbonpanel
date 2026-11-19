<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		title?: string;
		description?: string;
		class?: string;
		toolbar?: Snippet;
		header?: Snippet;
		children?: Snippet;
	}

	let {
		title = '',
		description = '',
		class: className = '',
		toolbar,
		header,
		children
	}: Props = $props();
</script>

<div class="w-full border border-border bg-background {className}">
	{#if title || toolbar}
		<div class="flex items-center justify-between border-b border-border bg-card p-4">
			<div>
				{#if title}
					<h3 class="font-sans text-sm font-semibold tracking-wider text-foreground uppercase">
						{title}
					</h3>
				{/if}
				{#if description}
					<p class="mt-0.5 font-sans text-xs text-muted-foreground">{description}</p>
				{/if}
			</div>
			{#if toolbar}
				<div class="flex items-center gap-2">
					{@render toolbar()}
				</div>
			{/if}
		</div>
	{/if}

	<div class="overflow-x-auto">
		<table class="w-full border-collapse text-left font-sans text-sm">
			{#if header}
				<thead class="border-b border-border bg-secondary text-foreground">
					{@render header()}
				</thead>
			{/if}
			<tbody class="divide-y divide-border bg-card text-foreground">
				{@render children?.()}
			</tbody>
		</table>
	</div>
</div>
