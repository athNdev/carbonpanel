<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { resolve } from '$app/paths';
	import { CarbonButton, CarbonErrorState } from '$lib/components/carbon';
	import { UserPlus } from '@lucide/svelte';
	import ServerTabPanel from '$lib/components/server-ui/ServerTabPanel.svelte';
	import PlayerRoster from '$lib/features/players/PlayerRoster.svelte';
	import AddAllowlistEntry from '$lib/features/players/AddAllowlistEntry.svelte';
	import type { Server } from '$lib/proto/carbonpanel/v1/common_pb';

	let { server, active = false }: { server: Server; active?: boolean } = $props();

	// Preserve the tab contract: the server-detail host decides when this working
	// pane is visible. Mounting the roster only while active avoids firing the
	// cross-server-capable list RPC for a hidden tab.
	const mounted = $derived(active);

	// This tab owns its failed-load state and retry, while the shared roster
	// still owns the distinction between an empty result and a failed request.
	let loadError = $state<string | null>(null);
	let rosterGeneration = $state(0);
	let allowlistOpen = $state(false);

	$effect(() => {
		void server.id;
		void active;
		allowlistOpen = false;
	});

	function loadPlayers() {
		loadError = null;
		rosterGeneration += 1;
	}
</script>

<ServerTabPanel
	title="Players"
	description="Manage players and access for {server.name || 'this server'}."
>
	{#snippet actions()}
		<CarbonButton
			kind="tertiary"
			size="sm"
			href={resolve('/players')}
			aria-label="Open the cross-server player roster"
		>
			All players
		</CarbonButton>
		{#if mounted}
			<CarbonButton kind="secondary" size="sm" onclick={() => (allowlistOpen = true)}>
				<UserPlus class="mr-1.5 size-4" aria-hidden="true" />
				Add player to allowlist
			</CarbonButton>
		{/if}
	{/snippet}

	<div class="min-h-0 flex-1 overflow-auto p-3 sm:p-4">
		{#if loadError}
			<CarbonErrorState title="Could not load players" message={loadError} onretry={loadPlayers} />
		{:else if mounted}
			{#key rosterGeneration}
				<PlayerRoster
					serverId={server.id}
					hideErrorState={true}
					onError={(message) => (loadError = message)}
				/>
			{/key}
		{/if}
	</div>
	{#key server.id}
		<AddAllowlistEntry
			serverId={server.id}
			serverName={server.name}
			onDone={loadPlayers}
			bind:dialogOpen={allowlistOpen}
		/>
	{/key}
</ServerTabPanel>
