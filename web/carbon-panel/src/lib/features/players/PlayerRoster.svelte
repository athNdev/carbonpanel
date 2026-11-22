<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { goto, replaceState } from '$app/navigation';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import type { ResolvedPathname } from '$app/types';
	import { SvelteSet, SvelteURLSearchParams } from 'svelte/reactivity';
	import { ChevronDown, ChevronRight, RefreshCw } from '@lucide/svelte';
	import {
		CarbonButton,
		CarbonDataTable,
		CarbonEmptyState,
		CarbonErrorState,
		CarbonInlineLoading,
		CarbonSearch,
		CarbonSelect,
		CarbonTag
	} from '$lib/components/carbon';
	import PlayerAvatar from '$lib/components/player-avatar.svelte';
	import { rpcClient } from '$lib/api/rpc-client';
	import { formatTimestamp } from '$lib/utils/format-time';
	import type { PlayerSummary } from '$lib/proto/carbonpanel/v1/player_pb';
	import {
		ACCESS_FILTERS,
		DEFAULT_ROSTER_FILTER,
		STATE_FILTERS,
		SORT_OPTIONS,
		filterAndSortPlayers,
		presenceLabel,
		presenceTagType,
		provenanceView,
		projectServerOptions,
		accessRank,
		type AccessFilter,
		type RosterFilter,
		type SortOption,
		type StateFilter
	} from './presentation';
	import { createPlayersStore } from '$lib/stores/players';
	import PlayerActions, { type PlayerActionTarget } from './PlayerActions.svelte';
	import PlayerProvenance from './PlayerProvenance.svelte';

	interface Props {
		/** Empty for the cross-server roster; a server id scopes the embedded tab roster. */
		serverId?: string;
		/** Persist filter/sort/search state in the URL query string. */
		syncUrl?: boolean;
		/** Extra options for the server filter (e.g. from the server inventory). */
		serverOptions?: { id: string; name: string }[];
		/** Let a host own the failed-load state instead of rendering its own. */
		hideErrorState?: boolean;
		/** Notified when the current load error changes (null when clear). */
		onError?: (message: string | null) => void;
	}

	let {
		serverId = '',
		syncUrl = false,
		serverOptions = [],
		hideErrorState = false,
		onError
	}: Props = $props();

	function readFilter(): RosterFilter {
		if (!syncUrl || typeof window === 'undefined') return { ...DEFAULT_ROSTER_FILTER, serverId };
		const params = page.url.searchParams;
		const state = params.get('state');
		const access = params.get('access');
		const sort = params.get('sort');
		return {
			serverId: params.get('server') || serverId,
			query: params.get('q') || '',
			stateFilter: (STATE_FILTERS as readonly string[]).includes(state ?? '')
				? (state as StateFilter)
				: 'all',
			accessFilter: (ACCESS_FILTERS as readonly string[]).includes(access ?? '')
				? (access as AccessFilter)
				: 'all',
			sortBy: (SORT_OPTIONS as readonly string[]).includes(sort ?? '')
				? (sort as SortOption)
				: 'name',
			sortDescending: params.get('dir') === 'desc'
		};
	}

	let filter = $state<RosterFilter>(readFilter());
	const selected = new SvelteSet<string>();
	let expanded = $state<string | null>(null);
	let focusedIndex = $state(-1);
	let toolbarEl = $state<HTMLDivElement | null>(null);
	let listEl = $state<HTMLDivElement | null>(null);

	const roster = createPlayersStore((request) => rpcClient.player.listPlayers(request));
	const rosterState = $derived($roster);

	$effect(() => {
		void roster.load(filter);
	});

	$effect(() => {
		onError?.(rosterState.error);
	});

	// Pure projection: options are derived, never written from an effect.
	// An effect that read and rewrote a server-name map caused an update-depth
	// loop whenever normal roster rows carried a serverId.
	const options = $derived(projectServerOptions(rosterState.players, serverOptions, serverId));

	const visible = $derived(
		neverObservedLast(filterAndSortPlayers(rosterState.players, filter), filter)
	);
	const hasFilters = $derived(
		filter.serverId !== serverId ||
			!!filter.query ||
			filter.stateFilter !== 'all' ||
			filter.accessFilter !== 'all'
	);

	function keyOf(player: PlayerSummary): string {
		return player.ref?.identityKey || `${player.serverId}:${player.ref?.name || 'unknown'}`;
	}

	/**
	 * A player who was never observed in game carries the least information, so
	 * they sort after every observed player in both directions of the "Last
	 * seen" sort (NULLS LAST). The shared projection sorts by the lastSeen
	 * scalar, which cannot express "no observation"; the input order from
	 * filterAndSortPlayers is stable and preserved inside each partition.
	 */
	function neverObservedLast(players: PlayerSummary[], current: RosterFilter): PlayerSummary[] {
		if (current.sortBy !== 'lastSeen') return players;
		const observed = players.filter((player) => !!player.lastSeen);
		const neverObserved = players.filter((player) => !player.lastSeen);
		return [...observed, ...neverObserved];
	}

	function selectedTargets(): PlayerActionTarget[] {
		return visible
			.filter((player) => selected.has(keyOf(player)))
			.map((player) => ({
				identityKey: keyOf(player),
				name: player.ref?.name || keyOf(player)
			}));
	}
	const bulkTargets = $derived(selectedTargets());

	function writeUrl(patch: Partial<RosterFilter>, push: boolean) {
		filter = { ...filter, ...patch };
		if (!syncUrl || typeof window === 'undefined') return;
		const params = new SvelteURLSearchParams(page.url.searchParams.toString());
		const set = (key: string, value: string) => {
			if (value) params.set(key, value);
			else params.delete(key);
		};
		set('server', filter.serverId);
		set('q', filter.query);
		set('state', filter.stateFilter === 'all' ? '' : filter.stateFilter);
		set('access', filter.accessFilter === 'all' ? '' : filter.accessFilter);
		set('sort', filter.sortBy === 'name' ? '' : filter.sortBy);
		set('dir', filter.sortDescending ? 'desc' : '');
		const query = params.toString();
		const url = `${resolve('/players')}?${query}`;
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- URL is resolve('/players') plus an encoded query string
		if (push) void goto(url, { reset: false });
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- URL is resolve('/players') plus an encoded query string
		else replaceState(url, page.state);
	}

	function clearFilters() {
		writeUrl(
			{
				query: '',
				stateFilter: 'all',
				accessFilter: 'all',
				serverId,
				sortBy: 'name',
				sortDescending: false
			},
			true
		);
		selected.clear();
		focusedIndex = -1;
	}

	function ariaSort(column: SortOption): 'ascending' | 'descending' | 'none' {
		if (filter.sortBy !== column) return 'none';
		return filter.sortDescending ? 'descending' : 'ascending';
	}

	function sortByColumn(column: SortOption) {
		writeUrl(
			{
				sortBy: column,
				sortDescending: filter.sortBy === column ? !filter.sortDescending : false
			},
			true
		);
	}

	function refresh() {
		void roster.load(filter);
	}

	function toggleSelected(player: PlayerSummary) {
		const key = keyOf(player);
		if (selected.has(key)) selected.delete(key);
		else selected.add(key);
	}

	function toggleAll() {
		const keys = visible.map(keyOf);
		const allSelected = keys.length > 0 && keys.every((key) => selected.has(key));
		selected.clear();
		if (!allSelected) for (const key of keys) selected.add(key);
	}

	function moveFocus(delta: number) {
		if (visible.length === 0) return;
		focusedIndex = (((focusedIndex + delta) % visible.length) + visible.length) % visible.length;
		listEl?.querySelector<HTMLElement>(`[data-player-row="${focusedIndex}"]`)?.scrollIntoView({
			block: 'nearest'
		});
	}

	function openDetail(player: PlayerSummary) {
		const key = keyOf(player);
		// Carry the row's server scope into detail: every players:* RPC is
		// server_id-scoped, so dropping it would deny scoped grants.
		const scope = player.serverId || serverId;
		const query = scope ? `?server=${encodeURIComponent(scope)}` : '';
		// Kit 3's typed resolve() cannot carry a dynamic query suffix; append it
		// to the resolved route and keep the branded pathname type.
		const href = (resolve('/players/[key]', { key: encodeURIComponent(key) }) +
			query) as ResolvedPathname;
		void goto(href, { reset: false });
	}

	function handleGlobalKeydown(event: KeyboardEvent) {
		if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey) return;
		const target = event.target as HTMLElement | null;
		if (
			target instanceof Element &&
			target.closest('dialog[open], [role="dialog"], [role="alertdialog"]')
		)
			return;
		// Native controls own Enter even when a roster row was highlighted earlier.
		if (event.key === 'Enter' && target instanceof Element && target.closest('button, a[href]'))
			return;
		const typing =
			!!target &&
			(target.tagName === 'INPUT' ||
				target.tagName === 'TEXTAREA' ||
				target.tagName === 'SELECT' ||
				target.isContentEditable);

		if (event.key === '/' && !typing) {
			event.preventDefault();
			toolbarEl?.querySelector<HTMLInputElement>('input')?.focus();
			return;
		}
		if (typing) return;
		if (event.key === 'Escape') {
			selected.clear();
			expanded = null;
			return;
		}
		if (event.key === 'j' || event.key === 'k') {
			event.preventDefault();
			moveFocus(event.key === 'j' ? 1 : -1);
		} else if (event.key === 'Enter' && focusedIndex >= 0 && visible[focusedIndex]) {
			event.preventDefault();
			openDetail(visible[focusedIndex]);
		} else if (event.key === 'x' && focusedIndex >= 0 && visible[focusedIndex]) {
			event.preventDefault();
			toggleSelected(visible[focusedIndex]);
		}
	}
