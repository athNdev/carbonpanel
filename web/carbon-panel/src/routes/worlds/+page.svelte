<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import type { ResolvedPathname } from '$app/types';
	import { create } from '@bufbuild/protobuf';
	import { ConnectError, Code } from '@connectrpc/connect';
	import { ArrowRight, Lock, Map as MapIcon, RefreshCw, X } from '@lucide/svelte';
	import {
		CarbonPageHeader,
		CarbonTag,
		CarbonButton,
		CarbonSearch,
		CarbonSelect,
		CarbonInlineLoading,
		CarbonEmptyState,
		CarbonErrorState
	} from '$lib/components/carbon';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { formatRpcError } from '$lib/api/rpc-error';
	import { authStore, checkPermission } from '$lib/stores/auth';
	import { serversStore } from '$lib/stores/servers';
	import { shortId } from '$lib/features/world-library/presentation';
	import { createWorldViewStore, type WorldViewRequests } from '$lib/stores/worldview.svelte';
	import {
		ListWorldViewTargetsRequestSchema,
		GetWorldViewStatusRequestSchema,
		type WorldViewTarget
	} from '$lib/proto/carbonpanel/v1/worldview_pb';
	import { encodeWorldKey } from '$lib/features/worldview/world-ref';
	import {
		deriveViewState,
		formatAge,
		VIEW_STATE_LABEL,
		VIEW_STATE_TONE
	} from '$lib/features/worldview/state';
	import { rendererName } from '$lib/features/worldview/capabilities';

	const requests: WorldViewRequests = {
		listTargets: async () =>
			(
				await rpcClient.worldView.listWorldViewTargets(
					create(ListWorldViewTargetsRequestSchema),
					silentCallOptions
				)
			).targets,
		status: async (serverId, worldRef) => {
			const { target } = await rpcClient.worldView.getWorldViewStatus(
				create(GetWorldViewStatusRequestSchema, { serverId, worldRef }),
				silentCallOptions
			);
			if (target && target.serverId !== serverId) {
				throw new Error('World View returned status for a different server.');
			}
			return target;
		}
	};
	const store = createWorldViewStore(requests);
	const scopedStore = createWorldViewStore(requests);

	let scopedServerId = $derived(page.url.searchParams.get('server') ?? '');
	let auth = $derived($authStore);
	let canBrowseAll = $derived(checkPermission(auth.permissions, 'world_view', 'read', '*'));
	let canRead = $derived(
		scopedServerId
			? checkPermission(auth.permissions, 'world_view', 'read', scopedServerId)
			: canBrowseAll
	);
	let scopedTarget = $derived($scopedStore.statuses[scopedServerId]);
	let targets = $derived(scopedServerId ? (scopedTarget ? [scopedTarget] : []) : $store.targets);
	let requestError = $derived(
		scopedServerId ? $scopedStore.statusErrors[scopedServerId] : $store.error
	);
	let loading = $derived(
		scopedServerId ? $scopedStore.statusLoading[scopedServerId] : $store.loading
	);
	let hydrated = $derived(
		scopedServerId ? Boolean(scopedTarget) || loading === false : $store.hydrated
	);
	let denied = $derived(
		requestError instanceof ConnectError &&
			(requestError.code === Code.PermissionDenied || requestError.code === Code.Unauthenticated)
	);

	let search = $state('');
	let serverFilter = $state('all');
	let selectedServerId = $derived(scopedServerId || serverFilter);
	let stateFilter = $state('all');

	function retryTargets() {
		const id = scopedServerId;
		const result = id ? scopedStore.loadStatus(id, id, undefined) : store.refreshTargets();
		void result.catch(() => {});
	}

	// Each request mode keeps its own status and error. A failed fleet refresh
	// cannot turn one server's failed lookup into a successful empty result.
	let dismissedStaleError: unknown = $state(null);
	let showStaleBanner = $derived(
		Boolean(requestError) && targets.length > 0 && dismissedStaleError !== requestError
	);

	let principal = $derived(auth.user?.id);
	$effect(() => {
		if (!principal) return;
		// Dispose request state when the authenticated identity changes.
		return () => {
			store.reset();
			scopedStore.reset();
		};
	});

	$effect(() => {
		if (!canRead || auth.isLoading || denied) return;
		const id = scopedServerId;
		retryTargets();
		return id ? scopedStore.watchStatus(id, id, undefined, 15_000) : store.watchTargets(15_000);
	});

	let serverNames = $derived(
		new Map(
			$serversStore
				.filter((server) => checkPermission(auth.permissions, 'servers', 'read', server.id))
				.map((server) => [server.id, server.name])
		)
	);

	function serverLabel(id: string): string {
		return serverNames.get(id) || 'Server ' + shortId(id);
	}

	let servers = $derived(
		Array.from(new Set(targets.map((target) => target.serverId))).sort((first, second) =>
			serverLabel(first).localeCompare(serverLabel(second))
		)
	);
	let stateOptions = $derived(Array.from(new Set(targets.map(deriveViewState))).sort());

	let filtered = $derived.by(() => {
		const query = search.trim().toLowerCase();
		return targets.filter((target) => {
			if (selectedServerId !== 'all' && target.serverId !== selectedServerId) return false;
			if (stateFilter !== 'all' && deriveViewState(target) !== stateFilter) return false;
			if (!query) return true;
			return [
				target.name,
				target.worldRef?.levelName,
				target.worldRef?.bindingId,
				target.serverId,
				serverLabel(target.serverId)
			]
				.join(' ')
				.toLowerCase()
				.includes(query);
		});
	});

	function targetHref(target: WorldViewTarget): ResolvedPathname {
		const key = encodeWorldKey(target.worldRef);
		const base = resolve('/worlds/[server]/[world]', { server: target.serverId, world: key });
		return (
			target.dimension ? `${base}?dimension=${encodeURIComponent(target.dimension)}` : base
		) as ResolvedPathname;
	}

	function rendererLabel(target: WorldViewTarget): string {
		return rendererName(target.renderer?.type ?? target.rendererType, '');
	}

	function clearFilters() {
		search = '';
		serverFilter = 'all';
		stateFilter = 'all';
	}
