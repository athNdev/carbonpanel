<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import { onMount } from 'svelte';
	import { Check, Package, Download, ArrowRight } from '@lucide/svelte';
	import StudioModTable from '$lib/components/fragments/StudioModTable.svelte';
	import CarbonTag from '$lib/components/carbon/CarbonTag.svelte';
	import CarbonButton from '$lib/components/carbon/CarbonButton.svelte';
	import CarbonTabs from '$lib/components/carbon/CarbonTabs.svelte';
	import CarbonSearch from '$lib/components/carbon/CarbonSearch.svelte';
	import {
		sampleCatalog,
		initialMods,
		addCatalogItem,
		removeMods,
		availableUpdates,
		updateMods,
		exportSelection,
		sampleOverrides,
		exportFormats,
		buildExportRecipe,
		type SampleMod,
		type ExportFormat
	} from './studio-model';
	import { publishDemoState } from './feedback';
	let root: HTMLDivElement;
	let ready = $state(false);
	let mods = $state(initialMods());
	let selectedSlugs = $state<string[]>([]);
	let query = $state('');
	let catalogQuery = $state('');
	let category = $state('All');
	let page = $state(0);
	let lastFilter = $state('');
	let activeTab = $state('discover');
	let format = $state<ExportFormat>('mrpack');
	let includeClient = $state(true);
	let includeOverrides = $state(true);
	let checkedUpdates = $state(false);
	let edited = $state(false);
	let message = $state(
		'Start with six mods. Add Waystones and its required library, then make the pack yours.'
	);
	let built = $state<{ revision: string; recipe: ReturnType<typeof buildExportRecipe> }>();
	let revision = $derived(JSON.stringify({ mods, format, includeClient, includeOverrides }));
	let filtered = $derived(
		mods.filter((item) =>
			`${item.name} ${item.version} ${item.side}`.toLowerCase().includes(query.toLowerCase())
		)
	);
	let catalog = $derived(
		sampleCatalog.filter(
			(item) =>
				(category === 'All' || item.category === category) &&
				`${item.name} ${item.description}`.toLowerCase().includes(catalogQuery.toLowerCase())
		)
	);
	let currentPage = $derived(Math.min(page, Math.max(0, Math.ceil(catalog.length / 4) - 1)));
	let visibleCatalog = $derived(catalog.slice(currentPage * 4, currentPage * 4 + 4));
	let exportMods = $derived(exportSelection(mods, includeClient));
	let exportError = $derived.by(() => {
		try {
			buildExportRecipe(mods, { format, includeClient, includeOverrides });
			return '';
		} catch (cause) {
			return cause instanceof Error ? cause.message : 'Review the export selection.';
		}
	});
	let updates = $derived(availableUpdates(mods));
	let updatesMap = $derived(
		checkedUpdates
			? Object.fromEntries(updates.map((item) => [item.slug, { update_available: true }]))
			: {}
	);
	let tabs = $derived([
		{ id: 'discover', label: 'Discover' },
		{ id: 'mods', label: 'Your pack', badge: mods.length },
		{ id: 'overrides', label: 'Overrides', badge: 3 },
		{ id: 'export', label: 'Export' }
	]);
	onMount(() => {
		ready = true;
	});
	$effect(() => {
		const filter = `${catalogQuery}/${category}`;
		if (lastFilter !== filter) {
			page = 0;
			lastFilter = filter;
		}
	});
	$effect(() => {
		if (built && built.revision !== revision) {
			built = undefined;
			message = 'Your pack or options changed. Build a fresh export preview when ready.';
		}
		if (ready) publishDemoState(root, 'studio', built ? 'ready' : edited ? 'curating' : 'idle');
	});
	function reset() {
		mods = initialMods();
		selectedSlugs = [];
		query = '';
		catalogQuery = '';
		category = 'All';
		page = 0;
		activeTab = 'discover';
		format = 'mrpack';
		includeClient = true;
		includeOverrides = true;
		checkedUpdates = false;
		edited = false;
		built = undefined;
		message = 'Six mods and three override files restored. Start a fresh pack.';
	}
	function add(slug: string) {
		const previous = mods;
		mods = addCatalogItem(mods, slug);
		edited = true;
		message = `${mods
			.filter((item) => !previous.some((old) => old.slug === item.slug))
			.map((item) => item.name)
			.join(' + ')} added. Required libraries are included automatically.`;
	}
	function remove(slugs: string[]) {
		const previous = mods;
		mods = removeMods(mods, slugs);
		selectedSlugs = selectedSlugs.filter((id) => mods.some((item) => item.slug === id));
		edited = true;
		message = `Removed ${previous
			.filter((item) => !mods.includes(item))
			.map((item) => item.name)
			.join(', ')}. Dependants of removed libraries leave with them.`;
	}
	function toggleSide(item: SampleMod) {
		const next = { both: 'server', server: 'client', client: 'both' } as const;
		mods = mods.map((mod) => (mod.slug === item.slug ? { ...mod, side: next[mod.side] } : mod));
		edited = true;
		message = `${item.name} now targets ${next[item.side]}. The export will use this choice.`;
	}
	function pin(slugs: string[], pinned: boolean) {
		mods = mods.map((item) => (slugs.includes(item.slug) ? { ...item, pinned } : item));
		edited = true;
		message = `${slugs.length} ${slugs.length === 1 ? 'version' : 'versions'} ${pinned ? 'pinned; update checks will skip them' : 'unpinned'} in this sample.`;
	}
	function bulkSide(side: SampleMod['side']) {
		mods = mods.map((item) => (selectedSlugs.includes(item.slug) ? { ...item, side } : item));
		edited = true;
		message = `${selectedSlugs.length} selected mods now target ${side}.`;
	}
	function checkUpdates() {
		checkedUpdates = true;
		message = `${updates.length} sample updates available. ${mods.filter((item) => item.pinned).length} pinned versions skipped.`;
	}
	function applyUpdates() {
		const count = updates.length;
		mods = updateMods(mods);
		edited = true;
		message = `${count} sample updates applied. Pinned versions kept. Review the file names in your export.`;
	}
	function build() {
		if (exportError) return;
		built = {
			revision,
			recipe: buildExportRecipe(mods, { format, includeClient, includeOverrides })
		};
		message = `Export preview built: ${built.recipe.mods.length} ${built.recipe.mods.length === 1 ? 'mod' : 'mods'}, ${built.recipe.overrides.length} override ${built.recipe.overrides.length === 1 ? 'file' : 'files'}. Inspect it or download the sample manifest.`;
	}
