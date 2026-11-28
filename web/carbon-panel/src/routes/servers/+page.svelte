<!--
SPDX-License-Identifier: MIT AND MPL-2.0
This file includes DiscoPanel material under the MIT License,
Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
and still applies to that inherited material.
Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
If a copy of the MPL was not distributed with this file, You can obtain
one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { resolve } from '$app/paths';
	import {
		serversStore,
		serverInventory,
		serverActions,
		sortServersByActivity
	} from '$lib/stores/servers';
	import { triggerStreamDownload } from '$lib/api/stream-download';
	import {
		serverStatusLabel as getStatusDisplayName,
		serverStatusTone as getStatusTagType
	} from '$lib/features/servers/presentation';
	import { formatRpcError } from '$lib/api/rpc-error';
	import { toast } from 'svelte-sonner';
	import {
		Plus,
		Search,
		Play,
		Square,
		RotateCw,
		RefreshCcw,
		Trash2,
		Server as ServerIcon,
		Users,
		Zap,
		MemoryStick,
		Wifi,
		Table as TableIcon,
		LayoutGrid,
		ArrowRight
	} from '@lucide/svelte';
	import { type Server, ServerStatus, ModLoader } from '$lib/proto/carbonpanel/v1/common_pb';
	import { joinIsPlayable, joinKindLabel } from '$lib/server-join';
	import { effectiveLoader } from '$lib/utils/mod-loader';
	import { CarbonTag, CarbonButton, CarbonConfirm, CarbonPageHeader } from '$lib/components/carbon';
	import { onMount } from 'svelte';

	let servers = $derived($serversStore);
	let filteredServers = $state<Server[]>([]);
	let searchQuery = $state('');
	let statusFilter = $state<'all' | 'running' | 'stopped' | 'issues'>('all');
	let hasErrorIssue = $derived(servers.some((s) => s.status === ServerStatus.ERROR));
	let viewMode = $state<'table' | 'tiles'>('table');
	let loading = $state(false);
	const inventoryState = serverInventory.state;
	let loadError = $derived(
		$inventoryState.error && servers.length === 0
			? formatRpcError($inventoryState.error, 'Could not load servers. Try again.')
			: null
	);
	let pendingDelete = $state<Server | null>(null);
	let confirmOpen = $state(false);
	let deleteDisposition = $state<'delete' | 'keep' | 'detach' | 'export'>('delete');
	// Cold direct loads render before the layout's fetch lands; hold the
	// empty state until the first fetch settles so "no servers" never
	// flashes for fleets that simply haven't loaded yet.
	let hydrated = $derived($inventoryState.hydrated);

	onMount(async () => {
		if (hydrated) return;
		await loadServers();
	});

	async function loadServers() {
		try {
			await serversStore.fetchServers(false);
		} catch (err) {
			console.error('Failed to fetch servers:', err);
		}
	}

	$effect(() => {
		filterServers();
	});

	function filterServers() {
		let sorted = sortServersByActivity([...servers]);

		// Status filter
		if (statusFilter === 'running') {
			sorted = sorted.filter((s) => s.status === ServerStatus.RUNNING);
		} else if (statusFilter === 'stopped') {
			sorted = sorted.filter((s) => s.status === ServerStatus.STOPPED);
		} else if (statusFilter === 'issues') {
			sorted = sorted.filter(
				(s) => s.status === ServerStatus.ERROR || s.status === ServerStatus.UNHEALTHY
			);
		}

		if (!searchQuery.trim()) {
			filteredServers = sorted;
		} else {
			const query = searchQuery.toLowerCase().trim();
			filteredServers = sorted.filter(
				(server) =>
					server.name.toLowerCase().includes(query) ||
					server.description.toLowerCase().includes(query) ||
					server.mcVersion.toLowerCase().includes(query) ||
					String(server.modLoader).toLowerCase().includes(query) ||
					String(server.port).includes(query)
			);
		}
	}

	async function handleServerAction(
		action: 'start' | 'stop' | 'restart' | 'recreate',
		server: Server,
		event?: MouseEvent
	) {
		event?.stopPropagation();
		loading = true;

		try {
			const outcome = await serverActions.perform(server.id, action);
			if (outcome.kind === 'superseded') return;
			toast.success(
				`${action === 'recreate' ? 'Recreating' : action === 'stop' ? 'Stopping' : action === 'restart' ? 'Restarting' : 'Starting'} ${server.name}…`
			);
		} catch (error) {
			toast.error(
				`Failed to ${action} server: ${error instanceof Error ? error.message : 'Unknown error'}`
			);
		} finally {
			loading = false;
		}
	}

	async function deleteServer(server: Server, event?: MouseEvent) {
		event?.stopPropagation();
		deleteDisposition = 'delete';
		pendingDelete = server;
		confirmOpen = true;
	}

	async function confirmDeleteServer() {
		const server = pendingDelete;
		if (!server) return;
		loading = true;
		const disposition = deleteDisposition;
		const target = server;
		try {
			const outcome = await serverActions.perform(target.id, 'delete', disposition);
			if (outcome.kind === 'superseded') return;
			if (outcome.kind === 'exported') {
				triggerStreamDownload(outcome.sessionId, outcome.filename);
				toast.success('World exported. The server was left in place.');
				return;
			}
			toast.success(
				disposition === 'detach'
					? `Detached ${server.name}. The container and world files were left in place.`
					: disposition === 'keep'
						? `Removed ${server.name}. World files were kept.`
						: `Deleted ${server.name}`
			);
		} catch (error) {
			toast.error(
				`Failed to ${disposition === 'export' ? 'export' : 'delete'} server: ${error instanceof Error ? error.message : 'Unknown error'}`
			);
		} finally {
			loading = false;
			pendingDelete = null;
		}
	}

	function getModLoaderDisplay(server: Server): string {
		const loader = effectiveLoader(server.modLoader, server.modpackLoader);
		return ModLoader[loader].replace('_', ' ').toLowerCase();
	}
