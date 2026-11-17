<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { resolve } from '$app/paths';
	import { create } from '@bufbuild/protobuf';
	import { ConnectError, Code } from '@connectrpc/connect';
	import { Lock, Map as MapIcon, RefreshCw } from '@lucide/svelte';
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
	import { createWorldViewStore } from '$lib/stores/worldview.svelte';
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

	const store = createWorldViewStore({
		listTargets: async () =>
			(
				await rpcClient.worldView.listWorldViewTargets(
					create(ListWorldViewTargetsRequestSchema),
					silentCallOptions
				)
			).targets,
		status: async (serverId, worldRef) =>
			(
				await rpcClient.worldView.getWorldViewStatus(
					create(GetWorldViewStatusRequestSchema, { serverId, worldRef }),
					silentCallOptions
				)
			).target
	});

	let auth = $derived($authStore);
	let canRead = $derived(checkPermission(auth.permissions, 'world_view', 'read'));
	let snapshot = $derived($store);
	let denied = $state(false);
	let started = false;

	let search = $state('');
	let serverFilter = $state('all');
	let stateFilter = $state('all');

	function handleListError(error: unknown) {
		if (error instanceof ConnectError && error.code === Code.PermissionDenied) denied = true;
	}

	$effect(() => {
		if (!canRead || auth.isLoading || started) return;
		started = true;
		void store.refreshTargets().catch(handleListError);
	});

	$effect(() => {
		if (!canRead || denied) return;
		return store.watchTargets(15_000);
	});

	let servers = $derived(
		Array.from(new Set(snapshot.targets.map((target) => target.serverId))).sort()
	);
	let stateOptions = $derived(
		Array.from(
			new Set(snapshot.targets.map((target) => deriveViewState(target).toUpperCase()))
		).sort()
	);

	let filtered = $derived.by(() => {
		const query = search.trim().toLowerCase();
		return snapshot.targets.filter((target) => {
			if (serverFilter !== 'all' && target.serverId !== serverFilter) return false;
			if (stateFilter !== 'all' && deriveViewState(target).toUpperCase() !== stateFilter)
				return false;
			if (!query) return true;
			return [target.name, target.worldRef?.levelName, target.worldRef?.bindingId, target.serverId]
				.join(' ')
				.toLowerCase()
				.includes(query);
		});
	});

	function targetHref(target: WorldViewTarget): string {
		const key = encodeWorldKey(target.worldRef);
		const base = resolve(`/worlds/${target.serverId}/${key}`);
		return target.dimension ? `${base}?dimension=${encodeURIComponent(target.dimension)}` : base;
	}

	function clearFilters() {
		search = '';
		serverFilter = 'all';
		stateFilter = 'all';
	}
</script>

<div class="h-full flex-1 space-y-6 bg-[#161616] font-sans text-[#f4f4f4]">
	<CarbonPageHeader
		title="Worlds"
		description="View Minecraft worlds through their configured renderer. Maps are observation only."
	>
		{#snippet tag()}
			<CarbonTag type="blue" size="sm">OBSERVATION</CarbonTag>
		{/snippet}
		{#snippet actions()}
			<CarbonButton kind="tertiary" size="sm" href={resolve('/modules')}>
				Renderer modules
			</CarbonButton>
			<CarbonButton
				kind="secondary"
				size="sm"
				onclick={() => void store.refreshTargets().catch(handleListError)}
				loading={snapshot.loading}
			>
				<RefreshCw class="h-3.5 w-3.5" />
				<span>Refresh</span>
			</CarbonButton>
		{/snippet}
	</CarbonPageHeader>

	{#if auth.isLoading}
		<div class="border border-[#393939] bg-[#262626] p-6" role="status">
			<CarbonInlineLoading description="Checking world-view access…" />
		</div>
	{:else if !canRead || denied}
		<CarbonEmptyState
			title="No world-view access"
			description="Your account needs the world_view/read permission to view maps. Ask an administrator for access. No map is loaded for this session."
		>
			{#snippet icon()}
				<Lock class="h-6 w-6" />
			{/snippet}
		</CarbonEmptyState>
	{:else if !snapshot.hydrated && snapshot.loading}
		<div class="border border-[#393939] bg-[#262626] p-6" role="status">
			<CarbonInlineLoading description="Loading viewable worlds…" />
		</div>
	{:else if snapshot.error}
		<CarbonErrorState
			title="Could not load worlds"
			message={formatRpcError(snapshot.error, 'The world inventory could not be read.')}
			onretry={() => {
				void store.refreshTargets().catch(handleListError);
			}}
		/>
	{:else if snapshot.targets.length === 0}
		<CarbonEmptyState
			title="No viewable worlds"
			description="No renderer-backed worlds are available for your account yet. Install a renderer module for a server, then refresh."
		>
			{#snippet icon()}
				<MapIcon class="h-6 w-6" />
			{/snippet}
		</CarbonEmptyState>
	{:else}
		<div
			class="flex flex-col items-stretch gap-3 border border-[#393939] bg-[#262626] p-3 sm:flex-row sm:items-end"
		>
			<div class="min-w-0 flex-1">
				<CarbonSearch
					bind:value={search}
					placeholder="Search worlds, levels or servers"
					aria-label="Search viewable worlds"
					size="sm"
				/>
			</div>
			<div class="w-full sm:w-52">
				<CarbonSelect bind:value={serverFilter} label="Server">
					<option value="all">All servers ({servers.length})</option>
					{#each servers as server (server)}
						<option value={server}>{server}</option>
					{/each}
				</CarbonSelect>
			</div>
			<div class="w-full sm:w-52">
				<CarbonSelect bind:value={stateFilter} label="State">
					<option value="all">All states</option>
					{#each stateOptions as option (option)}
						<option value={option}>{option}</option>
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
			<CarbonDataTable
				title="Viewable worlds"
				description="{filtered.length} of {snapshot.targets.length} worlds shown"
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
					{@const view = deriveViewState(target)}
					<tr class="transition-colors hover:bg-[#353535]">
						<td class="min-w-[180px] px-3 py-3">
							<a
								href={resolve(
									`/worlds/${target.serverId}/${encodeWorldKey(target.worldRef)}${target.dimension ? `?dimension=${encodeURIComponent(target.dimension)}` : ''}`
								)}
								class="block truncate font-medium text-[#f4f4f4] hover:text-[#78a9ff] hover:underline"
							>
								{target.name || target.worldRef?.levelName || 'Untitled world'}
							</a>
							<p class="font-mono text-xs break-all text-[#a8a8a8]">
								{target.worldRef?.levelName ||
									target.worldRef?.bindingId ||
									target.worldRef?.snapshotId ||
									'—'}
							</p>
						</td>
						<td class="px-3 py-3 font-mono text-xs break-all text-[#c6c6c6]">
							{target.serverId}
						</td>
						<td class="px-3 py-3 text-xs text-[#c6c6c6]">
							{rendererName(target.renderer?.type ?? target.rendererType)}
						</td>
						<td class="px-3 py-3">
							<CarbonTag type={VIEW_STATE_TONE[view]} size="sm">{VIEW_STATE_LABEL[view]}</CarbonTag>
						</td>
						<td class="px-3 py-3 font-mono text-xs text-[#a8a8a8]">
							{formatAge(target.freshness?.observedAt ?? target.lastRender)}
						</td>
					</tr>
				{/each}
			</CarbonDataTable>
		{/if}
	{/if}
</div>
