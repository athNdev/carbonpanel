<!--
SPDX-License-Identifier: MPL-2.0
-->

<script lang="ts">
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { worldLibraryInventory } from '$lib/stores/world-library';
	import { formatRpcError } from '$lib/api/rpc-error';
	import { formatTimestamp, shortId } from '$lib/features/world-library/presentation';
	import {
		CarbonButton,
		CarbonDataTable,
		CarbonEmptyState,
		CarbonErrorState,
		CarbonPageHeader,
		CarbonSearch,
		CarbonSelect,
		CarbonTag
	} from '$lib/components/carbon';
	import { BookOpen, Plus, RefreshCw, Search, Upload } from '@lucide/svelte';

	type EditionFilter = '' | 'java' | 'bedrock';
	type SortMode = 'updated' | 'name' | 'revisions';

	let searchQuery = $state('');
	let editionFilter = $state<EditionFilter>('');
	let sortMode = $state<SortMode>('updated');

	const inventoryState = worldLibraryInventory.state;
	let entries = $derived($worldLibraryInventory);
	let hydrated = $derived($inventoryState.hydrated);
	let loading = $derived($inventoryState.loading);
	let loadError = $derived(
		$inventoryState.error && entries.length === 0
			? formatRpcError($inventoryState.error, 'Could not load the world library. Try again.')
			: null
	);

	let filtered = $derived.by(() => {
		const query = searchQuery.trim().toLowerCase();
		const rows = entries.filter((entry) => {
			if (editionFilter && entry.edition.toLowerCase() !== editionFilter) return false;
			if (!query) return true;
			return (
				entry.name.toLowerCase().includes(query) ||
				entry.family.toLowerCase().includes(query) ||
				entry.edition.toLowerCase().includes(query) ||
				entry.id.toLowerCase().includes(query) ||
				entry.currentRevisionId.toLowerCase().includes(query)
			);
		});
		const sorted = [...rows];
		switch (sortMode) {
			case 'name':
				sorted.sort((a, b) => a.name.localeCompare(b.name));
				break;
			case 'revisions':
				sorted.sort((a, b) => b.revisionCount - a.revisionCount);
				break;
			default:
				sorted.sort(
					(a, b) => Number(b.updatedAt?.seconds ?? 0n) - Number(a.updatedAt?.seconds ?? 0n)
				);
		}
		return sorted;
	});

	let filtersActive = $derived(Boolean(searchQuery.trim()) || editionFilter !== '');

	onMount(async () => {
		if (hydrated) return;
		await worldLibraryInventory.refresh().catch(() => undefined);
	});

	function resetFilters() {
		searchQuery = '';
		editionFilter = '';
		sortMode = 'updated';
	}
</script>

