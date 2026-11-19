<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		class?: string;
		header?: Snippet;
		children?: Snippet;
	}

	let { class: className = '', header, children }: Props = $props();
</script>

<div class="w-full rounded-none border-t border-border bg-background font-sans text-sm {className}">
	{#if header}
		<div
			class="grid rounded-none border-b border-border bg-secondary px-4 py-2.5 text-xs font-semibold tracking-wider text-foreground uppercase"
		>
			{@render header()}
		</div>
	{/if}
	<div class="divide-y divide-border rounded-none bg-card text-foreground">
		{@render children?.()}
	</div>
</div>
