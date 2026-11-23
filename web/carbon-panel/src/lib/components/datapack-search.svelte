<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import type { Snippet } from 'svelte';
	import { CarbonButton } from '$lib/components/carbon';
	import { Input } from '$lib/components/ui/input';
	import { Badge } from '$lib/components/ui/badge';
	import { Card, CardContent } from '$lib/components/ui/card';
	import { Search, Loader2, AlertTriangle, Package } from '@lucide/svelte';
	import { rpcClient } from '$lib/api/rpc-client';
	import { createRequestSequence } from '$lib/utils/request-sequence';

	export interface DatapackSearchResult {
		id: string;
		indexer: string;
		name: string;
		slug: string;
		summary: string;
		iconUrl: string;
		author: string;
		downloadCount: number;
		gameVersions: string[];
	}

	interface Props {
		gameVersion?: string;
		emptyHint?: string;
		/** Per-result action, e.g. an Install or Add button. */
		actions: Snippet<[DatapackSearchResult]>;
	}

	let {
		gameVersion = '',
		emptyHint = 'Search for datapacks to install.',
		actions
	}: Props = $props();

	let query = $state('');
	let results = $state<DatapackSearchResult[]>([]);
	let searching = $state(false);
	let searched = $state(false);
	let error = $state('');

	// E21: the same stale-response race as the mods catalog searches — a slow
	// earlier response must never overwrite newer results.
	const searchSequence = createRequestSequence();

	export async function search() {
		searching = true;
		error = '';
		const seq = searchSequence.next();
		try {
			const res = await rpcClient.datapack.searchDatapacks({
				query,
				gameVersion,
				limit: 20,
				offset: 0
			});
			if (!searchSequence.isCurrent(seq)) return;
			results = (res.datapacks ?? []).map((d) => ({
				id: d.id,
				indexer: d.indexer,
				name: d.name,
				slug: d.slug,
				summary: d.summary,
				iconUrl: d.iconUrl,
				author: d.author,
				downloadCount: Number(d.downloadCount),
				gameVersions: d.gameVersions ?? []
			}));
			searched = true;
		} catch (e) {
			if (!searchSequence.isCurrent(seq)) return;
			error = e instanceof Error ? e.message : 'Datapack search failed';
		} finally {
			if (searchSequence.isCurrent(seq)) searching = false;
		}
	}

	function onkeydown(event: KeyboardEvent) {
		if (event.key === 'Enter') {
			// Stop implicit <form> submission: this input can sit inside the
			// create-server wizard form, and Enter must only trigger a search.
			event.preventDefault();
			search();
		}
	}
</script>

<div class="w-full min-w-0 space-y-3">
	<div class="flex items-center gap-2">
		<div class="relative flex-1">
			<Search class="absolute top-1/2 left-2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
			<Input
				bind:value={query}
				{onkeydown}
				placeholder="Search datapacks..."
				class="border-border bg-card pl-8 text-foreground"
			/>
		</div>
		<CarbonButton onclick={search} disabled={searching} class="rounded-none bg-primary text-white">
			{#if searching}<Loader2 class="h-4 w-4 animate-spin" />{:else}Search{/if}
		</CarbonButton>
	</div>

	{#if error}
		<div
			class="flex items-start gap-2 border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive-text"
		>
			<AlertTriangle class="mt-0.5 h-4 w-4 shrink-0" />
			<span>{error}</span>
		</div>
	{/if}

	<div class="max-h-[50vh] min-w-0 space-y-2 overflow-y-auto">
		{#each results as d (d.id)}
			<Card class="border-border bg-card">
				<CardContent class="flex items-start gap-3 p-3">
					{#if d.iconUrl}
						<img src={d.iconUrl} alt="" class="h-10 w-10 shrink-0 object-cover" />
					{:else}
						<div class="flex h-10 w-10 shrink-0 items-center justify-center bg-muted">
							<Package class="h-5 w-5 text-muted-foreground" />
						</div>
					{/if}
					<div class="min-w-0 flex-1">
						<div class="flex items-center gap-2">
							<span class="truncate font-medium text-foreground">{d.name}</span>
							<Badge class="border-border bg-muted whitespace-nowrap text-foreground"
								>datapack</Badge
							>
							{#if d.indexer}
								<Badge class="border-border bg-card whitespace-nowrap text-muted-foreground"
									>{d.indexer}</Badge
								>
							{/if}
						</div>
						<p class="line-clamp-2 text-xs text-muted-foreground">{d.summary}</p>
						<div class="mt-1 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
							<span>by {d.author}</span>
							<span>· {d.downloadCount.toLocaleString()} downloads</span>
							{#if d.gameVersions.length}
								<span>· {d.gameVersions.slice(0, 3).join(', ')}</span>
							{/if}
						</div>
					</div>
					<div class="shrink-0">{@render actions(d)}</div>
				</CardContent>
			</Card>
		{:else}
			{#if searched && !searching}
				<p class="py-8 text-center text-sm text-muted-foreground">No datapacks found.</p>
			{:else if !searching}
				<p class="py-8 text-center text-sm text-muted-foreground">{emptyHint}</p>
			{/if}
		{/each}
	</div>
</div>
