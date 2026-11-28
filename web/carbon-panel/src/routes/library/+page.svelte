<!--
SPDX-License-Identifier: MPL-2.0
-->

<script lang="ts">
	import { resolve } from '$app/paths';
	import { authStore, checkPermission } from '$lib/stores/auth';
	import { serverInventory } from '$lib/stores/servers';
	import { ModLoader, ServerStatus, type Server } from '$lib/proto/carbonpanel/v1/common_pb';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import {
		readServerRows,
		runningWorld,
		type RunningWorld
	} from '$lib/features/world-library/running-worlds';
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
	const serverState = serverInventory.state;
	let auth = $derived($authStore);
	let principal = $derived(auth.user?.id);
	let canBrowse = $derived(checkPermission(auth.permissions, 'worlds', 'read', '*'));
	let canCreate = $derived(checkPermission(auth.permissions, 'worlds', 'create', '*'));
	let canListServers = $derived(checkPermission(auth.permissions, 'servers', 'read', '*'));
	let scopedIds = $derived(
		JSON.stringify(
			[
				...new Set(
					auth.permissions
						.map((p) => p.objectId)
						.filter(
							(id) =>
								id &&
								id !== '*' &&
								checkPermission(auth.permissions, 'servers', 'read', id) &&
								checkPermission(auth.permissions, 'worlds', 'read', id)
						)
				)
			].sort()
		)
	);
	let scopedServers = $state.raw<Server[]>([]);
	let scopedServerError = $state(false);
	let scopedLoading = $state(false);
	let refreshReferences = $state(0);
	$effect(() => {
		if (canListServers || auth.isLoading) return;
		void principal;
		void refreshReferences;
		const ids: string[] = JSON.parse(scopedIds);
		const abort = new AbortController();
		let timer: ReturnType<typeof setTimeout>;
		scopedServers = [];
		async function refresh() {
			scopedLoading = true;
			const found: Server[] = [];
			let failed = false;
			await readServerRows(
				ids,
				abort.signal,
				(id, signal) => rpcClient.server.getServer({ id }, { ...silentCallOptions, signal }),
				(id, result) => {
					if (result?.server?.id === id) found.push(result.server);
					else failed = true;
				}
			);
			if (abort.signal.aborted) return;
			scopedServers = found;
			scopedServerError = failed;
			scopedLoading = false;
			if (ids.length) timer = setTimeout(refresh, 30_000);
		}
		void refresh();
		return () => {
			abort.abort();
			clearTimeout(timer);
		};
	});
	let serverError = $derived(canListServers ? $serverState.error : scopedServerError);
	let fleetDenied = $derived(canListServers && $serverState.listAccessDenied);
	let serversLoading = $derived(canListServers ? !$serverState.hydrated : scopedLoading);
	let runningServers = $derived(
		(fleetDenied ? [] : canListServers ? $serverState.servers : scopedServers).filter(
			(server) =>
				server.status === ServerStatus.RUNNING &&
				checkPermission(auth.permissions, 'servers', 'read', server.id) &&
				checkPermission(auth.permissions, 'worlds', 'read', server.id)
		)
	);
	// A stats poll returns fresh objects even when fleet membership is unchanged.
	let serverKey = $derived(JSON.stringify(runningServers.map((server) => server.id).sort()));
	let references = $state.raw<Record<string, RunningWorld>>({});
	$effect(() => {
		const ids: string[] = JSON.parse(serverKey);
		void principal;
		void refreshReferences;
		const abort = new AbortController();
		references = {};
		let timer: ReturnType<typeof setTimeout>;
		async function refresh() {
			await readServerRows(
				ids,
				abort.signal,
				(serverId, signal) =>
					rpcClient.world.listBindings({ serverId }, { ...silentCallOptions, signal }),
				(id, result) => {
					references = {
						...references,
						[id]: result
							? runningWorld(id, result)
							: { state: 'unavailable', level: '', revision: '' }
					};
				}
			);
			if (!abort.signal.aborted && ids.length) timer = setTimeout(refresh, 30_000);
		}
		void refresh();
		return () => {
			abort.abort();
			clearTimeout(timer);
		};
	});
	let filteredServers = $derived(
		runningServers
			.filter((server) => {
				const edition =
					server.modLoader !== ModLoader.UNSPECIFIED || server.javaVersion > 0 ? 'java' : '';
				const ref = references[server.id];
				return (
					(!editionFilter || editionFilter === edition) &&
					[server.name, server.id, edition, ref?.level, ref?.revision]
						.join(' ')
						.toLowerCase()
						.includes(searchQuery.trim().toLowerCase())
				);
			})
			.sort((a, b) => a.name.localeCompare(b.name))
	);
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

	let filtersActive = $derived(
		Boolean(searchQuery.trim()) || editionFilter !== '' || sortMode !== 'updated'
	);

	$effect(() => {
		void principal;
		if (auth.isLoading || !canBrowse) return;
		worldLibraryInventory.reset();
		void worldLibraryInventory.refresh().catch(() => undefined);
		return () => worldLibraryInventory.reset();
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
		description="Browse worlds on running servers and manage saved library revisions."
	>
		{#snippet tag()}
			<CarbonTag type="blue" size="sm">LIBRARY</CarbonTag>
		{/snippet}
		{#snippet actions()}
			{#if canCreate}
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
			{/if}
		{/snippet}
	</CarbonPageHeader>

	<!-- Grouped search / filter / sort. Each control is labelled and removable. -->
	<div
		class="grid items-end gap-3 border border-border bg-card p-3 sm:grid-cols-2 lg:grid-cols-[minmax(12rem,1fr)_minmax(8rem,0.4fr)_minmax(8rem,0.4fr)_auto]"
	>
		<div class="min-w-0 space-y-1">
			<label for="library-search" class="block text-xs tracking-[0.32px]">Search</label>
			<CarbonSearch
				id="library-search"
				aria-label="Search world library"
				placeholder="World, server, edition or revision"
				bind:value={searchQuery}
				onkeydown={(event) => event.key === 'Escape' && (searchQuery = '')}
			/>
		</div>
		<div class="min-w-0">
			<CarbonSelect label="Edition" bind:value={editionFilter}>
				<option value="">All editions</option>
				<option value="java">Java</option>
				<option value="bedrock">Bedrock</option>
			</CarbonSelect>
		</div>
		<div class="min-w-0">
			<CarbonSelect label="Sort library entries" bind:value={sortMode}>
				<option value="updated">Last updated</option>
				<option value="name">Name</option>
				<option value="revisions">Revision count</option>
			</CarbonSelect>
		</div>
		<CarbonButton
			kind="tertiary"
			size="md"
			disabled={!filtersActive}
			onclick={resetFilters}
			class="justify-center"
		>
			Reset filters
		</CarbonButton>
	</div>

	{#if (canListServers || scopedIds !== '[]') && checkPermission(auth.permissions, 'worlds', 'read')}
		<section aria-labelledby="running-worlds-heading" class="border border-border bg-card">
			<header class="flex flex-wrap items-center justify-between gap-2 border-b border-border p-4">
				<div>
					<h2 id="running-worlds-heading" class="text-sm font-semibold">Running server worlds</h2>
					<p class="mt-1 text-xs text-muted-foreground">
						These worlds stay on their servers. A library binding identifies the revision used to
						create the server’s working copy.
					</p>
				</div>
				<CarbonButton
					kind="tertiary"
					size="sm"
					onclick={() => {
						refreshReferences++;
						if (canListServers) void serverInventory.refresh(false, true).catch(() => undefined);
					}}>Refresh</CarbonButton
				>
			</header>
			{#if fleetDenied}
				<p role="alert" class="px-4 py-3 text-sm text-muted-foreground">
					Running server inventory access was denied.
				</p>
			{:else if serverError}
				<p role="alert" class="px-4 py-3 text-sm text-destructive-text">
					Could not refresh the running server list.
				</p>
			{/if}
			{#if !fleetDenied && !filteredServers.length}
				<p class="p-4 text-sm text-muted-foreground" role="status">
					{serversLoading
						? 'Loading running servers…'
						: serverError
							? 'Running worlds unavailable.'
							: runningServers.length
								? 'No running worlds match these filters.'
								: 'No running servers with world access.'}
				</p>
			{:else if !fleetDenied}
				<ul class="divide-y divide-border">
					{#each filteredServers as server (server.id)}
						{@const ref = references[server.id]}
						<li class="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
							<div class="min-w-0">
								<p class="text-sm font-semibold break-words">{ref?.level || 'Current world'}</p>
								<a
									class="focus-ring text-xs text-accent-text hover:underline"
									href={resolve('/servers/[id]', { id: server.id })}
									>{server.name || shortId(server.id)}</a
								>
							</div>
							<div class="flex min-w-0 flex-wrap items-center gap-2">
								<CarbonTag size="sm" type={ref?.state === 'bound' ? 'blue' : 'gray'}>
									{!ref
										? 'Reading world…'
										: ref.state === 'bound'
											? 'Library binding'
											: ref.state === 'local'
												? 'Server-local'
												: ref.state === 'unknown'
													? 'Level unknown'
													: 'World details unavailable'}
								</CarbonTag>
								{#if ref?.revision}<code class="text-xs text-muted-foreground"
										>{shortId(ref.revision)}</code
									>{/if}
								{#if checkPermission(auth.permissions, 'world_view', 'read', server.id)}
									<!-- eslint-disable svelte/no-navigation-without-resolve -- resolve() present; query appended -->
									<a
										class="focus-ring text-xs text-accent-text hover:underline"
										href={resolve('/servers/[id]', { id: server.id }) + '?tab=world'}>View map</a
									>
									<!-- eslint-enable svelte/no-navigation-without-resolve -->
								{/if}
							</div>
						</li>
					{/each}
				</ul>
			{/if}
		</section>
	{/if}
	{#if canBrowse}
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
				title="No saved library revisions yet"
				description={canCreate
					? 'Import a world or create a revision from a configuration.'
					: 'An operator with world creation access can add a revision.'}
			>
				{#snippet icon()}
					<BookOpen class="h-6 w-6" />
				{/snippet}
			</CarbonEmptyState>
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
				description="Revisions are immutable; opening an entry shows its provenance, bindings, and compatibility report."
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
	{:else if !auth.isLoading}
		<p class="text-sm text-muted-foreground">
			Viewing saved library revisions requires library-wide world read access.
		</p>
	{/if}
</div>
