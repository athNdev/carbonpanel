<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		title: string;
		description?: string;
		/** fill: lists/logs/tables grow and scroll inside. form: natural height, scroll if needed. */
		mode?: 'fill' | 'form';
		/** When false, skip .tab-fill (fullscreen console). */
		fill?: boolean;
		chip?: Snippet;
		actions?: Snippet;
		toolbar?: Snippet;
		children?: Snippet;
		class?: string;
	}

	let {
		title,
		description = '',
		mode = 'fill',
		fill = true,
		chip,
		actions,
		toolbar,
		children,
		class: className = ''
	}: Props = $props();
</script>

<section
	data-server-tab-panel
	data-mode={mode}
	class="server-tab-panel {fill ? 'tab-fill' : ''} {className}"
>
	<header class="server-tab-header">
		<div class="server-tab-header-copy">
			<div class="server-tab-title-row">
				<h2 class="server-tab-title">{title}</h2>
				{@render chip?.()}
			</div>
			{#if description}
				<p class="server-tab-description">{description}</p>
			{/if}
		</div>
		{#if actions}
			<div class="server-tab-actions">
				{@render actions()}
			</div>
		{/if}
	</header>
	{#if toolbar}
		<div class="server-tab-toolbar">
			{@render toolbar()}
		</div>
	{/if}
	<div class="server-tab-body server-tab-body--{mode}">
		{@render children?.()}
	</div>
</section>