</script>

<div
	class="h-full flex-1 space-y-6 bg-background font-sans text-foreground"
	data-testid="showcase-worlds-content"
>
	<CarbonPageHeader
		title="World View"
		description={scopedServerId && canRead && !denied
			? 'Maps for ' + serverLabel(scopedServerId)
			: 'Browse Minecraft world maps and see which servers are ready to view.'}
	>
		{#snippet actions()}
			{#if scopedServerId && canBrowseAll}
				<CarbonButton kind="tertiary" size="sm" href={resolve('/worlds')}>All servers</CarbonButton>
			{/if}
			{#if canRead && !denied}
				<CarbonButton kind="secondary" size="sm" onclick={retryTargets} loading={loading ?? false}>
					<RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
					<span>Refresh</span>
				</CarbonButton>
			{/if}
		{/snippet}
	</CarbonPageHeader>

	{#if auth.isLoading}
		<div class="border border-border bg-card p-6" role="status">
			<CarbonInlineLoading description="Checking world-view access…" />
		</div>
	{:else if !canRead || denied}
		<CarbonEmptyState
			title="No world-view access"
			description={scopedServerId
				? 'Viewing this map requires World View read access on this server.'
				: 'Browsing all worlds requires an all-server World View grant. You can open a shared server’s map from its World tab.'}
		>
			{#snippet icon()}
				<Lock class="h-6 w-6" />
			{/snippet}
		</CarbonEmptyState>
	{:else if !hydrated && !requestError}
		<div class="border border-border bg-card p-6" role="status">
			<CarbonInlineLoading description="Loading viewable worlds…" />
		</div>
	{:else if requestError && targets.length === 0}
		<CarbonErrorState
			title="Could not load worlds"
			message={formatRpcError(requestError, 'The world inventory could not be read.')}
			onretry={retryTargets}
		/>
	{:else if targets.length === 0}
		<CarbonEmptyState
			title={scopedServerId ? 'No map target found for this server' : 'No worlds available'}
			description={scopedServerId
				? 'Check the active world from the server’s World tab, then refresh this list.'
				: 'Open a server’s World tab to check its active world and available map options.'}
		>
			{#snippet icon()}
				<MapIcon class="h-6 w-6" />
			{/snippet}
		</CarbonEmptyState>
	{:else}
		{#if showStaleBanner}
			<div
				role="alert"
				class="mb-4 flex flex-col gap-2 border-l-4 border-destructive bg-destructive/10 p-3 text-sm text-destructive-text sm:flex-row sm:items-center sm:justify-between"
			>
				<span>World inventory refresh failed. Showing the last known worlds.</span>
				<div class="flex shrink-0 items-center gap-2">
					<CarbonButton kind="tertiary" size="sm" onclick={retryTargets}>Retry</CarbonButton>
					<button
						type="button"
						class="focus-ring motion-press flex h-8 w-8 cursor-pointer items-center justify-center"
						aria-label="Dismiss refresh error"
						onclick={() => (dismissedStaleError = requestError)}
					>
						<X class="h-4 w-4" strokeWidth={1.5} aria-hidden="true" />
					</button>
				</div>
			</div>
		{/if}
		<div
			class={[
				'grid gap-3 border border-border bg-card p-3 sm:p-4',
				scopedServerId
					? 'sm:grid-cols-[minmax(0,1fr)_minmax(8rem,12rem)]'
					: 'sm:grid-cols-2 lg:grid-cols-[minmax(0,1fr)_minmax(8rem,12rem)_minmax(8rem,12rem)]'
			]}
		>
			<div class={['min-w-0 space-y-1', !scopedServerId && 'sm:col-span-2 lg:col-span-1']}>
				<label
					for="world-map-search"
					class="block text-xs font-normal tracking-[0.32px] text-foreground">Search maps</label
				>
				<CarbonSearch
					id="world-map-search"
					bind:value={search}
					placeholder="World, level or server name"
					aria-label="Search maps"
				/>
			</div>
			{#if !scopedServerId}
				<CarbonSelect bind:value={serverFilter} label="Server">
					<option value="all">All servers ({servers.length})</option>
					{#each servers as server (server)}
						<option value={server}>{serverLabel(server)}</option>
					{/each}
				</CarbonSelect>
			{/if}
			<CarbonSelect bind:value={stateFilter} label="Map status">
				<option value="all">All statuses</option>
				{#each stateOptions as option (option)}
					<option value={option}>{VIEW_STATE_LABEL[option]}</option>
				{/each}
			</CarbonSelect>
		</div>

		{#if filtered.length === 0}
			<CarbonEmptyState
				title="No matching worlds"
				description="Try a different search or clear the filters."
				actionLabel="Clear filters"
				onaction={clearFilters}
			/>
		{:else}
			<section class="border border-border bg-card" aria-labelledby="world-maps-heading">
				<div
					class="flex flex-wrap items-center justify-between gap-3 border-b border-border px-4 py-3"
				>
					<div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
						<h2 id="world-maps-heading" class="text-base font-semibold">World maps</h2>
						<p role="status" class="text-xs text-muted-foreground">
							{filtered.length === targets.length
								? filtered.length
								: `${filtered.length} of ${targets.length}`}
							{targets.length === 1 ? 'map' : 'maps'}
						</p>
					</div>
					{#if search.trim() || serverFilter !== 'all' || stateFilter !== 'all'}
						<CarbonButton kind="tertiary" size="sm" onclick={clearFilters}
							>Reset filters</CarbonButton
						>
					{/if}
				</div>
				<ul class="divide-y divide-border">
					{#each filtered as target (targetHref(target))}
						{@const name = target.name || target.worldRef?.levelName || 'Untitled world'}
						{@const serverName = serverLabel(target.serverId)}
						{@const view = deriveViewState(target)}
						{@const action =
							view === 'onboarding'
								? 'Set up map'
								: ['ready', 'saved', 'stale'].includes(view)
									? 'Open map'
									: 'View details'}
						{@const renderer = rendererLabel(target)}
						<li class="min-w-0">
							<a
								href={targetHref(target)}
								aria-label={`${action} for ${name}${serverName === name ? '' : ` on ${serverName}`}`}
								class="focus-ring group grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-3 px-4 py-4 text-sm text-foreground transition-colors hover:bg-accent focus-visible:bg-accent lg:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)_auto]"
							>
								<div class="col-span-2 flex min-w-0 items-center gap-3 lg:col-span-1">
									<span
										class="grid h-10 w-10 shrink-0 place-items-center border border-border bg-background text-accent-text"
									>
										<MapIcon class="h-5 w-5" strokeWidth={1.5} aria-hidden="true" />
									</span>
									<div class="min-w-0">
										<p class="font-semibold break-words">{name}</p>
										{#if serverName !== name || target.dimension}
											<p class="mt-0.5 text-xs break-words text-muted-foreground">
												{[serverName !== name ? serverName : '', target.dimension]
													.filter(Boolean)
													.join(' · ')}
											</p>
										{/if}
									</div>
								</div>
								<div class="min-w-0 space-y-1">
									<CarbonTag type={VIEW_STATE_TONE[view]} size="sm"
										>{VIEW_STATE_LABEL[view]}</CarbonTag
									>
									{#if renderer || target.freshness?.observedAt || target.lastRender}
										<p class="text-xs break-words text-muted-foreground">
											{[
												renderer,
												target.freshness?.observedAt
													? `Checked ${formatAge(target.freshness.observedAt)}`
													: target.lastRender
														? `Rendered ${formatAge(target.lastRender)}`
														: ''
											]
												.filter(Boolean)
												.join(' · ')}
										</p>
									{/if}
								</div>
								<span
									class="inline-flex items-center gap-2 font-medium whitespace-nowrap text-accent-text group-hover:underline"
								>
									{action}<ArrowRight
										class="h-4 w-4 shrink-0"
										strokeWidth={1.5}
										aria-hidden="true"
									/>
								</span>
							</a>
						</li>
					{/each}
				</ul>
			</section>
		{/if}
	{/if}
</div>
