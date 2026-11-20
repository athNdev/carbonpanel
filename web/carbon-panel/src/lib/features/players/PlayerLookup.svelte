<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { resolve } from '$app/paths';
	import { create } from '@bufbuild/protobuf';
	import { timestampDate, type Timestamp } from '@bufbuild/protobuf/wkt';
	import { ArrowUpRight, Copy, Search, UserRound } from '@lucide/svelte';
	import { onDestroy } from 'svelte';
	import { CarbonButton, CarbonTag, CarbonTextInput } from '$lib/components/carbon';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { formatRpcError } from '$lib/api/rpc-error';
	import { authStore } from '$lib/stores/auth';
	import { formatTimestamp } from '$lib/utils/format-time';
	import {
		ListPlayerSessionsRequestSchema,
		LookupPlayerRequestSchema,
		PlayerDataSource,
		PlayerLookupStatus,
		type LookupPlayerResponse,
		type PlayerSkinAsset
	} from '$lib/proto/carbonpanel/v1/player_pb';

	let { serverOptions }: { serverOptions: { id: string; name: string }[] } = $props();

	const exactQuery =
		/^(?:[a-z0-9_]{1,16}|[a-f0-9]{32}|[a-f0-9]{8}-(?:[a-f0-9]{4}-){3}[a-f0-9]{12})$/i;

	let query = $state('');
	let busy = $state(false);
	let response = $state<LookupPlayerResponse | null>(null);
	let error = $state<string | null>(null);
	let copyStatus = $state('');
	let lastSession = $state<{ serverName: string; joinedAt: Timestamp } | null>(null);
	let imageFailed = $state(false);
	let request: AbortController | null = null;
	let generation = 0;
	let sessionRevision = authStore.getSessionRevision();

	const authState = $derived($authStore);
	const canLookup = $derived.by(() => {
		void authState;
		return authStore.hasPermission('players', 'read', '*');
	});
	$effect(() => {
		void authState;
		const currentSession = authStore.getSessionRevision();
		if (currentSession !== sessionRevision) {
			sessionRevision = currentSession;
			clear();
		}
	});

	// This optional section has the existing presence scope and is fetched
	// separately, so session access never gates showing the account.
	$effect(() => {
		const identityKey = response?.observedIdentityKey;
		void authState;
		lastSession = null;
		if (!canLookup || !identityKey || !serverOptions.length) return;
		const controller = new AbortController();
		void loadLastSession(identityKey, serverOptions, controller.signal);
		return () => controller.abort();
	});

	const skinUrl = $derived(imageFailed ? null : portraitUrl(response?.skin));
	const fromPanel = $derived(response?.observation?.source === PlayerDataSource.PANEL_DB);

	const statusLabel: Partial<Record<PlayerLookupStatus, string>> = {
		[PlayerLookupStatus.NOT_FOUND]: 'Account not found',
		[PlayerLookupStatus.AMBIGUOUS]: 'Could not verify an exact match',
		[PlayerLookupStatus.UNAVAILABLE]: 'Lookup temporarily unavailable'
	};

	function portraitUrl(skin: PlayerSkinAsset | undefined) {
		// Lookup returns a small, rendered PNG. Use the panel CSP's data image support.
		if (skin?.contentType !== 'image/png' || !skin.data.length || skin.data.length > 4096) {
			return null;
		}
		return 'data:image/png;base64,' + btoa(String.fromCharCode(...skin.data));
	}

	async function lookup() {
		const value = query.trim();
		if (!value || !canLookup || busy) return;
		const mine = ++generation;
		resetResult();
		if (!exactQuery.test(value)) {
			error = 'Enter an exact Minecraft username or UUID.';
			return;
		}

		const controller = new AbortController();
		request = controller;
		busy = true;
		try {
			const result = await rpcClient.player.lookupPlayer(
				create(LookupPlayerRequestSchema, { query: value, includeSkin: true }),
				{ ...silentCallOptions, signal: controller.signal }
			);
			if (mine !== generation) return;
			response = result;
		} catch (err) {
			if (mine !== generation) return;
			error = formatRpcError(err, 'Could not look up that player.');
		} finally {
			if (mine === generation) {
				busy = false;
				request = null;
			}
		}
	}

	async function loadLastSession(
		identityKey: string,
		servers: { id: string; name: string }[],
		signal: AbortSignal
	) {
		const globalPresence = authStore.hasPermission('players', 'presence', '*');
		const scopes = globalPresence
			? ['']
			: servers
					.filter((server) => authStore.hasPermission('players', 'presence', server.id))
					.map((server) => server.id);
		const results = await Promise.allSettled(
			scopes.map((serverId) =>
				rpcClient.player.listPlayerSessions(
					create(ListPlayerSessionsRequestSchema, { identityKey, serverId }),
					{ ...silentCallOptions, signal }
				)
			)
		);
		if (signal.aborted) return;
		const serverNames = new Map(servers.map(({ id, name }) => [id, name]));
		let latest: typeof lastSession = null;
		let latestTime = -Infinity;
		for (const result of results) {
			if (result.status !== 'fulfilled') continue;
			for (const session of result.value.sessions) {
				const serverName = serverNames.get(session.serverId);
				if (!serverName || session.estimate || !session.joinedAt) continue;
				const time = timestampDate(session.joinedAt).getTime();
				if (time > latestTime) {
					latest = { serverName, joinedAt: session.joinedAt };
					latestTime = time;
				}
			}
		}
		lastSession = latest;
	}

	async function copyUuid(uuid: string) {
		const mine = generation;
		try {
			await navigator.clipboard.writeText(uuid);
			if (mine === generation) copyStatus = 'UUID copied.';
		} catch {
			if (mine === generation) copyStatus = 'Copy failed. Select the UUID to copy it manually.';
		}
	}

	function resetResult() {
		response = null;
		error = null;
		copyStatus = '';
		lastSession = null;
		imageFailed = false;
	}

	function clear() {
		generation += 1;
		request?.abort();
		request = null;
		query = '';
		busy = false;
		resetResult();
	}

	onDestroy(() => {
		generation += 1;
		request?.abort();
	});
