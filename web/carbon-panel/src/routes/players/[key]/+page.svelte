<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { create } from '@bufbuild/protobuf';
	import { SvelteURLSearchParams } from 'svelte/reactivity';
	import {
		CarbonBreadcrumbs,
		CarbonButton,
		CarbonDataTable,
		CarbonEmptyState,
		CarbonErrorState,
		CarbonInlineLoading,
		CarbonPageHeader,
		CarbonStructuredList,
		CarbonTag,
		CarbonTabs
	} from '$lib/components/carbon';
	import CarbonInactiveTabPanels from '$lib/components/carbon/CarbonInactiveTabPanels.svelte';
	import PlayerAvatar from '$lib/components/player-avatar.svelte';
	import { rpcClient } from '$lib/api/rpc-client';
	import { formatRpcError } from '$lib/api/rpc-error';
	import { formatTimestamp } from '$lib/utils/format-time';
	import {
		GetPlayerDeepDataRequestSchema,
		GetPlayerRequestSchema,
		ListPlayersRequestSchema,
		ListPlayerAccessRequestSchema,
		ListPlayerCapabilitiesRequestSchema,
		ListPlayerHistoryRequestSchema,
		ListPlayerSessionsRequestSchema,
		PlayerSupportState,
		type PlayerAccessState,
		type PlayerCapabilityStatus,
		type PlayerDetail,
		type PlayerField,
		type PlayerHistoryEntry,
		type PlayerSession
	} from '$lib/proto/carbonpanel/v1/player_pb';
	import PlayerActions from '$lib/features/players/PlayerActions.svelte';
	import PlayerProvenance from '$lib/features/players/PlayerProvenance.svelte';
	import {
		capabilityDetail,
		capabilityLabel,
		fieldValue,
		nameHistoryNote,
		presenceLabel,
		presenceTagType,
		tabFields,
		withTabFields,
		supportStateLabel,
		supportStateTagType
	} from '$lib/features/players/presentation';

	const TABS = [
		{ id: 'overview', label: 'Overview' },
		{ id: 'presence', label: 'Presence' },
		{ id: 'character', label: 'Character' },
		{ id: 'progress', label: 'Progress' },
		{ id: 'access', label: 'Access' },
		{ id: 'history', label: 'History' },
		{ id: 'diagnostics', label: 'Diagnostics' }
	] as const;
	type TabId = (typeof TABS)[number]['id'];

	const key = $derived(page.params.key ?? '');
	const serverParam = $derived(page.url.searchParams.get('server') ?? '');
	const tabParam = $derived(page.url.searchParams.get('tab'));

	let activeTab = $state<TabId>('overview');
	let detail = $state<PlayerDetail | null>(null);
	let detailLoading = $state(false);
	let detailError = $state<string | null>(null);
	let capabilities = $state<PlayerCapabilityStatus[]>([]);

	let sessions = $state<PlayerSession[]>([]);
	let accessStates = $state<PlayerAccessState[]>([]);
	let history = $state<PlayerHistoryEntry[]>([]);
	// Character and Progress are separate sections backed by different kinds of
	// read-only data. They must not share one field array: returning to a tab
	// skips its reload, so its fields have to survive the other tab's load.
	let deepFieldsByTab = $state<Record<string, PlayerField[]>>({});
	let deepNoteByTab = $state<Record<string, string>>({});
	let skinRef = $state('');
	// Whether the server reported a resolvable skin for this identity; while it
	// is false the avatar skips the request instead of caching a placeholder.
	let skinAvailable = $state(false);

	let sectionLoading = $state<Record<string, boolean>>({});
	let sectionError = $state<Record<string, string | null>>({});
	let loadedSections = $state<Record<string, boolean>>({});
	let detailGeneration = 0;
	const sectionGenerations: Record<string, number> = {};

	const primaryServerId = $derived(serverParam || detail?.primaryServerId || '');
	const displayName = $derived(detail?.ref?.name || key);
	const activeDeepFields = $derived(tabFields(deepFieldsByTab, activeTab));
	const activeDeepNote = $derived(deepNoteByTab[activeTab] ?? '');

	function resetSections() {
		for (const tab of TABS) sectionGenerations[tab.id] = (sectionGenerations[tab.id] ?? 0) + 1;
		sessions = [];
		accessStates = [];
		history = [];
		deepFieldsByTab = {};
		deepNoteByTab = {};
		capabilities = [];
		skinRef = '';
		skinAvailable = false;
		sectionLoading = {};
		sectionError = {};
		loadedSections = {};
	}

	$effect(() => {
		const requested = TABS.find((tab) => tab.id === tabParam);
		if (requested) activeTab = requested.id;
		else if (!TABS.some((tab) => tab.id === activeTab)) activeTab = 'overview';
	});

	async function loadDetail() {
		const mine = ++detailGeneration;
		detailLoading = true;
		detailError = null;
		resetSections();
		try {
			const response = await rpcClient.player.getPlayer(
				create(GetPlayerRequestSchema, { identityKey: key, serverId: serverParam })
			);
			if (mine !== detailGeneration) return;
			detail = response.detail ?? null;
			if (!detail) detailError = 'The server returned no detail for this identity.';
		} catch (error) {
			if (mine !== detailGeneration) return;
			detailError = formatRpcError(error, 'Could not load player detail.');
		} finally {
			if (mine === detailGeneration) detailLoading = false;
		}
		// Capabilities are server-scoped and needed by Diagnostics regardless of
		// the tab selected first.
		void loadCapabilities(mine);
		void loadSkinRef(mine);
	}

	async function loadSkinRef(generation: number) {
		try {
			const response = await rpcClient.player.listPlayers(
				create(ListPlayersRequestSchema, {
					serverId: serverParam,
					query: key,
					limit: 20
				})
			);
			if (generation !== detailGeneration) return;
			const match = response.players.find((p) => p.ref?.identityKey === key);
			skinRef = match?.skinRef ?? '';
			skinAvailable = match?.skinAvailable ?? false;
		} catch {
			if (generation === detailGeneration) {
				skinRef = '';
				skinAvailable = false;
			}
		}
	}

	async function loadCapabilities(generation: number) {
		const serverId = serverParam || detail?.primaryServerId || '';
		try {
			const response = await rpcClient.player.listPlayerCapabilities(
				create(ListPlayerCapabilitiesRequestSchema, { serverId })
			);
			if (generation !== detailGeneration) return;
			capabilities = response.capabilities ?? [];
		} catch (error) {
			if (generation !== detailGeneration) return;
			sectionError = {
				...sectionError,
				diagnostics: formatRpcError(error, 'Capabilities unavailable.')
			};
		}
	}

	async function loadSection(tab: TabId) {
		if (loadedSections[tab] || sectionLoading[tab]) return;
		const generation = (sectionGenerations[tab] = (sectionGenerations[tab] ?? 0) + 1);
		sectionLoading = { ...sectionLoading, [tab]: true };
		sectionError = { ...sectionError, [tab]: null };
		try {
			switch (tab) {
				case 'presence': {
					const response = await rpcClient.player.listPlayerSessions(
						create(ListPlayerSessionsRequestSchema, {
							identityKey: key,
							serverId: primaryServerId
						})
					);
					if (generation === sectionGenerations[tab]) sessions = response.sessions ?? [];
					break;
				}
				case 'access': {
					const response = await rpcClient.player.listPlayerAccess(
						create(ListPlayerAccessRequestSchema, {
							identityKey: key,
							serverId: primaryServerId
						})
					);
					if (generation === sectionGenerations[tab]) accessStates = response.states ?? [];
					break;
				}
				case 'history': {
					const response = await rpcClient.player.listPlayerHistory(
						create(ListPlayerHistoryRequestSchema, {
							identityKey: key,
							serverId: primaryServerId,
							limit: 100
						})
					);
					if (generation === sectionGenerations[tab]) history = response.entries ?? [];
					break;
				}
				case 'character':
				case 'progress': {
					const kind = tab === 'character' ? 'character' : 'stats';
					if (!primaryServerId) {
						// The snapshot files live on a server. Without a server
						// scope there is nothing to read, and asking for it
						// would only produce a failing request.
						deepNoteByTab = {
							...deepNoteByTab,
							[tab]: 'This identity is not linked to a server, so no snapshot is available.'
						};
						break;
					}
					const response = await rpcClient.player.getPlayerDeepData(
						create(GetPlayerDeepDataRequestSchema, {
							identityKey: key,
							serverId: primaryServerId,
							kind
						})
					);
					if (generation === sectionGenerations[tab]) {
						deepFieldsByTab = withTabFields(deepFieldsByTab, tab, response.fields ?? []);
						deepNoteByTab = { ...deepNoteByTab, [tab]: response.snapshotNote ?? '' };
					}
					break;
				}
				default:
					break;
			}
		} catch (error) {
			if (generation === sectionGenerations[tab])
				sectionError = {
					...sectionError,
					[tab]: formatRpcError(error, `Could not load ${tab} data.`)
				};
		} finally {
			if (generation === sectionGenerations[tab]) {
				// The section counts as attempted even when it failed. A failed
				// section must not retry itself: the loading flag is read by the
				// effect that starts the load, so a section that stayed
				// un-attempted would be requested again on every state change.
				// The error state carries an explicit retry action instead.
				loadedSections = { ...loadedSections, [tab]: true };
				sectionLoading = { ...sectionLoading, [tab]: false };
			}
		}
	}

	$effect(() => {
		void key;
		void serverParam;
		void loadDetail();
	});

	$effect(() => {
		const tab = activeTab;
		if (detail || tab === 'overview' || tab === 'diagnostics') void loadSection(tab);
	});

	function setTab(id: string) {
		const params = new SvelteURLSearchParams(page.url.searchParams.toString());
		if (id === 'overview') params.delete('tab');
		else params.set('tab', id);
		activeTab = id as TabId;
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- URL is a resolve()d player route plus an encoded query string
		void goto(`${resolve('/players/[key]', { key })}?${params.toString()}`, {
			reset: false
		});
	}

	function retryTab(tab: TabId) {
		loadedSections = { ...loadedSections, [tab]: false };
		sectionError = { ...sectionError, [tab]: null };
		void loadSection(tab);
	}
