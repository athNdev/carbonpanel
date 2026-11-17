<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import { onMount } from 'svelte';
	import { Check, Package, Download, ArrowRight, RotateCcw } from '@lucide/svelte';
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
	let message = $state('Studio ready');
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
			message = 'Export settings changed';
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
		message = 'Pack reset';
	}
	function add(slug: string) {
		const previous = mods;
		mods = addCatalogItem(mods, slug);
		edited = true;
		message = `${mods
			.filter((item) => !previous.some((old) => old.slug === item.slug))
			.map((item) => item.name)
			.join(', ')} added`;
	}
	function remove(slugs: string[]) {
		const previous = mods;
		mods = removeMods(mods, slugs);
		selectedSlugs = selectedSlugs.filter((id) => mods.some((item) => item.slug === id));
		edited = true;
		message = `${previous
			.filter((item) => !mods.includes(item))
			.map((item) => item.name)
			.join(', ')} removed`;
	}
	function toggleSide(item: SampleMod) {
		const next = { both: 'server', server: 'client', client: 'both' } as const;
		mods = mods.map((mod) => (mod.slug === item.slug ? { ...mod, side: next[mod.side] } : mod));
		edited = true;
		message = `${item.name}: ${next[item.side]}`;
	}
	function pin(slugs: string[], pinned: boolean) {
		mods = mods.map((item) => (slugs.includes(item.slug) ? { ...item, pinned } : item));
		edited = true;
		message = `${slugs.length} versions ${pinned ? 'pinned' : 'unpinned'}`;
	}
	function bulkSide(side: SampleMod['side']) {
		mods = mods.map((item) => (selectedSlugs.includes(item.slug) ? { ...item, side } : item));
		edited = true;
		message = `${selectedSlugs.length} mods: ${side}`;
	}
	function checkUpdates() {
		checkedUpdates = true;
		message = `${updates.length} updates available`;
	}
	function applyUpdates() {
		const count = updates.length;
		mods = updateMods(mods);
		edited = true;
		message = `${count} updates applied`;
	}
	function build() {
		if (exportError) return;
		built = {
			revision,
			recipe: buildExportRecipe(mods, { format, includeClient, includeOverrides })
		};
		message = 'Export preview ready';
	}

	let overrideCount = $derived(
		includeOverrides
			? sampleOverrides.filter((file) => includeClient || file.side !== 'client').length
			: 0
	);
</script>