<div class="space-y-6 font-sans text-foreground" data-testid="showcase-library-content">
	<CarbonPageHeader
		title="World Library"
		description="Immutable, content-addressed world revisions. Bound servers always receive a private writable clone."
	>
		{#snippet tag()}
			<CarbonTag type="blue" size="sm">LIBRARY</CarbonTag>
		{/snippet}
		{#snippet actions()}
			<CarbonButton
				kind="secondary"
				size="md"
				href={resolve('/library/new')}
				class="justify-center gap-2"
			>
				<Plus class="h-4 w-4" />
				<span>Create from config</span>
			</CarbonButton>
			<CarbonButton
				kind="primary"
				size="md"
				href={resolve('/library/import')}
				class="justify-center gap-2"
			>
				<Upload class="h-4 w-4" />
				<span>Import world</span>
			</CarbonButton>
		{/snippet}
	</CarbonPageHeader>

	<!-- Grouped search / filter / sort. Each control is labelled and removable. -->
	<div
		class="flex flex-col gap-3 border border-border bg-card p-3 sm:flex-row sm:items-end sm:justify-between"
	>
		<div class="min-w-0 flex-1 sm:max-w-lg">
			<CarbonSearch
				aria-label="Search world library"
				placeholder="Search by name, family, edition or revision ID"
				bind:value={searchQuery}
				onkeydown={(event) => event.key === 'Escape' && (searchQuery = '')}
			/>
		</div>
		<div class="flex flex-wrap items-end gap-3">
			<div class="w-36">
				<CarbonSelect label="Edition" bind:value={editionFilter}>
					<option value="">All editions</option>
					<option value="java">Java</option>
					<option value="bedrock">Bedrock</option>
				</CarbonSelect>
			</div>
			<div class="w-36">
				<CarbonSelect label="Sort by" bind:value={sortMode}>
					<option value="updated">Last updated</option>
					<option value="name">Name</option>
					<option value="revisions">Revision count</option>
				</CarbonSelect>
			</div>
			<CarbonButton
				kind="tertiary"
				size="sm"
				disabled={!filtersActive}
				onclick={resetFilters}
				class="justify-center"
			>
				Reset filters
			</CarbonButton>
		</div>
	</div>

	<p class="text-xs text-muted-foreground" role="status" aria-live="polite">
		{filtered.length} of {entries.length} world {entries.length === 1 ? 'entry' : 'entries'}
		{#if filtersActive}match the active filters{/if}
	</p>

	{#if !hydrated && loading}
		<div class="border border-border bg-card" role="status" aria-label="Loading world library">
			{#each [0, 1, 2, 3] as _, index (index)}
				<div class="flex items-center gap-3 border-b border-border px-4 py-3.5 last:border-b-0">
					<div class="motion-skeleton h-5 w-16"></div>
					<div class="min-w-0 flex-1">
						<div class="motion-skeleton h-4 w-40"></div>
						<div class="motion-skeleton mt-1.5 h-3 w-24"></div>
					</div>
					<div class="motion-skeleton hidden h-4 w-20 sm:block"></div>
				</div>
			{/each}
		</div>
	{:else if loadError}
		<CarbonErrorState
			title="Could not load the world library"
			message={loadError}
			onretry={async () => {
				await worldLibraryInventory.refresh().catch(() => undefined);
			}}
		/>
	{:else if entries.length === 0}
		<CarbonEmptyState
			title="No worlds in the library yet"
			description="Import a world from a URL or completed upload, or create a fresh revision from a validated configuration draft."
		>
			{#snippet icon()}
				<BookOpen class="h-6 w-6" />
			{/snippet}
		</CarbonEmptyState>
		<div class="flex flex-wrap justify-center gap-2">
			<CarbonButton
				kind="primary"
				size="sm"
				href={resolve('/library/import')}
				class="justify-center gap-2"
			>
				<Upload class="h-4 w-4" />
				<span>Import world</span>
			</CarbonButton>
			<CarbonButton
				kind="secondary"
				size="sm"
				href={resolve('/library/new')}
				class="justify-center gap-2"
			>
				<Plus class="h-4 w-4" />
				<span>Create from config</span>
			</CarbonButton>
		</div>
	{:else if filtered.length === 0}
		<div class="border border-border bg-card p-12 text-center" role="status">
			<div
				class="mx-auto mb-4 flex h-12 w-12 items-center justify-center border border-border bg-background text-muted-foreground"
			>
				<Search class="h-6 w-6" />
			</div>
			<h3 class="text-sm font-semibold text-foreground">No matching worlds</h3>
			<p class="mt-1 text-xs text-muted-foreground">
				Try a different search term or clear the edition filter.
			</p>
			<CarbonButton kind="secondary" size="sm" class="mt-4 justify-center" onclick={resetFilters}>
				Clear filters
			</CarbonButton>
		</div>
	{:else}
		<CarbonDataTable
			title="Library entries"
			description="Revisions are immutable; opening an entry shows its provenance, bindings and compatibility report."
		>
			{#snippet header()}
				<tr>
					<th scope="col" class="px-3 py-3">World</th>
					<th scope="col" class="hidden px-3 py-3 md:table-cell">Edition / family</th>
					<th scope="col" class="px-3 py-3">Revisions</th>
					<th scope="col" class="hidden px-3 py-3 lg:table-cell">Current revision</th>
					<th scope="col" class="hidden px-3 py-3 lg:table-cell">Updated</th>
				</tr>
			{/snippet}
			{#each filtered as entry (entry.id)}
				<tr class="transition-colors hover:bg-accent">
					<td class="min-w-[200px] px-3 py-3.5">
						<a
							href={resolve('/library/[id]', { id: entry.id })}
							class="focus-ring block truncate font-medium text-foreground hover:text-accent-text hover:underline"
						>
							{entry.name || shortId(entry.id)}
						</a>
						<span class="font-mono text-xs break-all text-muted-foreground">{entry.id}</span>
					</td>
					<td class="hidden px-3 py-3.5 md:table-cell">
						<div class="flex flex-wrap items-center gap-2">
							<CarbonTag type={entry.edition === 'bedrock' ? 'teal' : 'blue'} size="sm">
								{entry.edition ? entry.edition.toUpperCase() : 'UNSPECIFIED'}
							</CarbonTag>
							<span class="text-xs text-muted-foreground">{entry.family || '—'}</span>
						</div>
					</td>
					<td class="px-3 py-3.5 font-mono text-xs text-foreground">
						{entry.revisionCount}
					</td>
					<td class="hidden px-3 py-3.5 font-mono text-xs text-muted-foreground lg:table-cell">
						{shortId(entry.currentRevisionId)}
					</td>
					<td class="hidden px-3 py-3.5 font-mono text-xs text-muted-foreground lg:table-cell">
						{formatTimestamp(entry.updatedAt)}
					</td>
				</tr>
			{/each}
		</CarbonDataTable>
	{/if}

	{#if $inventoryState.error && entries.length > 0}
		<div class="flex items-center justify-between gap-3 border border-border bg-card p-3 text-xs">
			<span class="text-destructive-text" role="alert">
				Refresh failed; showing the last known library state.
				{formatRpcError($inventoryState.error, 'Unknown error')}
			</span>
			<CarbonButton
				kind="tertiary"
				size="sm"
				onclick={() => worldLibraryInventory.refresh(true).catch(() => undefined)}
				class="justify-center gap-2"
			>
				<RefreshCw class="h-3.5 w-3.5" />
				Retry
			</CarbonButton>
		</div>
	{/if}
</div>