</script>

<div class="fragment-root" data-carbon-theme="dark" bind:this={root}>
	<fieldset disabled={!ready}>
		<div class="demo-heading">
			<div>
				<h3>A little more adventure</h3>
				<p>MODPACK STUDIO · FABRIC · 1.21.1</p>
			</div>
			<CarbonTag type={built ? 'green' : 'blue'}
				>{built ? 'Export ready' : `${mods.length} mods`}</CarbonTag
			>
		</div>
		<div class="demo-guide">
			<span>01 Discover</span><ArrowRight size={12} aria-hidden="true" /><span>02 Curate</span
			><ArrowRight size={12} aria-hidden="true" /><span>03 Package & share</span>
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
						placeholder="Search 12 sample mods…"
						bind:value={catalogQuery}
						size="sm"
					/>
				</div>
				<div class="studio-filters" role="group" aria-label="Catalog category">
					{#each ['All', 'Exploration', 'Performance', 'Utility'] as choice (choice)}<button
							class:active={category === choice}
							aria-pressed={category === choice}
							onclick={() => (category = choice)}>{choice}</button
						>{/each}
				</div>
				<div class="studio-catalog">
					<p class="studio-hint">
						Sample catalog. Versions and dependency relationships are illustrative.
					</p>
					{#each visibleCatalog as item (item.slug)}
						{@const added = mods.some((mod) => mod.slug === item.slug)}
						<article class="studio-catalog-item" class:is-added={added}>
							<span class="catalog-icon" aria-hidden="true"
								><Package size={20} strokeWidth={1.5} /></span
							>
							<div>
								<h4>{item.name}</h4>
								<p>{item.description}</p>
								<span class="studio-dependency"
									>{item.category} · {item.side}{item.requires.length
										? ` · Requires ${item.requires.map((id) => sampleCatalog.find((entry) => entry.slug === id)?.name).join(', ')}`
										: ''}</span
								>
							</div>
							<CarbonButton
								size="sm"
								kind="secondary"
								disabled={added}
								onclick={() => add(item.slug)}
								aria-label={`Add ${item.name}`}>{added ? 'Added' : '+ Add'}</CarbonButton
							>
						</article>
					{:else}<div class="demo-empty">
							<Package size={24} aria-hidden="true" />
							<p>No sample mods match your search.</p>
							<CarbonButton
								kind="ghost"
								size="sm"
								onclick={() => {
									catalogQuery = '';
									category = 'All';
								}}>Clear filters</CarbonButton
							>
						</div>{/each}
					<div class="demo-pagination">
						<span
							>{catalog.length ? currentPage * 4 + 1 : 0}–{Math.min(
								(currentPage + 1) * 4,
								catalog.length
							)} of {catalog.length} mods</span
						>
						<div>
							<CarbonButton
								kind="ghost"
								size="sm"
								disabled={currentPage === 0}
								onclick={() => (page = currentPage - 1)}>Previous</CarbonButton
							><CarbonButton
								kind="ghost"
								size="sm"
								disabled={(currentPage + 1) * 4 >= catalog.length}
								onclick={() => (page = currentPage + 1)}>Next</CarbonButton
							>
						</div>
					</div>
					<CarbonButton size="sm" onclick={() => (activeTab = 'mods')}
						>Curate your pack <ArrowRight size={14} class="ml-3" aria-hidden="true" /></CarbonButton
					>
				</div>
			{:else if activeTab === 'mods'}
				<div class="demo-toolbar">
					<CarbonSearch
						aria-label="Search sample mods"
						placeholder="Filter by name, side or version…"
						bind:value={query}
						size="sm"
					/><CarbonButton size="sm" kind="secondary" onclick={checkUpdates}
						>Check updates</CarbonButton
					>
				</div>
				{#if checkedUpdates}<div class="studio-update-notice">
						<span>{updates.length} updates available · pinned versions skipped</span><CarbonButton
							size="sm"
							kind="ghost"
							disabled={!updates.length}
							onclick={applyUpdates}>Apply sample updates</CarbonButton
						>
					</div>{/if}
				{#if selectedSlugs.length}<div class="studio-bulk" aria-label="Selected mod actions">
						<strong>{selectedSlugs.length} selected</strong><CarbonButton
							kind="ghost"
							size="sm"
							onclick={() => pin(selectedSlugs, true)}>Pin selected</CarbonButton
						><CarbonButton kind="ghost" size="sm" onclick={() => pin(selectedSlugs, false)}
							>Unpin selected</CarbonButton
						><label
							>Side<select
								aria-label="Set side for selected mods"
								value=""
								onchange={(event) => {
									bulkSide(event.currentTarget.value as SampleMod['side']);
									event.currentTarget.value = '';
								}}
								><option value="" disabled>Set side…</option><option value="both">Both</option
								><option value="server">Server</option><option value="client">Client</option
								></select
							></label
						><CarbonButton kind="ghost" size="sm" onclick={() => remove(selectedSlugs)}
							>Remove selected</CarbonButton
						>
					</div>{/if}
				{#if filtered.length}<StudioModTable
						filteredMods={filtered}
						bind:selectedSlugs
						{updatesMap}
						{toggleSide}
						togglePin={(item) => pin([item.slug], !item.pinned)}
						removeMod={(item) => remove([item.slug])}
					/>{:else}<div class="demo-empty">
						<p>
							{mods.length
								? 'No mods match these filters.'
								: 'Your pack is empty. Discover a mod to start again.'}
						</p>
						<CarbonButton
							kind="ghost"
							size="sm"
							onclick={() => {
								query = '';
								activeTab = mods.length ? 'mods' : 'discover';
							}}
						>
							{mods.length ? 'Clear search' : 'Discover mods'}</CarbonButton
						>
					</div>{/if}
				<div class="studio-next">
					<p class="studio-hint">
						Sides and pins carry into your export. Removing a library also removes mods that require
						it.
					</p>
					<CarbonButton size="sm" onclick={() => (activeTab = 'export')}
						>Continue to export <ArrowRight
							size={14}
							class="ml-3"
							aria-hidden="true"
						/></CarbonButton
					>
				</div>
			{:else if activeTab === 'overrides'}
				<div class="studio-export">
					<div>
						<h4 class="demo-subheading">The little details, included.</h4>
						<p class="studio-hint">
							Three sample configuration files travel with your pack. Expand a file to inspect its
							contents.
						</p>
					</div>
					{#each sampleOverrides as file (file.path)}<details class="studio-file">
							<summary
								><code>{file.path}</code><CarbonTag type="gray" size="sm">{file.side}</CarbonTag
								></summary
							>
							<pre>{file.content}</pre>
						</details>{/each}<CarbonButton size="sm" onclick={() => (activeTab = 'export')}
						>Choose export options <ArrowRight
							size={14}
							class="ml-3"
							aria-hidden="true"
						/></CarbonButton
					>
				</div>
			{:else}
				<div class="studio-export">
					<label class="studio-format"
						>Export format<select aria-label="Export format" bind:value={format}
							>{#each Object.entries(exportFormats) as [value, details] (value)}<option {value}
									>{details.label}</option
								>{/each}</select
						></label
					>
					<label class="studio-client"
						><input type="checkbox" bind:checked={includeClient} /><span
							>Include client-only mods<small>Turn off for a server-only selection.</small></span
						></label
					>
					<label class="studio-client"
						><input type="checkbox" bind:checked={includeOverrides} /><span
							>Include override files<small
								>Server-only exports also leave client configuration behind.</small
							></span
						></label
					>
					<div class="studio-export-summary">
						<CarbonTag type="blue">{exportMods.length} mods</CarbonTag><span class="studio-hint"
							>{mods.length - exportMods.length} client-only mods excluded</span
						>
					</div>
					<p class="studio-hint">
						This demo builds an inspectable recipe, not a playable archive. Carbon Panel resolves
						real files and provider IDs when you export from the installed app.
					</p>
					{#if exportError}<p class="demo-warning" role="alert">{exportError}</p>{/if}
					<CarbonButton size="sm" disabled={!!exportError} onclick={build}
						><Package size={14} class="mr-2" aria-hidden="true" />Build export preview</CarbonButton
					>
					{#if built}
						<section class="studio-built" aria-label="Built export preview">
							<div class="studio-export-summary">
								<Check size={18} aria-hidden="true" />
								<h4>Export preview ready.</h4>
							</div>
							<code class="studio-filename">{built.recipe.filename}</code>
							<p>
								{built.recipe.mods.length}
								{built.recipe.mods.length === 1 ? 'mod' : 'mods'} · {built.recipe.overrides.length} override
								{built.recipe.overrides.length === 1 ? 'file' : 'files'} · {exportFormats[
									built.recipe.format
								].label}
							</p>
							<details open>
								<summary>Inspect planned files</summary>
								<ul class="studio-file-tree">
									{#each built.recipe.files as file (file)}<li><code>{file}</code></li>{/each}
								</ul>
							</details>
							<details>
								<summary>Review mod versions and sides</summary>
								<ul class="studio-export-list" aria-label="Mods in export preview">
									{#each built.recipe.mods as item (item.slug)}<li>
											<span>{item.name}<small>{item.version}</small></span><span
												>{item.side}{item.pinned ? ' · pinned' : ''}</span
											>
										</li>{/each}
								</ul>
							</details>
							{#if built.recipe.excluded.length}<p class="studio-hint">
									Left out of this export: {built.recipe.excluded.join(', ')}. They stay in your
									pack.
								</p>{/if}
							<a
								class="demo-download"
								href={`data:application/json;charset=utf-8,${encodeURIComponent(JSON.stringify(built.recipe, null, 2))}`}
								download="a-little-more-adventure.demo.json"
								><Download size={14} aria-hidden="true" />Download sample manifest (.json)</a
							>
						</section>
					{/if}
				</div>
			{/if}
		</div>
		<div class="demo-result" role="status">{message}</div>
		<div class="demo-footer">
			<span>Browser-only sample · nothing installed</span><button class="demo-reset" onclick={reset}
				>↺ Reset example</button
			>
		</div>
	</fieldset>
</div>