<div class="fragment-root studio-demo" data-carbon-theme="dark" bind:this={root}>
	<fieldset disabled={!ready}>
		<header class="demo-heading">
			<div class="demo-identity">
				<span class="demo-eyebrow">MODPACK STUDIO</span>
				<h3>A little more adventure</h3>
				<p>Fabric · Minecraft 1.21.1</p>
			</div>
			<div class="demo-heading-actions">
				<CarbonTag type="gray">{mods.length} {mods.length === 1 ? 'mod' : 'mods'}</CarbonTag><button
					class="demo-reset"
					onclick={reset}
					aria-label="Reset example"
					title="Reset example"><RotateCcw size={15} aria-hidden="true" /></button
				>
			</div>
		</header>
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
						placeholder="Search mods…"
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
					{#each visibleCatalog as item (item.slug)}{@const added = mods.some(
							(mod) => mod.slug === item.slug
						)}
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
							<Package size={26} strokeWidth={1.5} aria-hidden="true" />
							<h4>No results</h4>
							<CarbonButton
								kind="secondary"
								size="sm"
								onclick={() => {
									catalogQuery = '';
									category = 'All';
								}}>Clear filters</CarbonButton
							>
						</div>{/each}
					<div class="catalog-footer">
						<div class="demo-pagination">
							<span
								>{catalog.length ? currentPage * 4 + 1 : 0}–{Math.min(
									(currentPage + 1) * 4,
									catalog.length
								)} of {catalog.length}</span
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
						<div class="demo-action-row">
							<CarbonButton size="sm" onclick={() => (activeTab = 'mods')}
								>Curate pack<ArrowRight size={14} class="ml-3" aria-hidden="true" /></CarbonButton
							>
						</div>
					</div>
				</div>
			{:else if activeTab === 'mods'}
				<div class="demo-toolbar">
					<CarbonSearch
						aria-label="Search sample mods"
						placeholder="Filter mods…"
						bind:value={query}
						size="sm"
					/><CarbonButton size="sm" kind="secondary" onclick={checkUpdates}
						>Check updates</CarbonButton
					>
				</div>
				{#if checkedUpdates}<div class="studio-update-notice" aria-label="Available updates">
						<div>
							<strong>{updates.length} updates</strong><span
								>{mods.filter((item) => item.pinned).length} pinned</span
							>
						</div>
						<CarbonButton
							size="sm"
							kind="secondary"
							disabled={!updates.length}
							onclick={applyUpdates}>Update all</CarbonButton
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
						<Package size={26} strokeWidth={1.5} aria-hidden="true" />
						<h4>{mods.length ? 'No results' : 'No mods'}</h4>
						<CarbonButton
							kind="secondary"
							size="sm"
							onclick={() => {
								query = '';
								activeTab = mods.length ? 'mods' : 'discover';
							}}>{mods.length ? 'Clear search' : 'Discover mods'}</CarbonButton
						>
					</div>{/if}
				<div class="studio-next">
					<dl class="side-breakdown">
						{#each ['both', 'server', 'client'] as side (side)}<div>
								<dt>{side}</dt>
								<dd>{mods.filter((item) => item.side === side).length}</dd>
							</div>{/each}
					</dl>
					<CarbonButton size="sm" onclick={() => (activeTab = 'export')}
						>Continue to export<ArrowRight
							size={14}
							class="ml-3"
							aria-hidden="true"
						/></CarbonButton
					>
				</div>
			{:else if activeTab === 'overrides'}
				<div class="studio-export">
					<div class="section-heading">
						<h4>Override files</h4>
						<span class="section-count">{sampleOverrides.length}</span>
					</div>
					{#each sampleOverrides as file (file.path)}<details class="studio-file">
							<summary
								><code>{file.path}</code><CarbonTag type="gray" size="sm">{file.side}</CarbonTag
								></summary
							>
							<pre>{file.content}</pre>
						</details>{/each}
					<div class="demo-action-row">
						<CarbonButton size="sm" onclick={() => (activeTab = 'export')}
							>Export options<ArrowRight size={14} class="ml-3" aria-hidden="true" /></CarbonButton
						>
					</div>
				</div>
			{:else}
				<div class="studio-export">
					<div class="section-heading">
						<h4>Export options</h4>
						<span class="output-kind">JSON sample</span>
					</div>
					<label class="studio-format"
						>Format<select aria-label="Export format" bind:value={format}
							>{#each Object.entries(exportFormats) as [value, details] (value)}<option {value}
									>{details.label}</option
								>{/each}</select
						></label
					>
					<fieldset class="export-content">
						<legend>Included content</legend>
						<div class="export-toggles">
							<label class="studio-client"
								><input type="checkbox" bind:checked={includeClient} /><span
									>Client-only mods<small
										>{mods.filter((item) => item.side === 'client').length} mods</small
									></span
								></label
							><label class="studio-client"
								><input type="checkbox" bind:checked={includeOverrides} /><span
									>Override files<small>{sampleOverrides.length} files</small></span
								></label
							>
						</div>
					</fieldset>
					<dl class="demo-metrics export-metrics">
						<div>
							<dt>Mods</dt>
							<dd>{exportMods.length}</dd>
						</div>
						<div>
							<dt>Overrides</dt>
							<dd>{overrideCount}</dd>
						</div>
						<div>
							<dt>Excluded</dt>
							<dd>{mods.length - exportMods.length}</dd>
						</div>
					</dl>
					{#if exportError}<p class="demo-warning" role="alert">{exportError}</p>{/if}
					{#if !built}<div class="demo-action-row">
							<CarbonButton size="sm" disabled={!!exportError} onclick={build}
								><Package size={14} class="mr-2" aria-hidden="true" />Build export preview</CarbonButton
							>
						</div>
					{:else}<section class="studio-built" aria-label="Built export preview">
							<div class="outcome-heading">
								<span class="outcome-icon success"><Check size={22} aria-hidden="true" /></span>
								<div>
									<span class="demo-eyebrow">COMPLETED</span>
									<h4>Export ready</h4>
								</div>
							</div>
							<div class="export-identity">
								<code class="studio-filename">{built.recipe.filename}</code><span
									>{exportFormats[built.recipe.format].label}</span
								>
							</div>
							<details open>
								<summary
									>Package contents<span class="section-count">{built.recipe.files.length}</span
									></summary
								>
								<ul class="studio-file-tree">
									{#each built.recipe.files as file (file)}<li><code>{file}</code></li>{/each}
								</ul>
							</details>
							<details>
								<summary
									>Mod versions<span class="section-count">{built.recipe.mods.length}</span
									></summary
								>
								<ul class="studio-export-list" aria-label="Mods in export preview">
									{#each built.recipe.mods as item (item.slug)}<li>
											<span>{item.name}<small>{item.version}</small></span><span
												>{item.side}{item.pinned ? ' · pinned' : ''}</span
											>
										</li>{/each}
								</ul>
							</details>
							{#if built.recipe.excluded.length}<details>
									<summary
										>Excluded mods<span class="section-count">{built.recipe.excluded.length}</span
										></summary
									>
									<ul class="studio-excluded-list">
										{#each built.recipe.excluded as name (name)}<li>{name}</li>{/each}
									</ul>
								</details>{/if}
							<div class="demo-action-row">
								<a
									class="demo-download"
									href={`data:application/json;charset=utf-8,${encodeURIComponent(JSON.stringify(built.recipe, null, 2))}`}
									download="a-little-more-adventure.demo.json"
									><Download size={14} aria-hidden="true" />Download sample manifest</a
								>
							</div>
						</section>{/if}
				</div>
			{/if}
		</div>
		<div class="sr-only" role="status">{message}</div>
	</fieldset>
</div>
