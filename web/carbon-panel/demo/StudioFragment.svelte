<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import { onMount } from 'svelte';
	import StudioModTable, { type StudioMod } from '$lib/components/fragments/StudioModTable.svelte';
	import CarbonTag from '$lib/components/carbon/CarbonTag.svelte';
	import CarbonButton from '$lib/components/carbon/CarbonButton.svelte';
	import CarbonTabs from '$lib/components/carbon/CarbonTabs.svelte';
	import CarbonSearch from '$lib/components/carbon/CarbonSearch.svelte';
	import CarbonTile from '$lib/components/carbon/CarbonTile.svelte';
	import { sampleCatalog, addCatalogItem, exportSelection } from './studio-model';
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
	let catalogQuery = $state('');
	let activeTab = $state('discover');
	let format = $state('mrpack');
	let includeClient = $state(true);
	let message = $state('Try adding Waystones. Its sample dependency comes along with it.');
	let filtered = $derived(mods.filter((m) => m.name.toLowerCase().includes(query.toLowerCase())));
	let catalog = $derived(
		sampleCatalog.filter((m) =>
			`${m.name} ${m.description}`.toLowerCase().includes(catalogQuery.toLowerCase())
		)
	);
	let exportMods = $derived(exportSelection(mods, includeClient));
	let tabs = $derived([
		{ id: 'discover', label: 'Discover' },
		{ id: 'mods', label: 'Your pack', badge: mods.length },
		{ id: 'export', label: 'Export' }
	]);
	onMount(() => {
		ready = true;
	});
	function reset() {
		mods = initial.map((m) => ({ ...m }));
		selectedSlugs = [];
		query = '';
		catalogQuery = '';
		activeTab = 'discover';
		format = 'mrpack';
		includeClient = true;
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
	function addSample(slug: string) {
		query = '';
		mods = addCatalogItem(mods, slug);
		message =
			slug === 'waystones'
				? 'Waystones and its Balm dependency added to your sample pack.'
				: 'Sodium added as a client-only mod in your sample pack.';
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
		<CarbonTabs {tabs} bind:selectedTab={activeTab} idPrefix="studio-demo" />
		<div
			class="studio-stage"
			role="tabpanel"
			id={`studio-demo-panel-${activeTab}`}
			aria-labelledby={`studio-demo-tab-${activeTab}`}
			tabindex="0"
		>
			{#if activeTab === 'discover'}
				<div class="demo-toolbar">
					<CarbonSearch
						aria-label="Search sample catalog"
						placeholder="Search sample catalog…"
						bind:value={catalogQuery}
						size="sm"
					/>
				</div>
				<div class="studio-catalog">
					<p class="studio-hint">
						A small sample catalog. Versions and dependencies are illustrative.
					</p>
					{#each catalog as item (item.slug)}
						<CarbonTile class="studio-catalog-item">
							<div>
								<h4>{item.name}</h4>
								<p>{item.description}</p>
								<span class="studio-dependency"
									>{item.dependency
										? `+ ${item.dependency.name} · required dependency`
										: 'Client-side · leaves the server pack lean'}</span
								>
							</div>
							<CarbonButton
								size="sm"
								kind="secondary"
								disabled={mods.some((mod) => mod.slug === item.slug)}
								onclick={() => addSample(item.slug)}
								aria-label={`Add ${item.name}`}
							>
								{mods.some((mod) => mod.slug === item.slug) ? 'Added' : '+ Add'}
							</CarbonButton>
						</CarbonTile>
					{:else}<p class="studio-hint">
							No sample mods match. Try “Waystones” or “Sodium”.
						</p>{/each}
					<CarbonButton size="sm" kind="ghost" onclick={() => (activeTab = 'mods')}
						>Curate your pack →</CarbonButton
					>
				</div>
			{:else if activeTab === 'mods'}
				<div class="demo-toolbar">
					<CarbonSearch
						aria-label="Search sample mods"
						placeholder="Filter your mods…"
						bind:value={query}
						size="sm"
					/>
				</div>
				{#if filtered.length}<StudioModTable
						filteredMods={filtered}
						bind:selectedSlugs
						{toggleSide}
						{togglePin}
						{removeMod}
					/>{:else}<p class="demo-body">
						No mods match. Try another search or reset the example.
					</p>{/if}
				<p class="studio-hint studio-table-hint">
					Try the side and pin controls. Your choices carry through to the export preview.
				</p>
			{:else}
				<div class="studio-export">
					<label class="studio-format"
						>Export format<select aria-label="Export format" bind:value={format}
							><option value="mrpack">Modrinth (.mrpack)</option><option value="packwiz"
								>Packwiz archive</option
							></select
						></label
					>
					<label class="studio-client"
						><input type="checkbox" bind:checked={includeClient} /><span
							>Include client-only mods<small>Turn off to preview a server-only pack.</small></span
						></label
					>
					<div class="studio-export-summary">
						<CarbonTag type="blue">{exportMods.length} mods</CarbonTag><code
							>a-little-more-adventure.{format === 'mrpack' ? 'mrpack' : 'zip'}</code
						>
					</div>
					<ul class="studio-export-list" aria-label="Mods in export preview">
						{#each exportMods as mod (mod.slug)}<li>
								<span>{mod.name}</span><span>{mod.side}{mod.pinned ? ' · pinned' : ''}</span>
							</li>{/each}
					</ul>
					<p class="studio-hint">
						Preview only. Install Carbon Panel to resolve real versions and download a working pack.
					</p>
				</div>
			{/if}
		</div>
		<div class="demo-result" role="status">{message}</div>
		<div class="px-5 py-2"><button class="demo-reset" onclick={reset}>↺ Reset example</button></div>
	</fieldset>
</div>
