<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import { onMount } from 'svelte';
	import StudioModTable, { type StudioMod } from '$lib/components/fragments/StudioModTable.svelte';
	import CarbonTag from '$lib/components/carbon/CarbonTag.svelte';
	const initial: StudioMod[] = [
		{
			slug: 'fabric-api',
			name: 'Fabric API',
			file_name: 'fabric-api-sample.jar',
			side: 'both',
			platform: 'Modrinth',
			pinned: false
		},
		{
			slug: 'lithium',
			name: 'Lithium',
			file_name: 'lithium-sample.jar',
			side: 'server',
			platform: 'Modrinth',
			pinned: true
		},
		{
			slug: 'ferrite-core',
			name: 'FerriteCore',
			file_name: 'ferritecore-sample.jar',
			side: 'both',
			platform: 'Modrinth',
			pinned: false
		}
	];
	let ready = $state(false);
	let mods = $state<StudioMod[]>(initial.map((m) => ({ ...m })));
	let selectedSlugs = $state<string[]>([]);
	let query = $state('');
	let message = $state('Search, change a mod’s side, pin a version, or remove a mod.');
	let filtered = $derived(mods.filter((m) => m.name.toLowerCase().includes(query.toLowerCase())));
	onMount(() => {
		ready = true;
	});
	function reset() {
		mods = initial.map((m) => ({ ...m }));
		selectedSlugs = [];
		query = '';
		message = 'Sample modpack restored.';
	}
	function toggleSide(mod: StudioMod) {
		const next = { both: 'server', server: 'client', client: 'both' } as const;
		mods = mods.map((m) => (m.slug === mod.slug ? { ...m, side: next[m.side] } : m));
		message = `${mod.name} side changed in this sample.`;
	}
	function togglePin(mod: StudioMod) {
		mods = mods.map((m) => (m.slug === mod.slug ? { ...m, pinned: !m.pinned } : m));
		message = `${mod.name} version ${mod.pinned ? 'unpinned' : 'pinned'} in this sample.`;
	}
	function removeMod(mod: StudioMod) {
		mods = mods.filter((m) => m.slug !== mod.slug);
		selectedSlugs = selectedSlugs.filter((s) => s !== mod.slug);
		message = `${mod.name} removed from the sample pack.`;
	}
	function addSample() {
		query = '';
		mods = [
			...mods.filter((m) => !['waystones', 'balm'].includes(m.slug)),
			{
				slug: 'waystones',
				name: 'Waystones',
				file_name: 'waystones-sample.jar',
				side: 'both',
				platform: 'Modrinth'
			},
			{
				slug: 'balm',
				name: 'Balm',
				file_name: 'balm-sample.jar',
				side: 'both',
				platform: 'Modrinth'
			}
		];
		message =
			'Sample scenario: Waystones and its Balm dependency added. Compatibility and versions here are illustrative.';
	}
</script>

<div class="fragment-root" data-carbon-theme="dark">
	<fieldset disabled={!ready}>
		<div class="demo-heading">
			<div>
				<h3>A little more adventure</h3>
				<p>MODPACK STUDIO · FABRIC · 1.21.1</p>
			</div>
			<CarbonTag type="blue">{mods.length} mods</CarbonTag>
		</div>
		<div class="demo-toolbar">
			<label
				><span class="sr-only">Search sample mods</span><input
					class="demo-search"
					type="search"
					placeholder="Search mods…"
					bind:value={query}
				/></label
			><button class="sample-action" onclick={addSample}>+ Add sample scenario</button>
		</div>
		{#if filtered.length}<StudioModTable
				filteredMods={filtered}
				bind:selectedSlugs
				{toggleSide}
				{togglePin}
				{removeMod}
			/>{:else}<p class="demo-body">No mods match. Try another search or reset the example.</p>{/if}
		<div class="demo-result" role="status">{message}</div>
		<div class="px-5 py-2"><button class="demo-reset" onclick={reset}>↺ Reset example</button></div>
	</fieldset>
</div>