</script>

<svelte:window onkeydown={handleGlobalKeydown} />

<div class="space-y-3 font-sans" aria-busy={rosterState.loading}>
	{#if rosterState.stale}
		<div class="flex flex-wrap items-center gap-2 border border-warning/40 bg-muted p-3 text-xs">
			<CarbonTag type="yellow" size="sm">Stale results</CarbonTag>
			<span class="text-muted-foreground">
				Showing the last successful load. Refresh failed: {rosterState.error ?? 'unknown error'}.
			</span>
			<CarbonButton kind="tertiary" size="sm" onclick={refresh}>Retry</CarbonButton>
		</div>
	{/if}

	<div class="sr-only" aria-live="polite" role="status">
		{#if rosterState.loading}
			Loading players…
		{:else if rosterState.error && !rosterState.loaded}
			Could not load players: {rosterState.error}
		{:else}
			{visible.length} players shown
		{/if}
	</div>

	{#if rosterState.loading && !rosterState.loaded}
		<div class="border border-border bg-card p-6">
			<CarbonInlineLoading description="Loading players…" />
		</div>
	{:else if rosterState.error && !rosterState.loaded}
		{#if !hideErrorState}
			<CarbonErrorState
				title="Could not load players"
				message={rosterState.error}
				onretry={refresh}
			/>
		{/if}
	{:else if visible.length === 0 && !hasFilters}
		<CarbonEmptyState
			title="No players yet"
			description="Players will appear here as their identities are observed."
			actionLabel="Refresh"
			onaction={refresh}
		/>
	{:else}
		<div bind:this={listEl} class="space-y-2">
			<CarbonDataTable title="Player roster" description={rosterState.sourceDetail || undefined}>
				{#snippet toolbar()}
					<div bind:this={toolbarEl} class="flex min-w-0 flex-wrap items-end gap-2">
						<CarbonSearch
							bind:value={filter.query}
							placeholder="Search name, UUID, alias or server…"
							aria-label="Search players"
							size="sm"
							class="w-full sm:w-64"
							oninput={() => writeUrl({ query: filter.query }, false)}
							onkeydown={(event) => {
								if (event.key === 'Escape' && filter.query) {
									event.stopPropagation();
									writeUrl({ query: '' }, true);
								}
							}}
						/>
						{#if !serverId}
							<CarbonSelect
								label="Server"
								value={filter.serverId}
								onchange={(event) =>
									writeUrl({ serverId: (event.currentTarget as HTMLSelectElement).value }, true)}
								class="w-full sm:w-40"
							>
								<option value="">All servers</option>
								{#each options as option (option.id)}
									<option value={option.id}>{option.name}</option>
								{/each}
							</CarbonSelect>
						{/if}
						<CarbonSelect
							label="Presence"
							value={filter.stateFilter}
							onchange={(event) =>
								writeUrl(
									{
										stateFilter: (event.currentTarget as HTMLSelectElement).value as StateFilter
									},
									true
								)}
							class="w-full sm:w-36"
						>
							<option value="all">Any presence</option>
							<option value="online">Online</option>
							<option value="offline">Offline</option>
							<option value="recent">Recently seen</option>
							<option value="unobserved">Unobserved</option>
						</CarbonSelect>
						<CarbonSelect
							label="Access"
							value={filter.accessFilter}
							onchange={(event) =>
								writeUrl(
									{
										accessFilter: (event.currentTarget as HTMLSelectElement).value as AccessFilter
									},
									true
								)}
							class="w-full sm:w-36"
						>
							<option value="all">Any access</option>
							<option value="operator">Operator</option>
							<option value="whitelisted">Allowlisted</option>
							<option value="banned">Banned</option>
						</CarbonSelect>
						<CarbonButton
							kind="secondary"
							size="sm"
							onclick={refresh}
							disabled={rosterState.loading}
						>
							<RefreshCw class="h-3.5 w-3.5" />
							<span>Refresh</span>
						</CarbonButton>
					</div>
				{/snippet}

				{#snippet header()}
					<tr>
						<th scope="col" class="px-3 py-2">
							<input
								type="checkbox"
								aria-label="Select all visible players"
								checked={visible.length > 0 && visible.every((p) => selected.has(keyOf(p)))}
								onchange={toggleAll}
							/>
						</th>
						<th scope="col" class="px-3 py-2" aria-sort={ariaSort('name')}>
							<button
								type="button"
								class="cursor-pointer hover:underline"
								onclick={() => sortByColumn('name')}
							>
								Player {ariaSort('name') === 'none'
									? ''
									: ariaSort('name') === 'ascending'
										? '↑'
										: '↓'}
							</button>
						</th>
						<th scope="col" class="px-3 py-2" aria-sort={ariaSort('server')}>
							<button
								type="button"
								class="cursor-pointer hover:underline"
								onclick={() => sortByColumn('server')}
							>
								Server {ariaSort('server') === 'none'
									? ''
									: ariaSort('server') === 'ascending'
										? '↑'
										: '↓'}
							</button>
						</th>
						<th scope="col" class="px-3 py-2" aria-sort={ariaSort('presence')}>
							<button
								type="button"
								class="cursor-pointer hover:underline"
								onclick={() => sortByColumn('presence')}
							>
								Presence {ariaSort('presence') === 'none'
									? ''
									: ariaSort('presence') === 'ascending'
										? '↑'
										: '↓'}
							</button>
						</th>
						<th scope="col" class="px-3 py-2">Access</th>
						<th scope="col" class="px-3 py-2" aria-sort={ariaSort('lastSeen')}>
							<button
								type="button"
								class="cursor-pointer hover:underline"
								onclick={() => sortByColumn('lastSeen')}
							>
								Last seen {ariaSort('lastSeen') === 'none'
									? ''
									: ariaSort('lastSeen') === 'ascending'
										? '↑'
										: '↓'}
							</button>
						</th>
						<th scope="col" class="px-3 py-2">Actions</th>
					</tr>
				{/snippet}

				{#each visible as player, index (keyOf(player))}
					{@const key = keyOf(player)}
					<tr data-player-row={index} class="align-top {focusedIndex === index ? 'bg-accent' : ''}">
						<td class="px-3 py-2">
							<input
								type="checkbox"
								aria-label="Select {player.ref?.name || key}"
								checked={selected.has(key)}
								onchange={() => toggleSelected(player)}
							/>
						</td>
						<td class="px-3 py-2">
							<div class="flex min-w-0 items-center gap-2">
								<PlayerAvatar
									identityKey={key}
									name={player.ref?.name || key}
									skinRef={player.skinRef}
									serverId={player.serverId}
									skinAvailable={player.skinAvailable}
								/>
								<div class="min-w-0">
									<button
										type="button"
										class="focus-ring block max-w-[16rem] truncate text-left text-xs font-semibold text-accent-text hover:underline"
										onclick={() => openDetail(player)}
									>
										{player.ref?.name || key}
									</button>
									<span class="block truncate font-mono text-xs text-muted-foreground" title={key}>
										{key}
									</span>
								</div>
								<button
									type="button"
									class="focus-ring ml-1 cursor-pointer text-muted-foreground hover:text-foreground"
									aria-label={expanded === key ? 'Collapse details' : 'Expand details'}
									aria-expanded={expanded === key}
									onclick={() => (expanded = expanded === key ? null : key)}
								>
									{#if expanded === key}
										<ChevronDown class="h-4 w-4" />
									{:else}
										<ChevronRight class="h-4 w-4" />
									{/if}
								</button>
							</div>
							{#if expanded === key}
								<div class="mt-2 max-w-md space-y-2 border-l border-border pl-3">
									<PlayerProvenance
										observation={player.presenceObservation}
										label="Presence"
										compact
									/>
									{#if player.aliases.length > 0}
										<p class="text-xs text-muted-foreground">
											Observed aliases:
											<span class="font-mono text-foreground">
												{player.aliases.map((a) => a.name).join(', ')}
											</span>
										</p>
									{/if}
									<p class="text-xs text-muted-foreground">
										Access rank: <span class="font-mono">{accessRank(player)}</span>
									</p>
								</div>
							{/if}
						</td>
						<td class="px-3 py-2 text-xs text-muted-foreground">
							{player.serverName || player.serverId || '—'}
						</td>
						<td class="px-3 py-2">
							<CarbonTag type={presenceTagType(player.presence)} size="sm">
								{presenceLabel(player.presence)}
							</CarbonTag>
							{#if provenanceView(player.presenceObservation) && !provenanceView(player.presenceObservation)?.authoritative}
								<span class="ml-1 text-xs text-warning-text" title="Non-authoritative presence"
									>*</span
								>
							{/if}
						</td>
						<td class="px-3 py-2">
							<div class="flex flex-wrap gap-1">
								{#if player.operator}
									<CarbonTag type="purple" size="sm">Operator</CarbonTag>
								{/if}
								{#if player.whitelisted}
									<CarbonTag type="blue" size="sm">Allowlisted</CarbonTag>
								{/if}
								{#if player.banned}
									<CarbonTag type="red" size="sm">Banned</CarbonTag>
								{/if}
								{#if !player.operator && !player.whitelisted && !player.banned}
									<span class="text-xs text-muted-foreground"
										>{player.accessBand || 'Unrestricted'}</span
									>
								{/if}
							</div>
						</td>
						<td class="px-3 py-2 font-mono text-xs text-muted-foreground">
							{#if player.lastSeen}
								{formatTimestamp(player.lastSeen, 'unavailable')}
							{:else}
								Never observed
							{/if}
						</td>
						<td class="px-3 py-2">
							<div class="flex flex-wrap gap-1">
								<PlayerActions
									serverId={player.serverId}
									targets={[{ identityKey: key, name: player.ref?.name || key }]}
									action="kick"
									unavailableReason={player.serverId ? '' : 'Select a server for this player first'}
									onDone={refresh}
								/>
								<PlayerActions
									serverId={player.serverId}
									targets={[{ identityKey: key, name: player.ref?.name || key }]}
									action="ban"
									unavailableReason={player.serverId ? '' : 'Select a server for this player first'}
									onDone={refresh}
								/>
								<PlayerActions
									serverId={player.serverId}
									targets={[{ identityKey: key, name: player.ref?.name || key }]}
									action={player.operator ? 'deop' : 'op'}
									unavailableReason={player.serverId ? '' : 'Select a server for this player first'}
									onDone={refresh}
								/>
								<PlayerActions
									serverId={player.serverId}
									targets={[{ identityKey: key, name: player.ref?.name || key }]}
									action={player.whitelisted ? 'whitelist_remove' : 'whitelist_add'}
									unavailableReason={player.serverId ? '' : 'Select a server for this player first'}
									onDone={refresh}
								/>
							</div>
						</td>
					</tr>
				{:else}
					<tr>
						<td colspan="7" class="px-4 py-6">
							<div class="flex flex-wrap items-center justify-between gap-2">
								<p class="text-sm text-muted-foreground">No players match these filters.</p>
								<CarbonButton kind="tertiary" size="sm" onclick={clearFilters}>
									Clear filters
								</CarbonButton>
							</div>
						</td>
					</tr>
				{/each}
			</CarbonDataTable>

			{#if bulkTargets.length > 0}
				<div
					class="flex flex-col gap-2 border border-border bg-card p-3 sm:flex-row sm:items-center"
				>
					<div class="min-w-0 flex-1">
						<p class="mono-label-accent">{bulkTargets.length} players selected</p>
						<p class="truncate font-mono text-xs text-foreground">
							{bulkTargets
								.map((t) => t.name)
								.slice(0, 5)
								.join(', ')}{bulkTargets.length > 5 ? ` +${bulkTargets.length - 5} more` : ''}
						</p>
					</div>
					<div class="flex flex-wrap gap-2">
						<PlayerActions
							serverId={serverId || filter.serverId}
							targets={bulkTargets}
							action="kick"
							onDone={refresh}
						/>
						<PlayerActions
							serverId={serverId || filter.serverId}
							targets={bulkTargets}
							action="ban"
							onDone={refresh}
						/>
						<PlayerActions
							serverId={serverId || filter.serverId}
							targets={bulkTargets}
							action="pardon"
							onDone={refresh}
						/>
						<PlayerActions
							serverId={serverId || filter.serverId}
							targets={bulkTargets}
							action="deop"
							onDone={refresh}
						/>
						<PlayerActions
							serverId={serverId || filter.serverId}
							targets={bulkTargets}
							action="whitelist_add"
							onDone={refresh}
						/>
						<PlayerActions
							serverId={serverId || filter.serverId}
							targets={bulkTargets}
							action="whitelist_remove"
							onDone={refresh}
						/>
						<CarbonButton kind="tertiary" size="sm" onclick={() => selected.clear()}>
							Clear selection
						</CarbonButton>
					</div>
				</div>
			{/if}

			<p class="text-xs text-muted-foreground">
				Keyboard: <kbd>/</kbd> search · <kbd>j</kbd>/<kbd>k</kbd> move · <kbd>Enter</kbd> open ·
				<kbd>x</kbd> select · <kbd>Esc</kbd> clear
			</p>
		</div>
	{/if}
</div>
