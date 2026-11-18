<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { CarbonButton, CarbonTag, CarbonTextInput } from '$lib/components/carbon';
	import { rpcClient } from '$lib/api/rpc-client';
	import { formatRpcError } from '$lib/api/rpc-error';
	import { authStore } from '$lib/stores/auth';
	import {
		LookupPlayerRequestSchema,
		PlayerLookupStatus,
		type LookupPlayerResponse
	} from '$lib/proto/carbonpanel/v1/player_pb';
	import PlayerProvenance from './PlayerProvenance.svelte';

	let query = $state('');
	let busy = $state(false);
	let response = $state<LookupPlayerResponse | null>(null);
	let error = $state<string | null>(null);
	// Monotonic generation: a provider reply for a superseded query is dropped
	// so an out-of-order response can never overwrite the current result.
	let generation = 0;

	const authState = $derived($authStore);
	const canLookup = $derived.by(() => {
		void authState;
		return authStore.hasPermission('players', 'read', '*');
	});

	const statusLabel: Record<number, string> = {
		[PlayerLookupStatus.FOUND]: 'Found',
		[PlayerLookupStatus.NOT_FOUND]: 'Not found',
		[PlayerLookupStatus.AMBIGUOUS]: 'Ambiguous',
		[PlayerLookupStatus.UNAVAILABLE]: 'Unavailable'
	};

	const statusTag: Record<number, 'green' | 'gray' | 'yellow' | 'red'> = {
		[PlayerLookupStatus.FOUND]: 'green',
		[PlayerLookupStatus.NOT_FOUND]: 'gray',
		[PlayerLookupStatus.AMBIGUOUS]: 'yellow',
		[PlayerLookupStatus.UNAVAILABLE]: 'red'
	};

	async function lookup() {
		const value = query.trim();
		if (!value || !canLookup) return;
		const mine = ++generation;
		busy = true;
		error = null;
		response = null;
		try {
			const result = await rpcClient.player.lookupPlayer(
				create(LookupPlayerRequestSchema, { query: value })
			);
			if (mine !== generation) return;
			response = result;
		} catch (err) {
			if (mine !== generation) return;
			error = formatRpcError(err, 'Could not look up that player.');
		} finally {
			if (mine === generation) busy = false;
		}
	}

	function clear() {
		generation += 1;
		query = '';
		busy = false;
		response = null;
		error = null;
	}
</script>

<section class="border border-border bg-card p-4 font-sans" aria-labelledby="player-lookup-heading">
	<h2 id="player-lookup-heading" class="mono-label-accent">Look up a player</h2>
	<p class="mt-1 text-xs text-muted-foreground">
		Find an account that has never played here by its exact UUID or current username. This uses the
		provider's identity capability; no name history is queried or shown. A lookup is read-only: it
		creates no identity, roster entry or presence claim.
	</p>

	{#if !canLookup}
		<p class="mt-2 text-xs text-warning-text" role="note">
			You need the global <code>players:read</code> grant to look up new identities.
		</p>
	{/if}

	<form
		class="mt-3 flex flex-col gap-2 sm:flex-row sm:items-end"
		onsubmit={(event) => {
			event.preventDefault();
			void lookup();
		}}
	>
		<div class="min-w-0 flex-1">
			<CarbonTextInput
				label="Exact UUID or username"
				placeholder="e.g. 069a79f4-44e9-4726-a5be-fca90e38aaf5 or Notch"
				bind:value={query}
				disabled={busy || !canLookup}
			/>
		</div>
		<div class="flex shrink-0 gap-2">
			<CarbonButton
				kind="primary"
				size="sm"
				type="submit"
				disabled={!canLookup || query.trim() === ''}
			>
				Look up
			</CarbonButton>
			<CarbonButton
				kind="tertiary"
				size="sm"
				onclick={clear}
				disabled={query === '' && !response && !error}
			>
				Clear
			</CarbonButton>
		</div>
	</form>

	<div aria-live="polite" role="status" class="mt-3">
		{#if error}
			<p class="text-xs text-destructive-text" role="alert">{error}</p>
		{:else if response}
			<div class="space-y-2">
				<div class="flex flex-wrap items-center gap-2">
					<CarbonTag type={statusTag[response.status] ?? 'gray'} size="sm">
						{statusLabel[response.status] ?? 'Unknown'}
					</CarbonTag>
					{#if response.status === PlayerLookupStatus.FOUND}
						<span class="text-sm font-semibold text-foreground">{response.name}</span>
						<span class="font-mono text-xs break-all text-muted-foreground">{response.uuid}</span>
					{/if}
				</div>
				{#if response.note}
					<p
						class="text-xs {response.status === PlayerLookupStatus.FOUND
							? 'text-muted-foreground'
							: 'text-warning-text'}"
					>
						{response.note}
					</p>
				{/if}
				{#if response.status === PlayerLookupStatus.FOUND}
					<PlayerProvenance observation={response.observation} label="Source" compact />
				{:else}
					<p class="text-xs text-muted-foreground">
						{#if response.status === PlayerLookupStatus.AMBIGUOUS}
							Enter the exact UUID instead of a name to disambiguate.
						{:else if response.status === PlayerLookupStatus.UNAVAILABLE}
							The provider could not answer (outage or timeout); retry shortly. This is not a "not
							found" result.
						{:else}
							No identity was created because the provider authoritatively reported no match.
						{/if}
					</p>
				{/if}
			</div>
		{/if}
	</div>
</section>
