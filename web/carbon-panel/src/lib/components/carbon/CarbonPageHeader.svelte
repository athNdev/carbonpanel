<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		/** Primary heading for the page. */
		title: string;
		/** Optional supporting sentence, rendered as small secondary text. */
		description?: string;
		/** Optional tag or status snippet shown next to the title. */
		tag?: Snippet;
		/** Optional action buttons. They are placed right-aligned on wide screens. */
		actions?: Snippet;
		class?: string;
	}

	let { title, description = '', tag, actions, class: className = '' }: Props = $props();
</script>

<div
	class="flex flex-col justify-between gap-4 border-b border-border pb-4 sm:flex-row sm:flex-wrap sm:items-center {className}"
>
	<div class="min-w-0">
		<div class="flex min-w-0 flex-wrap items-center gap-2">
			<h1
				class="min-w-0 font-sans text-3xl font-semibold tracking-tight [overflow-wrap:anywhere] text-foreground sm:text-4xl"
			>
				{title}
			</h1>
			{@render tag?.()}
		</div>
		{#if description}
			<p class="mt-1 font-sans text-xs text-muted-foreground">
				{description}
			</p>
		{/if}
	</div>
	{#if actions}
		<div class="flex flex-wrap items-center gap-2">
			{@render actions()}
		</div>
	{/if}
</div>
