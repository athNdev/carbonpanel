<!--
SPDX-License-Identifier: MPL-2.0
-->
<script lang="ts">
	import type { Snippet } from 'svelte';
	import { Download, Package } from '@lucide/svelte';
	import CarbonButton from '$lib/components/carbon/CarbonButton.svelte';
	import CarbonTag from '$lib/components/carbon/CarbonTag.svelte';
	import CarbonTile from '$lib/components/carbon/CarbonTile.svelte';

	/** A catalog row as rendered by any mods surface (dialog, wizard, studio). */
	export interface CatalogCardResult {
		title: string;
		description: string;
		iconUrl: string;
		author: string;
		downloads: number;
		categories: string[];
		platform: string;
		clientSide?: string;
		serverSide?: string;
	}

	type CardState = 'idle' | 'installed' | 'queued' | 'added';

	interface Props {
		result: CatalogCardResult;
		state?: CardState;
		busy?: boolean;
		selectLabel?: string;
		onselect?: () => void;
		badges?: Snippet;
		actions?: Snippet;
		maxCategories?: number;
	}

	let {
		result,
		state = 'idle',
		busy = false,
		selectLabel = 'Select Version',
		onselect,
		badges,
		actions,
		maxCategories = 3
	}: Props = $props();

	function formatDownloads(count: number): string {
		if (!count) return '0';
		if (count >= 1_000_000) return `${(count / 1_000_000).toFixed(1)}M`;
		if (count >= 1_000) return `${(count / 1_000).toFixed(1)}k`;
		return count.toString();
	}

	// Platform labels are first-class badges: Hangar results must be labeled,
	// not silently mixed into the Modrinth/CurseForge feed.
	const PLATFORM_LABELS: Record<string, string> = {
		modrinth: 'Modrinth',
		curseforge: 'CurseForge',
		hangar: 'Hangar'
	};

	const STATE_TAGS: Record<CardState, { tone: 'green' | 'teal'; label: string } | null> = {
		idle: null,
		installed: { tone: 'green', label: 'Installed' },
		added: { tone: 'green', label: 'Added just now' },
		queued: { tone: 'teal', label: 'Selected' }
	};
</script>

<CarbonTile
	class="flex items-start justify-between gap-4 rounded-none border-border p-4 transition-colors hover:border-primary/50"
>
	<div class="flex min-w-0 flex-1 items-start gap-3.5">
		{#if result.iconUrl}
			<img
				src={result.iconUrl}
				alt={result.title}
				class="h-12 w-12 shrink-0 rounded-none border border-border bg-muted/40 object-contain p-1"
			/>
		{:else}
			<div
				class="flex h-12 w-12 shrink-0 items-center justify-center rounded-none border border-border bg-muted"
			>
				<Package class="h-6 w-6 text-muted-foreground" aria-hidden="true" />
			</div>
		{/if}

		<div class="min-w-0 flex-1 space-y-1">
			<div class="flex flex-wrap items-center gap-2">
				<span class="truncate text-base font-semibold text-foreground">{result.title}</span>
				{#if result.author}
					<span class="truncate text-xs text-muted-foreground">by {result.author}</span>
				{/if}
				{#if state !== 'idle' && STATE_TAGS[state]}
					<CarbonTag type={STATE_TAGS[state].tone} size="sm">{STATE_TAGS[state].label}</CarbonTag>
				{/if}
			</div>
			{#if result.description}
				<p class="line-clamp-2 text-xs text-muted-foreground">{result.description}</p>
			{/if}

			<div class="flex flex-wrap items-center gap-1.5 pt-1">
				<CarbonTag type="cyan" size="sm">
					{PLATFORM_LABELS[result.platform.toLowerCase()] ?? result.platform}
				</CarbonTag>
				{#if result.downloads}
					<CarbonTag type="gray" size="sm">
						<Download class="mr-0.5 h-2.5 w-2.5" aria-hidden="true" />
						{formatDownloads(result.downloads)}
					</CarbonTag>
				{/if}
				{#if result.serverSide}
					<CarbonTag
						type={result.serverSide === 'required' ? 'green' : 'gray'}
						size="sm"
						title="Server-side support: {result.serverSide}"
					>
						Server: {result.serverSide}
					</CarbonTag>
				{/if}
				{#if result.clientSide}
					<CarbonTag type="gray" size="sm" title="Client-side support: {result.clientSide}">
						Client: {result.clientSide}
					</CarbonTag>
				{/if}
				{#each (result.categories || []).slice(0, maxCategories) as category (category)}
					<CarbonTag type="gray" size="sm" class="capitalize">{category}</CarbonTag>
				{/each}
				{@render badges?.()}
			</div>
		</div>
	</div>

	<div class="flex shrink-0 items-center gap-1.5">
		{@render actions?.()}
		{#if !actions && onselect}
			<CarbonButton size="sm" loading={busy} disabled={busy} onclick={onselect}>
				{selectLabel}
			</CarbonButton>
		{/if}
	</div>
</CarbonTile>
