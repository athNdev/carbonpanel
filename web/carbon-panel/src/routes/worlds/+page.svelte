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
	import { Lock, Map as MapIcon, RefreshCw, X } from '@lucide/svelte';
	import {
		CarbonPageHeader,
		CarbonTag,
		CarbonButton,
		CarbonSearch,
		CarbonSelect,
		CarbonDataTable,
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
		return deriveViewState(target) === 'onboarding'
			? 'Not configured'
			: rendererName(target.renderer?.type ?? target.rendererType, 'Unknown renderer');
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
			class="flex flex-col items-stretch gap-3 border border-border bg-card p-3 sm:flex-row sm:items-end"
		>
			<div class="min-w-0 flex-1">
				<CarbonSearch
					bind:value={search}
					placeholder="Search worlds, levels or servers"
					aria-label="Search viewable worlds"
					size="sm"
				/>
			</div>
			{#if !scopedServerId}
				<div class="w-full sm:w-52">
					<CarbonSelect bind:value={serverFilter} label="Server">
						<option value="all">All servers ({servers.length})</option>
						{#each servers as server (server)}
							<option value={server}>{serverLabel(server)}</option>
						{/each}
					</CarbonSelect>
				</div>
			{/if}
			<div class="w-full sm:w-52">
				<CarbonSelect bind:value={stateFilter} label="State">
					<option value="all">All states</option>
					{#each stateOptions as option (option)}
						<option value={option}>{VIEW_STATE_LABEL[option]}</option>
					{/each}
				</CarbonSelect>
			</div>
		</div>

		{#if filtered.length === 0}
			<CarbonEmptyState
				title="No matching worlds"
				description="Try a different search or clear the filters."
				actionLabel="Clear filters"
				onaction={clearFilters}
			/>
		{:else}
			{#snippet worldLink(target: WorldViewTarget)}
				<a
					href={targetHref(target)}
					class="focus-ring font-medium break-words text-foreground underline-offset-2 hover:text-accent-text hover:underline"
				>
					{target.name || target.worldRef?.levelName || 'Untitled world'}
				</a>
			{/snippet}
			{#snippet stateTag(target: WorldViewTarget)}
				{@const view = deriveViewState(target)}
				<CarbonTag type={VIEW_STATE_TONE[view]} size="sm">{VIEW_STATE_LABEL[view]}</CarbonTag>
			{/snippet}
			{#snippet observation(target: WorldViewTarget)}
				{deriveViewState(target) === 'saved'
					? 'Saved chunks'
					: formatAge(target.freshness?.observedAt ?? target.lastRender)}
			{/snippet}

			<section class="border border-border bg-card xl:hidden" aria-label="Viewable worlds">
				<h2 class="border-b border-border p-3 text-sm font-semibold">
					{filtered.length} of {targets.length} worlds shown
				</h2>
				<ul class="divide-y divide-border">
					{#each filtered as target (targetHref(target))}
						<li class="flex min-w-0 flex-col gap-2 p-3 text-sm">
							<div class="grid items-start gap-2 sm:grid-cols-[minmax(0,1fr)_auto]">
								{@render worldLink(target)}
								<div>{@render stateTag(target)}</div>
							</div>
							<dl
								class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs text-muted-foreground"
							>
								<dt>Server</dt>
								<dd class="break-words">{serverLabel(target.serverId)}</dd>
								<dt>Renderer</dt>
								<dd>{rendererLabel(target)}</dd>
								<dt>Last observation</dt>
								<dd>{@render observation(target)}</dd>
							</dl>
						</li>
					{/each}
				</ul>
			</section>

			<CarbonDataTable
				class="hidden xl:block"
				title="Viewable worlds"
				description="{filtered.length} of {targets.length} worlds shown"
			>
				{#snippet header()}
					<tr>
						<th scope="col" class="px-3 py-3">World</th>
						<th scope="col" class="px-3 py-3">Server</th>
						<th scope="col" class="px-3 py-3">Renderer</th>
						<th scope="col" class="px-3 py-3">State</th>
						<th scope="col" class="px-3 py-3">Last observation</th>
					</tr>
				{/snippet}
				{#each filtered as target (targetHref(target))}
					<tr class="transition-colors hover:bg-accent">
						<td class="min-w-[180px] px-3 py-3">
							{@render worldLink(target)}
							<p class="font-mono text-xs break-all text-muted-foreground">
								{target.worldRef?.levelName ||
									target.worldRef?.bindingId ||
									target.worldRef?.snapshotId ||
									'—'}
							</p>
						</td>
						<td class="px-3 py-3 text-xs break-words text-muted-foreground">
							{serverLabel(target.serverId)}
						</td>
						<td class="px-3 py-3 text-xs text-muted-foreground">
							{rendererLabel(target)}
						</td>
						<td class="px-3 py-3">
							{@render stateTag(target)}
						</td>
						<td class="px-3 py-3 font-mono text-xs text-muted-foreground">
							{@render observation(target)}
						</td>
					</tr>
				{/each}
			</CarbonDataTable>
		{/if}
	{/if}
</div>