</script>

{#snippet fieldRows(fields: PlayerField[])}
	{#each fields as field, fieldIndex (field.name + field.observation?.source + fieldIndex)}
		<div class="grid gap-2 px-4 py-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
			<div class="min-w-0">
				<p class="mono-label">{field.name}</p>
				<p class="font-mono text-xs break-words text-foreground">{fieldValue(field)}</p>
			</div>
			<PlayerProvenance observation={field.observation} label={field.name} compact />
		</div>
	{:else}
		<p class="px-4 py-3 text-xs text-muted-foreground">No fields reported for this group.</p>
	{/each}
{/snippet}

<div class="h-full flex-1 space-y-4 font-sans" data-testid="showcase-player-detail-content">
	<CarbonBreadcrumbs
		items={[{ label: 'Players', href: resolve('/players') }, { label: displayName }]}
	/>

	<CarbonPageHeader
		title={displayName}
		description="Opaque identity: {key}{primaryServerId ? ` · server: ${primaryServerId}` : ''}"
	>
		{#snippet tag()}
			{#if detail}
				<CarbonTag type={presenceTagType(detail.presence)} size="sm">
					{presenceLabel(detail.presence)}
				</CarbonTag>
			{/if}
		{/snippet}
		{#snippet actions()}
			{#if detail}
				<PlayerAvatar
					identityKey={detail.ref?.identityKey || key}
					name={displayName}
					{skinRef}
					serverId={primaryServerId}
					size="md"
					refreshable
					{skinAvailable}
					showFullSkin
				/>
			{/if}
		{/snippet}
	</CarbonPageHeader>

	{#if detailLoading && !detail}
		<div class="border border-border bg-card p-6">
			<CarbonInlineLoading description="Loading player detail…" />
		</div>
	{:else if detailError && !detail}
		<CarbonErrorState title="Player detail unavailable" message={detailError} />
	{:else if !detail}
		<CarbonEmptyState
			title="No player detail"
			description="This identity was not found or its data is not available."
		/>
	{:else}
		<CarbonTabs tabs={[...TABS]} selectedTab={activeTab} idPrefix="player" onselect={setTab} />
		<CarbonInactiveTabPanels tabs={TABS} selectedTab={activeTab} idPrefix="player" />

		<div class="space-y-4">
			{#if activeTab === 'overview'}
				<section
					role="tabpanel"
					id="player-panel-overview"
					aria-labelledby="player-tab-overview"
					class="focus-ring"
					tabindex="0"
				>
					<CarbonStructuredList>
						{#snippet header()}
							<p class="mono-label-strong">Identity</p>
						{/snippet}
						<div class="grid gap-2 px-4 py-3 sm:grid-cols-2">
							<div>
								<p class="mono-label">Name</p>
								<p class="font-mono text-xs text-foreground">{detail.ref?.name || '—'}</p>
							</div>
							<div>
								<p class="mono-label">UUID</p>
								<p class="font-mono text-xs break-all text-foreground">{detail.ref?.uuid || '—'}</p>
							</div>
							<div>
								<p class="mono-label">XUID</p>
								<p class="font-mono text-xs break-all text-foreground">
									{detail.ref?.xuid || 'unavailable'}
								</p>
							</div>
							<div>
								<p class="mono-label">Offline UUID</p>
								<p class="font-mono text-xs text-foreground">
									{detail.ref?.offlineUuid === undefined
										? 'unavailable'
										: detail.ref.offlineUuid
											? 'Yes'
											: 'No'}
								</p>
							</div>
						</div>
					</CarbonStructuredList>

					<div class="mt-4">
						<CarbonStructuredList>
							{#snippet header()}
								<p class="mono-label-strong">Presence &amp; timeline</p>
							{/snippet}
							<div class="grid gap-2 px-4 py-3 sm:grid-cols-2">
								<div>
									<p class="mono-label">Current presence</p>
									<p class="text-xs text-foreground">{presenceLabel(detail.presence)}</p>
									<PlayerProvenance observation={detail.presenceObservation} compact />
								</div>
								<div>
									<p class="mono-label">First recorded / last observed</p>
									<p class="font-mono text-xs text-foreground">
										{formatTimestamp(detail.firstSeen, 'unavailable')} ·
										{#if detail.lastSeen}
											{formatTimestamp(detail.lastSeen, 'unavailable')}
										{:else}
											<span class="text-muted-foreground">Never observed</span>
										{/if}
									</p>
									<p class="mt-1 text-xs text-muted-foreground">
										First recorded is when this panel first observed the identity; it is not a
										presence claim.
									</p>
								</div>
							</div>
						</CarbonStructuredList>
					</div>

					<div class="mt-4">
						<CarbonStructuredList>
							{#snippet header()}
								<p class="mono-label-strong">Observed aliases</p>
							{/snippet}
							<div class="px-4 py-3">
								<p class="mb-2 text-xs text-warning-text" role="note">
									{nameHistoryNote(detail.nameHistoryNote)}
								</p>
								{#if detail.aliases.length > 0}
									<ul class="space-y-2">
										{#each detail.aliases as alias, aliasIndex (alias.name + alias.source + aliasIndex)}
											<li class="flex flex-wrap items-center gap-2 text-xs">
												<span class="font-mono text-foreground">{alias.name}</span>
												<CarbonTag type="gray" size="sm">
													{formatTimestamp(alias.observedAt, 'unavailable')}
												</CarbonTag>
												<span class="text-muted-foreground">
													observed on {alias.sourceServerId || 'unspecified server'}
												</span>
											</li>
										{/each}
									</ul>
								{:else}
									<p class="text-xs text-muted-foreground">No aliases observed.</p>
								{/if}
							</div>
						</CarbonStructuredList>
					</div>
				</section>
			{:else}
				<section
					role="tabpanel"
					id="player-panel-{activeTab}"
					aria-labelledby="player-tab-{activeTab}"
					class="focus-ring space-y-4"
					tabindex="0"
				>
					{#if sectionLoading[activeTab] && !loadedSections[activeTab]}
						<div class="border border-border bg-card p-6">
							<CarbonInlineLoading description="Loading {activeTab}…" />
						</div>
					{:else if sectionError[activeTab]}
						<CarbonErrorState
							title="{activeTab} unavailable"
							message={sectionError[activeTab] ?? ''}
							onretry={() => retryTab(activeTab)}
						/>
					{:else if activeTab === 'presence'}
						<CarbonDataTable title="Sessions" description="Observed join and leave records.">
							{#snippet header()}
								<tr>
									<th scope="col" class="px-3 py-2">Server</th>
									<th scope="col" class="px-3 py-2">Joined</th>
									<th scope="col" class="px-3 py-2">Left</th>
									<th scope="col" class="px-3 py-2">Source</th>
									<th scope="col" class="px-3 py-2">Estimate</th>
								</tr>
							{/snippet}
							{#each sessions as session (session.identityKey + session.serverId + (session.joinedAt?.seconds ?? '0'))}
								<tr>
									<td class="px-3 py-2 font-mono text-xs">{session.serverId || '—'}</td>
									<td class="px-3 py-2 font-mono text-xs"
										>{formatTimestamp(session.joinedAt, 'unavailable')}</td
									>
									<td class="px-3 py-2 font-mono text-xs"
										>{formatTimestamp(session.leftAt, 'unavailable')}</td
									>
									<td class="px-3 py-2 text-xs text-muted-foreground"
										>{session.source || 'unavailable'}</td
									>
									<td class="px-3 py-2 text-xs">{session.estimate ? 'Yes' : 'No'}</td>
								</tr>
							{:else}
								<tr>
									<td colspan="5" class="px-3 py-4 text-center text-xs text-muted-foreground">
										No sessions were observed for this identity.
									</td>
								</tr>
							{/each}
						</CarbonDataTable>
						<div class="mt-4">
							<CarbonStructuredList>
								{#snippet header()}
									<p class="mono-label-strong">Presence provenance</p>
								{/snippet}
								<div class="px-4 py-3">
									<PlayerProvenance observation={detail.presenceObservation} />
								</div>
							</CarbonStructuredList>
						</div>
					{:else if activeTab === 'character' || activeTab === 'progress'}
						<CarbonStructuredList>
							{#snippet header()}
								<p class="mono-label-strong">
									{activeTab === 'character'
										? 'Character — read-only snapshot'
										: 'Progress — read-only snapshot'}
								</p>
							{/snippet}
							{@render fieldRows(
								activeDeepFields.length > 0
									? activeDeepFields
									: activeTab === 'character'
										? detail.character
										: detail.progress
							)}
						</CarbonStructuredList>
						<p class="text-xs text-muted-foreground" role="note">
							These values are read-only and cannot be edited here.
							{#if activeDeepNote}{activeDeepNote}{/if}
						</p>
					{:else if activeTab === 'access'}
						<CarbonDataTable
							title="Observed access"
							description="Per-server access facts with provenance."
						>
							{#snippet header()}
								<tr>
									<th scope="col" class="px-3 py-2">Server</th>
									<th scope="col" class="px-3 py-2">Allowlist</th>
									<th scope="col" class="px-3 py-2">Operator</th>
									<th scope="col" class="px-3 py-2">Op level</th>
									<th scope="col" class="px-3 py-2">Banned</th>
									<th scope="col" class="px-3 py-2">Mute</th>
									<th scope="col" class="px-3 py-2">Provenance</th>
								</tr>
							{/snippet}
							{#each accessStates as state (state.serverId)}
								<tr>
									<td class="px-3 py-2 font-mono text-xs">{state.serverId}</td>
									<td class="px-3 py-2 text-xs">{state.whitelisted ? 'Yes' : 'No'}</td>
									<td class="px-3 py-2 text-xs">{state.operator ? 'Yes' : 'No'}</td>
									<td class="px-3 py-2 font-mono text-xs">{state.opLevel}</td>
									<td class="px-3 py-2 text-xs">
										<CarbonTag type={state.banned ? 'red' : 'gray'} size="sm">
											{state.banned
												? `Banned${state.banReason ? `: ${state.banReason}` : ''}`
												: 'Not banned'}
										</CarbonTag>
									</td>
									<td class="px-3 py-2 text-xs">{state.mute ? 'Muted' : 'Not muted'}</td>
									<td class="px-3 py-2"
										><PlayerProvenance observation={state.observation} compact /></td
									>
								</tr>
							{:else}
								<tr>
									<td colspan="7" class="px-3 py-4 text-center text-xs text-muted-foreground">
										No access state was observed on any server.
									</td>
								</tr>
							{/each}
						</CarbonDataTable>
						{#if primaryServerId}
							<div class="mt-4 flex flex-wrap gap-2">
								<PlayerActions
									serverId={primaryServerId}
									targets={[{ identityKey: key, name: displayName }]}
									action="ban"
								/>
								<PlayerActions
									serverId={primaryServerId}
									targets={[{ identityKey: key, name: displayName }]}
									action="pardon"
								/>
								<PlayerActions
									serverId={primaryServerId}
									targets={[{ identityKey: key, name: displayName }]}
									action={detail.access.some(
										(f) => f.name === 'operator' && (f.value === 'yes' || f.value === 'true')
									)
										? 'deop'
										: 'op'}
								/>
							</div>
						{/if}
					{:else if activeTab === 'history'}
						<CarbonDataTable
							title="Moderation history"
							description="Audited events for this identity."
						>
							{#snippet header()}
								<tr>
									<th scope="col" class="px-3 py-2">When</th>
									<th scope="col" class="px-3 py-2">Action</th>
									<th scope="col" class="px-3 py-2">Actor</th>
									<th scope="col" class="px-3 py-2">Reason</th>
									<th scope="col" class="px-3 py-2">Status</th>
									<th scope="col" class="px-3 py-2">Undone</th>
								</tr>
							{/snippet}
							{#each history as entry (entry.id)}
								<tr>
									<td class="px-3 py-2 font-mono text-xs"
										>{formatTimestamp(entry.occurredAt, 'unavailable')}</td
									>
									<td class="px-3 py-2 text-xs">{entry.action}</td>
									<td class="px-3 py-2 text-xs">{entry.actorName || '—'}</td>
									<td class="px-3 py-2 text-xs text-muted-foreground">{entry.reason || '—'}</td>
									<td class="px-3 py-2 text-xs">{entry.requestId ? 'Recorded' : '—'}</td>
									<td class="px-3 py-2 text-xs">{entry.undone ? 'Yes' : 'No'}</td>
								</tr>
							{:else}
								<tr>
									<td colspan="6" class="px-3 py-4 text-center text-xs text-muted-foreground">
										No moderation history is recorded for this identity.
									</td>
								</tr>
							{/each}
						</CarbonDataTable>
					{:else if activeTab === 'diagnostics'}
						<CarbonStructuredList>
							{#snippet header()}
								<p class="mono-label-strong">Capabilities</p>
							{/snippet}
							{#each capabilities as capability (capability.capability)}
								<div class="grid gap-2 px-4 py-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)]">
									<div class="space-y-1">
										<p class="text-xs font-semibold text-foreground">
											{capabilityLabel(capability.capability)}
										</p>
										<CarbonTag type={supportStateTagType(capability.state)} size="sm">
											{supportStateLabel(capability.state)}
										</CarbonTag>
									</div>
									<p class="text-xs text-muted-foreground" role="note">
										{capabilityDetail(capability.capability, capability.state, capability.detail)}
									</p>
								</div>
							{:else}
								<p class="px-4 py-3 text-xs text-muted-foreground">
									No capability report is available for this server.
								</p>
							{/each}
						</CarbonStructuredList>
						<p class="text-xs text-muted-foreground" role="note">
							Actions are shown only when your permissions allow them. Features that are not
							available here are described above.
						</p>
						{#if capabilities.some((c) => c.state === PlayerSupportState.UNSUPPORTED || c.state === PlayerSupportState.DECISION_PENDING)}
							<CarbonButton kind="tertiary" size="sm" onclick={() => setTab('access')}>
								Review available access actions
							</CarbonButton>
						{/if}
					{/if}
				</section>
			{/if}
		</div>
	{/if}
</div>
