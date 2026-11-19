<!-- SPDX-License-Identifier: MPL-2.0 -->
<script>
	import PlayerLookup from '$lib/features/players/PlayerLookup.svelte';
	import AddAllowlistEntry from '$lib/features/players/AddAllowlistEntry.svelte';

	let generation = $state(0);
	let lastDone = $state(0);

	function bump() {
		generation += 1;
	}
</script>

<main class="min-h-screen bg-background p-4 font-sans">
	<h1 class="mb-4 text-lg font-semibold">OSS-383 scoped player QA fixture</h1>
	<div class="mb-4 flex flex-wrap gap-2" data-testid="scenario-controls">
		<button type="button" class="border px-2 py-1 text-sm" onclick={() => { __qa.reset(); bump(); }}>
			Reset
		</button>
		<button type="button" class="border px-2 py-1 text-sm" data-scenario="found"
			onclick={() => { __qa.set({ lookup: 'found' }); bump(); }}>Lookup found</button>
		<button type="button" class="border px-2 py-1 text-sm" data-scenario="not_found"
			onclick={() => { __qa.set({ lookup: 'not_found' }); bump(); }}>Lookup not found</button>
		<button type="button" class="border px-2 py-1 text-sm" data-scenario="ambiguous"
			onclick={() => { __qa.set({ lookup: 'ambiguous' }); bump(); }}>Lookup ambiguous</button>
		<button type="button" class="border px-2 py-1 text-sm" data-scenario="unavailable"
			onclick={() => { __qa.set({ lookup: 'unavailable' }); bump(); }}>Lookup outage</button>
		<button type="button" class="border px-2 py-1 text-sm" data-scenario="race"
			onclick={() => { __qa.reset(); __qa.set({ lookup: 'race' }); bump(); }}>Lookup race</button>
		<button type="button" class="border px-2 py-1 text-sm" data-whitelist="success"
			onclick={() => { __qa.set({ whitelistAdd: 'success' }); bump(); }}>Whitelist success</button>
		<button type="button" class="border px-2 py-1 text-sm" data-whitelist="failure"
			onclick={() => { __qa.set({ whitelistAdd: 'failure' }); bump(); }}>Whitelist failure</button>
		<button type="button" class="border px-2 py-1 text-sm" data-whitelist="denied"
			onclick={() => { __qa.set({ whitelistAdd: 'denied' }); bump(); }}>Whitelist denied</button>
		<button type="button" class="border px-2 py-1 text-sm" data-whitelist="bedrock_capability"
			onclick={() => { __qa.set({ whitelistAdd: 'bedrock_capability' }); bump(); }}>Bedrock capability</button>
		<button type="button" class="border px-2 py-1 text-sm" data-perm="none"
			onclick={() => { __qaAuth.set([]); bump(); }}>No permission</button>
		<button type="button" class="border px-2 py-1 text-sm" data-perm="granted"
			onclick={() => {
				__qaAuth.set([
					{ resource: 'players', action: 'read', objectId: '*' },
					{ resource: 'players', action: 'access:write', objectId: 'srv-1' }
				]);
				bump();
			}}>Granted permission</button>
	</div>
	<p data-testid="meta" class="mb-4 text-xs">generation={generation} done={lastDone}</p>
	{#key generation}
		<div class="space-y-6">
			<PlayerLookup />
			<AddAllowlistEntry serverId="srv-1" onDone={() => (lastDone += 1)} />
		</div>
	{/key}
</main>
