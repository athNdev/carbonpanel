<!--
SPDX-License-Identifier: MPL-2.0
-->
<script lang="ts">
	import CarbonTag from '$lib/components/carbon/CarbonTag.svelte';
	import type { InstallOutcomeRow } from './mods-browser-store.svelte';

	interface Props {
		rows: InstallOutcomeRow[];
		title?: string;
		ariaLabel?: string;
	}

	let { rows, title = '', ariaLabel = 'Install results' }: Props = $props();
</script>

{#if rows.length > 0}
	<div class="w-full">
		{#if title}
			<p class="mb-1 font-mono text-xs tracking-wider text-muted-foreground uppercase">{title}</p>
		{/if}
		<ul class="border-t border-border pt-2" aria-label={ariaLabel}>
			{#each rows as row (row.key)}
				<li class="flex flex-wrap items-center gap-2 py-0.5">
					<CarbonTag type={row.tone} size="sm">
						{row.tag ??
							(row.tone === 'green' ? 'Installed' : row.tone === 'red' ? 'Failed' : 'Skipped')}
					</CarbonTag>
					<span class="font-mono text-xs text-foreground">{row.label}</span>
					{#if row.detail}
						<span class="text-xs text-muted-foreground">{row.detail}</span>
					{/if}
				</li>
			{/each}
		</ul>
	</div>
{/if}
