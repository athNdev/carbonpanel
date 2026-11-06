<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		/** Page title. Rendered at Carbon's 2xl light scale. */
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
	class="flex flex-col justify-between gap-4 border-b border-[#393939] pb-4 sm:flex-row sm:flex-wrap sm:items-center {className}"
>
	<div class="min-w-0">
		<div class="flex min-w-0 flex-wrap items-center gap-2">
			<h1 class="font-sans text-2xl font-light tracking-tight text-[#f4f4f4]">
				{title}
			</h1>
			{@render tag?.()}
		</div>
		{#if description}
			<p class="mt-1 font-sans text-xs text-[#8d8d8d]">
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
