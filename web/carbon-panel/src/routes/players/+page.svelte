<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { onMount } from 'svelte';
	import { CarbonPageHeader, CarbonTag } from '$lib/components/carbon';
	import PlayerRoster from '$lib/features/players/PlayerRoster.svelte';
	import PlayerLookup from '$lib/features/players/PlayerLookup.svelte';
	import { serversStore } from '$lib/stores/servers';

	const serverOptions = $derived(
		$serversStore.map((server) => ({ id: server.id, name: server.name || server.id }))
	);

	onMount(() => {
		if ($serversStore.length === 0) void serversStore.fetchServers(true);
	});
</script>

<div class="h-full flex-1 space-y-6 font-sans" data-testid="showcase-players-content">
	<CarbonPageHeader
		title="Players"
		description="Search player identities across every server, then inspect observed data and moderate with an audit trail."
	>
		{#snippet tag()}
			<CarbonTag type="blue" size="sm">CROSS-SERVER</CarbonTag>
		{/snippet}
	</CarbonPageHeader>

	<PlayerLookup />
	<PlayerRoster syncUrl={true} {serverOptions} />
</div>