</script>

<div class="h-full flex-1 space-y-6 bg-background font-sans text-foreground">
	<CarbonPageHeader title="Servers" description="Manage, inspect, and deploy Minecraft servers">
		{#snippet tag()}
			<CarbonTag type="blue" size="sm">MANAGEMENT</CarbonTag>
		{/snippet}
		{#snippet actions()}
			<CarbonButton
				kind="primary"
				size="md"
				href={resolve('/servers/new')}
				class="justify-center gap-2"
			>
				<Plus class="h-4 w-4" />
				<span>Create server</span>
			</CarbonButton>
		{/snippet}
	</CarbonPageHeader>

	<!-- Carbon Action Bar: Search, Filters, View Toggles -->
	<div
		class="flex flex-col items-stretch justify-between gap-3 border border-border bg-card p-3 sm:flex-row sm:items-center"
	>
		<!-- Search Input (Carbon Style) -->
		<div class="relative max-w-lg flex-1">
			<div class="relative flex items-center">
				<Search class="pointer-events-none absolute left-3 h-4 w-4 text-muted-foreground" />
				<input
					type="search"
					aria-label="Search servers"
					placeholder="Search by name, description, version, loader, or port…"
					bind:value={searchQuery}
					onkeydown={(e) => e.key === 'Escape' && (searchQuery = '')}
					class="h-9 w-full rounded-none border-b border-input bg-background pr-3 pl-9 font-sans text-xs text-foreground transition-all placeholder:text-muted-foreground focus:border-b-2 focus:border-primary focus:outline-none"
				/>
			</div>
		</div>

		<!-- Status Filter Tags and View Toggles -->
		<div class="flex flex-wrap items-center justify-between gap-2 sm:justify-end">
			<!-- Filter Tags -->
			<div class="mr-1 flex items-center gap-1 border-r border-border pr-2">
				<button
					type="button"
					onclick={() => (statusFilter = 'all')}
					aria-pressed={statusFilter === 'all'}
					class="cursor-pointer rounded-none border px-2.5 py-1 font-mono text-xs uppercase transition-colors {statusFilter ===
					'all'
						? 'border-primary bg-primary text-primary-foreground'
						: 'border-border bg-background text-muted-foreground hover:bg-accent'}"
				>
					All ({servers.length})
				</button>
				<button
					type="button"
					onclick={() => (statusFilter = 'running')}
					aria-pressed={statusFilter === 'running'}
					class="cursor-pointer rounded-none border px-2.5 py-1 font-mono text-xs uppercase transition-colors {statusFilter ===
					'running'
						? 'border-success bg-success/15 text-success-text'
						: 'border-success/50 bg-background text-success-text hover:bg-accent'}"
				>
					Running ({servers.filter((s) => s.status === ServerStatus.RUNNING).length})
				</button>
				<button
					type="button"
					onclick={() => (statusFilter = 'stopped')}
					aria-pressed={statusFilter === 'stopped'}
					class="cursor-pointer rounded-none border px-2.5 py-1 font-mono text-xs uppercase transition-colors {statusFilter ===
					'stopped'
						? 'border-border bg-secondary text-foreground'
						: 'border-border/60 bg-background text-muted-foreground hover:bg-accent'}"
				>
					Stopped ({servers.filter((s) => s.status === ServerStatus.STOPPED).length})
				</button>
				{#if servers.some((s) => s.status === ServerStatus.ERROR || s.status === ServerStatus.UNHEALTHY)}
					<button
						type="button"
						onclick={() => (statusFilter = 'issues')}
						aria-pressed={statusFilter === 'issues'}
						class="cursor-pointer rounded-none border px-2.5 py-1 font-mono text-xs uppercase transition-colors {statusFilter ===
						'issues'
							? hasErrorIssue
								? 'border-destructive bg-destructive text-primary-foreground'
								: 'border-warning bg-warning text-black'
							: hasErrorIssue
								? 'border-destructive/50 bg-background text-destructive-text hover:bg-accent'
								: 'border-warning/50 bg-background text-warning-text hover:bg-accent'}"
					>
						Issues ({servers.filter(
							(s) => s.status === ServerStatus.ERROR || s.status === ServerStatus.UNHEALTHY
						).length})
					</button>
				{/if}
			</div>

			<!-- View Mode Switcher -->
			<div class="flex items-center border border-border bg-background">
				<button
					type="button"
					onclick={() => (viewMode = 'table')}
					title="Table view"
					aria-label="Table view"
					aria-pressed={viewMode === 'table'}
					class="flex h-8 w-8 cursor-pointer items-center justify-center rounded-none transition-colors {viewMode ===
					'table'
						? 'bg-secondary text-foreground'
						: 'text-muted-foreground hover:bg-card hover:text-foreground'}"
				>
					<TableIcon class="h-4 w-4" />
				</button>
				<button
					type="button"
					onclick={() => (viewMode = 'tiles')}
					title="Tile view"
					aria-label="Tile view"
					aria-pressed={viewMode === 'tiles'}
					class="flex h-8 w-8 cursor-pointer items-center justify-center rounded-none transition-colors {viewMode ===
					'tiles'
						? 'bg-secondary text-foreground'
						: 'text-muted-foreground hover:bg-card hover:text-foreground'}"
				>
					<LayoutGrid class="h-4 w-4" />
				</button>
			</div>
		</div>
	</div>

	<!-- Loading skeleton (cold direct load, before first fetch settles) -->
	{#if !hydrated}
		<div
			class="w-full overflow-hidden rounded-none border border-border bg-background"
			role="status"
			aria-label="Loading servers"
		>
			{#each [0, 1, 2, 3] as _, i (i)}
				<div class="flex items-center gap-3 border-b border-border px-4 py-3.5 last:border-b-0">
					<div class="motion-skeleton h-5 w-16"></div>
					<div class="motion-skeleton h-8 w-8"></div>
					<div class="min-w-0 flex-1">
						<div class="motion-skeleton h-4 w-40"></div>
						<div class="motion-skeleton mt-1.5 h-3 w-24"></div>
					</div>
					<div class="motion-skeleton hidden h-4 w-20 sm:block"></div>
				</div>
			{/each}
		</div>

		<!-- Empty State -->
	{:else if loadError}
		<div class="rounded-none border border-destructive bg-card p-12 text-center" role="alert">
			<h3 class="mb-1 text-base font-semibold text-foreground">Failed to load servers</h3>
			<p class="mx-auto mb-6 max-w-md font-sans text-xs text-muted-foreground">
				{loadError}
			</p>
			<CarbonButton kind="secondary" size="sm" onclick={loadServers} class="justify-center">
				<RefreshCcw class="h-4 w-4" />
				<span>Retry</span>
			</CarbonButton>
		</div>

		<!-- Empty State -->
	{:else if filteredServers.length === 0}
		<div class="rounded-none border border-border bg-card p-12 text-center">
			{#if servers.length === 0}
				<div
					class="mx-auto mb-4 flex h-14 w-14 items-center justify-center border border-border bg-background text-accent-text"
				>
					<ServerIcon class="h-7 w-7" />
				</div>
				<h3 class="mb-1 text-base font-semibold text-foreground">No servers configured</h3>
				<p class="mx-auto mb-6 max-w-md font-sans text-xs text-muted-foreground">
					Create your first Minecraft server.
				</p>
				<CarbonButton
					kind="primary"
					size="md"
					href={resolve('/servers/new')}
					class="inline-flex justify-center gap-2"
				>
					<Plus class="h-4 w-4" />
					<span>Create server</span>
				</CarbonButton>
			{:else}
				<div
					class="mx-auto mb-4 flex h-14 w-14 items-center justify-center border border-border bg-background text-muted-foreground"
				>
					<Search class="h-7 w-7" />
				</div>
				<h3 class="mb-1 text-base font-semibold text-foreground">No matching servers</h3>
				<p class="text-xs text-muted-foreground">Adjust the search or status filter.</p>
				<CarbonButton
					kind="secondary"
					size="sm"
					class="mt-4 justify-center"
					onclick={() => {
						searchQuery = '';
						statusFilter = 'all';
					}}
				>
					Reset filters
				</CarbonButton>
			{/if}
		</div>

		<!-- Carbon DataTable Layout (Requirement 2) -->
	{:else if viewMode === 'table'}
		<div
			class="fleet-table-scroll focus-ring w-full rounded-none border border-border bg-background"
			role="region"
			aria-label="Server inventory table"
			tabindex="0"
		>
			<table class="w-full border-collapse text-left font-sans text-sm">
				<!-- Sharp Header Row #393939 -->
				<thead
					class="border-b border-border bg-secondary text-xs font-medium tracking-wider text-foreground uppercase select-none"
				>
					<tr>
						<th scope="col" class="w-28 px-3 py-3">Status</th>
						<th scope="col" class="px-3 py-3">Server</th>
						<th scope="col" class="px-3 py-3">Minecraft & loader</th>
						<th scope="col" class="hidden px-3 py-3 md:table-cell">Host / Port</th>
						<th scope="col" class="hidden px-3 py-3 md:table-cell">Memory</th>
						<th scope="col" class="px-3 py-3">Players & TPS</th>
						<th scope="col" class="px-3 py-3 text-right">Actions</th>
					</tr>
				</thead>

				<!-- Sharp Alternating/Hover Rows #353535 -->
				<tbody class="motion-stagger divide-y divide-border bg-card text-foreground">
					{#each filteredServers as server (server.id)}
						<tr class="group transition-colors hover:bg-accent">
							<!-- Status Cell -->
							<td class="px-3 py-3.5 whitespace-nowrap">
								<CarbonTag type={getStatusTagType(server.status)} size="sm">
									{getStatusDisplayName(server.status)}
								</CarbonTag>
							</td>

							<!-- Name & Description -->
							<td class="min-w-[200px] px-3 py-3.5">
								<div class="flex items-center gap-2.5">
									<div
										class="flex h-8 w-8 shrink-0 items-center justify-center border border-border bg-background text-accent-text"
									>
										<ServerIcon class="h-4 w-4" />
									</div>
									<div class="min-w-0">
										<a
											href={resolve('/servers/[id]', { id: server.id })}
											class="block truncate text-sm font-medium text-foreground hover:text-accent-text hover:underline"
										>
											{server.name}
										</a>
										<p class="line-clamp-1 text-xs text-muted-foreground">
											{server.description || 'No description provided'}
										</p>
									</div>
								</div>
							</td>

							<!-- Engine & Loader -->
							<td class="px-3 py-3.5 whitespace-nowrap">
								<div class="flex items-center gap-1.5 font-mono text-xs">
									<span class="text-foreground">{server.mcVersion || 'Latest'}</span>
									{#if server.modLoader !== ModLoader.VANILLA && server.modLoader !== ModLoader.UNSPECIFIED}
										<CarbonTag type="teal" size="sm">
											{getModLoaderDisplay(server)}
										</CarbonTag>
									{:else}
										<span class="text-xs text-muted-foreground">Vanilla</span>
									{/if}
								</div>
							</td>

							<!-- Host / Port -->
							<td class="hidden px-3 py-3.5 whitespace-nowrap md:table-cell">
								<div class="flex items-center gap-1.5 font-mono text-xs">
									<Wifi class="h-3.5 w-3.5 text-muted-foreground" />
									<span class="text-muted-foreground">
										{server.proxyHostname ? server.proxyHostname : `:${server.port}`}
									</span>
								</div>
							</td>

							<!-- Memory -->
							<td class="hidden px-3 py-3.5 whitespace-nowrap md:table-cell">
								<div class="flex items-center gap-1.5 font-mono text-xs text-muted-foreground">
									<MemoryStick class="h-3.5 w-3.5 text-muted-foreground" />
									<span>{(server.memory / 1024).toFixed(1)} GB</span>
								</div>
							</td>

							<!-- Players & TPS -->
							<td class="px-3 py-3.5 whitespace-nowrap">
								{#if server.status === ServerStatus.RUNNING || server.status === ServerStatus.UNHEALTHY}
									<div class="flex items-center gap-3 font-mono text-xs">
										<span
											class="flex items-center gap-1 {joinIsPlayable(server)
												? 'text-success-text'
												: 'text-muted-foreground'}"
										>
											<Users class="h-3.5 w-3.5" />
											{#if joinIsPlayable(server)}
												{server.playersOnline || 0}/{server.maxPlayers}
											{:else}
												{joinKindLabel(server.joinObservationKind)}
											{/if}
										</span>
										<span
											class="flex items-center gap-1 {server.tps && server.tps >= 18
												? 'text-success-text'
												: server.tps && server.tps >= 15
													? 'text-warning-text'
													: 'text-destructive-text'}"
										>
											<Zap class="h-3.5 w-3.5" />
											{server.tps ? server.tps.toFixed(1) : '—'}
										</span>
									</div>
								{:else}
									<span class="font-mono text-xs text-muted-foreground">—</span>
								{/if}
							</td>

							<!-- Actions -->
							<td
								class="px-3 py-3.5 text-right whitespace-nowrap"
								onclick={(e) => e.stopPropagation()}
							>
								<div class="inline-flex items-center border border-border bg-background">
									{#if server.status === ServerStatus.STOPPED || server.status === ServerStatus.ERROR || server.status === ServerStatus.DEEPSLEEP || server.status === ServerStatus.PAUSED}
										<button
											title={server.status === ServerStatus.DEEPSLEEP
												? 'Wake'
												: server.status === ServerStatus.PAUSED
													? 'Resume'
													: 'Start'}
											aria-label={`${server.status === ServerStatus.DEEPSLEEP ? 'Wake' : server.status === ServerStatus.PAUSED ? 'Resume' : 'Start'} server ${server.name}`}
											disabled={loading}
											class="flex h-8 w-8 cursor-pointer items-center justify-center rounded-none border-r border-border text-success-text transition-colors hover:bg-success/20 disabled:opacity-40"
											onclick={(e) => handleServerAction('start', server, e)}
										>
											<Play class="h-3.5 w-3.5 fill-current" />
										</button>
									{/if}

									{#if server.status === ServerStatus.RUNNING || server.status === ServerStatus.UNHEALTHY || server.status === ServerStatus.STARTING || server.status === ServerStatus.DEEPSLEEP || server.status === ServerStatus.PAUSED}
										<button
											title="Stop"
											aria-label={`Stop server ${server.name}`}
											disabled={loading}
											class="flex h-8 w-8 cursor-pointer items-center justify-center rounded-none border-r border-border text-destructive-text transition-colors hover:bg-destructive/20 disabled:opacity-40"
											onclick={(e) => handleServerAction('stop', server, e)}
										>
											<Square class="h-3.5 w-3.5 fill-current" />
										</button>
									{/if}

									{#if server.status === ServerStatus.RUNNING || server.status === ServerStatus.UNHEALTHY}
										<button
											title="Restart"
											aria-label={`Restart server ${server.name}`}
											disabled={loading}
											class="flex h-8 w-8 cursor-pointer items-center justify-center rounded-none border-r border-border text-warning-text transition-colors hover:bg-warning/20 disabled:opacity-40"
											onclick={(e) => handleServerAction('restart', server, e)}
										>
											<RotateCw class="h-3.5 w-3.5" />
										</button>
									{/if}

									<button
										title="Recreate"
										aria-label={`Recreate server ${server.name}`}
										disabled={loading}
										class="flex h-8 w-8 cursor-pointer items-center justify-center rounded-none border-r border-border text-muted-foreground transition-colors hover:bg-accent disabled:opacity-40"
										onclick={(e) => handleServerAction('recreate', server, e)}
									>
										<RefreshCcw class="h-3.5 w-3.5" />
									</button>

									<button
										title="Delete"
										aria-label={`Delete server ${server.name}`}
										disabled={loading}
										class="flex h-8 w-8 cursor-pointer items-center justify-center rounded-none border-r border-border text-destructive-text transition-colors hover:bg-destructive/20 disabled:opacity-40"
										onclick={(e) => deleteServer(server, e)}
									>
										<Trash2 class="h-3.5 w-3.5" />
									</button>

									<a
										href={resolve('/servers/[id]', { id: server.id })}
										title="View details"
										aria-label={`View details for ${server.name}`}
										class="flex h-8 w-8 cursor-pointer items-center justify-center rounded-none text-accent-text transition-colors hover:bg-primary/20"
									>
										<ArrowRight class="h-3.5 w-3.5" />
									</a>
								</div>
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>

		<!-- Carbon Tile / Grid Layout -->
	{:else}
		<div
			class="motion-stagger-fade grid grid-cols-[repeat(auto-fill,minmax(min(100%,20rem),1fr))] gap-4 [&>*]:min-w-0"
		>
			{#each filteredServers as server (server.id)}
				<div
					class="flex flex-col justify-between rounded-none border border-border bg-card p-4 transition-colors hover:border-foreground/30"
				>
					<div>
						<!-- Header & Actions -->
						<div class="border-b border-border pb-3">
							<div class="flex min-w-0 items-start gap-3">
								<div
									class="flex h-10 w-10 shrink-0 items-center justify-center border border-border bg-background text-accent-text"
								>
									<ServerIcon class="h-5 w-5" />
								</div>
								<div class="min-w-0">
									<a
										href={resolve('/servers/[id]', { id: server.id })}
										class="focus-ring block text-lg leading-snug font-semibold wrap-anywhere text-foreground hover:text-accent-text"
									>
										{server.name}
									</a>
									{#if server.description}
										<p class="mt-1 text-sm wrap-anywhere text-muted-foreground">
											{server.description}
										</p>
									{/if}
								</div>
							</div>
						</div>

						<!-- Status & Version row -->
						<div class="my-3 flex flex-wrap items-center gap-2">
							<CarbonTag type={getStatusTagType(server.status)} size="sm">
								{getStatusDisplayName(server.status)}
							</CarbonTag>
							<span class="font-mono text-xs text-muted-foreground">{server.mcVersion}</span>
							{#if server.modLoader !== ModLoader.VANILLA && server.modLoader !== ModLoader.UNSPECIFIED}
								<CarbonTag type="teal" size="sm">
									{getModLoaderDisplay(server)}
								</CarbonTag>
							{/if}
						</div>

						<!-- Stats Sharp Grid -->
						<div class="mt-3 grid grid-cols-2 gap-x-3 gap-y-2">
							<div class="flex min-w-0 items-center gap-2 bg-background px-2.5 py-2">
								<Wifi class="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
								<div class="min-w-0">
									<p class="text-xs text-muted-foreground">Port</p>
									<p class="truncate font-mono text-xs font-semibold text-foreground">
										{server.port}
									</p>
								</div>
							</div>
							<div class="flex min-w-0 items-center gap-2 bg-background px-2.5 py-2">
								<MemoryStick class="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
								<div class="min-w-0">
									<p class="text-xs text-muted-foreground">Memory</p>
									<p class="truncate font-mono text-xs font-semibold text-foreground">
										{(server.memory / 1024).toFixed(1)} GiB
									</p>
								</div>
							</div>
							{#if server.status === ServerStatus.RUNNING || server.status === ServerStatus.UNHEALTHY}
								{@const tpsColor = !server.tps
									? 'text-muted-foreground'
									: server.tps >= 18
										? 'text-success-text'
										: server.tps >= 15
											? 'text-warning-text'
											: 'text-destructive-text'}
								<div class="flex min-w-0 items-center gap-2 bg-background px-2.5 py-2">
									<Users class="h-3.5 w-3.5 shrink-0 text-success-text" />
									<div class="min-w-0">
										<p class="text-xs text-muted-foreground">Players</p>
										<p
											class="font-mono text-xs font-semibold {joinIsPlayable(server)
												? 'text-success-text'
												: 'text-muted-foreground'}"
										>
											{#if joinIsPlayable(server)}
												{server.playersOnline || 0} / {server.maxPlayers}
											{:else}
												{joinKindLabel(server.joinObservationKind)}
											{/if}
										</p>
									</div>
								</div>
								<div class="flex min-w-0 items-center gap-2 bg-background px-2.5 py-2">
									<Zap class="h-3.5 w-3.5 shrink-0 {tpsColor}" />
									<div class="min-w-0">
										<p class="text-xs text-muted-foreground">TPS</p>
										<p class="font-mono text-xs font-semibold {tpsColor}">
											{server.tps ? server.tps.toFixed(1) : '—'}
										</p>
									</div>
								</div>
							{/if}
						</div>
					</div>

					<div
						class="mt-4 flex flex-wrap items-center justify-between gap-2 border-t border-border pt-3"
					>
						<a
							href={resolve('/servers/[id]', { id: server.id })}
							class="focus-ring inline-flex min-h-9 items-center gap-2 text-sm font-medium text-accent-text hover:underline"
						>
							View details
							<ArrowRight class="h-4 w-4" aria-hidden="true" />
						</a>
						<!-- Action Buttons Bar -->
						<div class="flex shrink-0 border border-border bg-background">
							{#if server.status === ServerStatus.STOPPED || server.status === ServerStatus.ERROR || server.status === ServerStatus.DEEPSLEEP || server.status === ServerStatus.PAUSED}
								<button
									title={server.status === ServerStatus.DEEPSLEEP
										? 'Wake'
										: server.status === ServerStatus.PAUSED
											? 'Resume'
											: 'Start'}
									aria-label={`${server.status === ServerStatus.DEEPSLEEP ? 'Wake' : server.status === ServerStatus.PAUSED ? 'Resume' : 'Start'} server ${server.name}`}
									disabled={loading}
									class="focus-ring flex h-9 w-9 cursor-pointer items-center justify-center rounded-none border-r border-border text-success-text transition-colors hover:bg-success/20 disabled:opacity-50"
									onclick={(e) => handleServerAction('start', server, e)}
								>
									<Play class="h-3.5 w-3.5 fill-current" />
								</button>
							{/if}
							{#if server.status === ServerStatus.RUNNING || server.status === ServerStatus.UNHEALTHY || server.status === ServerStatus.STARTING || server.status === ServerStatus.DEEPSLEEP || server.status === ServerStatus.PAUSED}
								<button
									title="Stop"
									aria-label={`Stop server ${server.name}`}
									disabled={loading}
									class="focus-ring flex h-9 w-9 cursor-pointer items-center justify-center rounded-none border-r border-border text-destructive-text transition-colors hover:bg-destructive/20 disabled:opacity-50"
									onclick={(e) => handleServerAction('stop', server, e)}
								>
									<Square class="h-2.5 w-2.5 fill-current" />
								</button>
							{/if}
							{#if server.status === ServerStatus.RUNNING || server.status === ServerStatus.UNHEALTHY}
								<button
									title="Restart"
									aria-label={`Restart server ${server.name}`}
									disabled={loading}
									class="focus-ring flex h-9 w-9 cursor-pointer items-center justify-center rounded-none border-r border-border text-warning-text transition-colors hover:bg-warning/20 disabled:opacity-50"
									onclick={(e) => handleServerAction('restart', server, e)}
								>
									<RotateCw class="h-3.5 w-3.5" />
								</button>
							{/if}
							<button
								title="Recreate"
								aria-label={`Recreate server ${server.name}`}
								disabled={loading}
								class="focus-ring flex h-9 w-9 cursor-pointer items-center justify-center rounded-none border-r border-border text-muted-foreground transition-colors hover:bg-accent disabled:opacity-50"
								onclick={(e) => handleServerAction('recreate', server, e)}
							>
								<RefreshCcw class="h-3.5 w-3.5" />
							</button>
							<button
								title="Delete"
								aria-label={`Delete server ${server.name}`}
								disabled={loading}
								class="focus-ring flex h-9 w-9 cursor-pointer items-center justify-center rounded-none text-destructive-text transition-colors hover:bg-destructive/20 disabled:opacity-50"
								onclick={(e) => deleteServer(server, e)}
							>
								<Trash2 class="h-3.5 w-3.5" />
							</button>
						</div>
					</div>
				</div>
			{/each}
		</div>
	{/if}

	<CarbonConfirm
		bind:open={confirmOpen}
		title={deleteDisposition === 'export'
			? 'Export world?'
			: deleteDisposition === 'detach'
				? 'Detach server?'
				: deleteDisposition === 'keep'
					? 'Remove server from the panel?'
					: 'Delete server?'}
		message={deleteDisposition === 'export'
			? 'A zip of the world downloads now. The container, the world, and the server stay in the panel.'
			: deleteDisposition === 'detach'
				? 'The container keeps running and the world stays on disk. The server is removed from the panel. Its host port stays reserved; the proxy name is dropped at the next panel start.'
				: deleteDisposition === 'keep'
					? 'The container is removed and the server leaves the panel. The world directory stays on disk.'
					: 'This will stop and remove the container, delete all files and data, and remove mods and configurations. This action cannot be undone.'}
		confirmLabel={deleteDisposition === 'export'
			? 'Export world'
			: deleteDisposition === 'detach'
				? 'Detach server'
				: deleteDisposition === 'keep'
					? 'Remove from panel'
					: 'Delete server'}
		danger={deleteDisposition !== 'export'}
		confirming={loading}
		onconfirm={confirmDeleteServer}
		onclose={() => (pendingDelete = null)}
	>
		{#snippet details()}
			{#if pendingDelete}
				{pendingDelete.name} · {pendingDelete.mcVersion || 'Latest'}
			{/if}
			<fieldset class="mt-3 space-y-2 font-sans text-xs text-foreground">
				<legend class="mb-1 text-muted-foreground">What to do with the server</legend>
				<label class="flex items-start gap-2">
					<input
						type="radio"
						name="server-delete-disposition"
						value="delete"
						bind:group={deleteDisposition}
						disabled={loading}
					/>
					<span>Delete the container and the world files</span>
				</label>
				<label class="flex items-start gap-2">
					<input
						type="radio"
						name="server-delete-disposition"
						value="keep"
						bind:group={deleteDisposition}
						disabled={loading}
					/>
					<span>Remove the container and keep the world files</span>
				</label>
				<label class="flex items-start gap-2">
					<input
						type="radio"
						name="server-delete-disposition"
						value="detach"
						bind:group={deleteDisposition}
						disabled={loading}
					/>
					<span>Leave the container and the world files</span>
				</label>
				<label class="flex items-start gap-2">
					<input
						type="radio"
						name="server-delete-disposition"
						value="export"
						bind:group={deleteDisposition}
						disabled={loading}
					/>
					<span>Export the world and leave the server</span>
				</label>
			</fieldset>
		{/snippet}
	</CarbonConfirm>
</div>