</script>

<section class="border border-border bg-card font-sans" aria-labelledby="player-lookup-heading">
	<div class="space-y-4 p-4 sm:p-5">
		<header>
			<h2 id="player-lookup-heading" class="text-lg font-semibold text-foreground">
				Look up a player
			</h2>
			<p class="mt-1 text-sm text-muted-foreground">
				Find a Minecraft account by its exact username or UUID.
			</p>
		</header>

		{#if !canLookup}
			<p class="text-sm text-warning-text" role="note">
				Player lookup requires global permission to read players.
			</p>
		{/if}

		<form
			class="flex max-w-3xl flex-col gap-2 sm:flex-row sm:items-end"
			onsubmit={(event) => {
				event.preventDefault();
				void lookup();
			}}
		>
			<div class="min-w-0 flex-1">
				<CarbonTextInput
					label="Minecraft username or UUID"
					placeholder="e.g. Notch"
					autocomplete="off"
					spellcheck={false}
					bind:value={query}
					disabled={busy || !canLookup}
				/>
			</div>
			<div class="flex shrink-0 gap-2">
				<CarbonButton
					type="submit"
					class="gap-2"
					disabled={!canLookup || query.trim() === ''}
					loading={busy}
				>
					<Search size={16} aria-hidden="true" />
					Look up
				</CarbonButton>
				<CarbonButton
					type="button"
					kind="tertiary"
					onclick={clear}
					disabled={query === '' && !response && !error}
				>
					{busy ? 'Cancel' : 'Clear'}
				</CarbonButton>
			</div>
		</form>
		{#if canLookup && !(response?.status === PlayerLookupStatus.FOUND)}
			<p class="text-xs text-muted-foreground">
				Looking up an account does not add it to your roster.
			</p>
		{/if}

		<div aria-live="polite" role="status" class="sr-only">
			{#if busy}
				Looking up player.
			{:else if response?.status === PlayerLookupStatus.FOUND}
				Profile for {response.name} loaded.
			{:else if response}
				{statusLabel[response.status] ?? 'Could not look up player'}. {response.note}
			{/if}
		</div>
		{#if error}
			<p class="border-l-2 border-destructive pl-3 text-sm text-destructive-text" role="alert">
				{error}
			</p>
		{:else if response && response.status !== PlayerLookupStatus.FOUND}
			<div class="space-y-1 border border-border bg-muted/35 px-4 py-3">
				<p class="text-sm font-semibold">
					{statusLabel[response.status] ?? 'Could not look up player'}
				</p>
				<p class="text-sm text-muted-foreground">{response.note || 'Try again in a moment.'}</p>
			</div>
		{/if}
	</div>

	{#if canLookup && response?.status === PlayerLookupStatus.FOUND}
		<article
			aria-labelledby="lookup-player-name"
			class="grid border-t border-border sm:grid-cols-[12rem_minmax(0,1fr)] lg:grid-cols-[15rem_minmax(0,1fr)]"
		>
			<header class="min-w-0 px-4 pt-5 pb-4 sm:col-start-2 sm:row-start-1 sm:px-6 sm:pt-6">
				<CarbonTag type={fromPanel ? 'gray' : 'blue'} size="sm">
					{fromPanel ? 'Panel observation' : 'Minecraft account'}
				</CarbonTag>
				<h3
					id="lookup-player-name"
					class="mt-2 text-3xl leading-tight font-semibold wrap-anywhere text-foreground"
				>
					{response.name}
				</h3>
			</header>

			<figure
				class="portrait relative grid min-h-60 place-items-center border-y border-border px-4 py-5 sm:col-start-1 sm:row-span-2 sm:row-start-1 sm:border-y-0 sm:border-r"
			>
				{#if skinUrl}
					<img
						src={skinUrl}
						alt={`Full-body skin for ${response.name}`}
						width="96"
						height="192"
						onerror={() => (imageFailed = true)}
					/>
				{:else}
					<div class="grid justify-items-center gap-2 text-muted-foreground">
						<UserRound size={36} strokeWidth={1.25} aria-hidden="true" />
						<span class="text-sm">Skin unavailable</span>
					</div>
				{/if}
			</figure>

			<div class="min-w-0 px-4 pb-5 sm:col-start-2 sm:row-start-2 sm:px-6 sm:pb-6">
				<div class="divide-y divide-border border-y border-border">
					<dl class="min-h-16 py-2">
						<dt class="text-xs text-muted-foreground">UUID</dt>
						<dd class="flex flex-wrap items-center justify-between gap-x-3">
							<span class="min-w-0 font-mono text-sm wrap-anywhere text-foreground select-all">
								{response.uuid || 'Unavailable'}
							</span>
							{#if response.uuid}
								<CarbonButton
									type="button"
									kind="tertiary"
									size="sm"
									class="gap-2"
									onclick={() => response && copyUuid(response.uuid)}
									aria-label="Copy UUID"
								>
									<Copy size={16} aria-hidden="true" />
									Copy
								</CarbonButton>
							{/if}
							{#if copyStatus}
								<p class="w-full text-xs text-muted-foreground" role="status">{copyStatus}</p>
							{/if}
						</dd>
					</dl>
					<dl class="grid gap-x-3 gap-y-1 py-3 md:grid-cols-2">
						<dt class="text-xs text-muted-foreground">Identity source</dt>
						<dd class="text-sm md:col-start-1 md:row-start-2">
							{fromPanel ? 'Carbon Panel observations' : 'Mojang'}
						</dd>
						<dt class="mt-2 text-xs text-muted-foreground md:col-start-2 md:row-start-1 md:mt-0">
							{fromPanel ? 'Last seen on this panel' : 'Checked at'}
						</dt>
						<dd class="text-sm md:col-start-2 md:row-start-2">
							{formatTimestamp(response.observation?.observedAt, 'Unavailable')}
						</dd>
					</dl>
					{#if response.lastSeen && !fromPanel}
						<dl class="py-3">
							<dt class="text-xs text-muted-foreground">Last seen on this panel</dt>
							<dd class="mt-1 text-sm">{formatTimestamp(response.lastSeen)}</dd>
						</dl>
					{/if}
					{#if lastSession}
						<dl class="py-3">
							<dt class="text-xs text-muted-foreground">Recorded server join</dt>
							<dd class="mt-1 text-sm">
								{lastSession.serverName} · {formatTimestamp(lastSession.joinedAt)}
							</dd>
						</dl>
					{/if}
				</div>
				{#if response.observedIdentityKey}
					<CarbonButton
						kind="tertiary"
						size="sm"
						class="mt-2 gap-1"
						href={resolve('/players/[key]', { key: response.observedIdentityKey })}
					>
						View player record <ArrowUpRight size={16} aria-hidden="true" />
					</CarbonButton>
				{/if}
				{#if fromPanel}
					<p class="mt-2 text-xs text-muted-foreground">
						The panel matched a stored observation. Mojang returned no profile.
					</p>
				{/if}
			</div>
		</article>
	{/if}
</section>

<style>
	.portrait {
		background-color: var(--muted);
		background-image: radial-gradient(var(--border) 1px, transparent 1px);
		background-size: 12px 12px;
	}

	.portrait img {
		width: 96px;
		height: 192px;
		image-rendering: pixelated;
		object-fit: contain;
		filter: drop-shadow(0 4px 6px rgb(0 0 0 / 18%));
	}
</style>
