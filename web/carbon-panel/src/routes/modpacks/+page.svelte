<!--
SPDX-License-Identifier: MIT AND MPL-2.0
This file includes DiscoPanel material under the MIT License,
Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
and still applies to that inherited material.
Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
If a copy of the MPL was not distributed with this file, You can obtain
one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import {
		CarbonButton,
		CarbonTile,
		CarbonTag,
		CarbonSearch,
		CarbonSelect,
		CarbonModal,
		CarbonTextInput,
		CarbonConfirm,
		CarbonTabs,
		CarbonPageHeader
	} from '$lib/components/carbon';
	import { toast } from 'svelte-sonner';
	import {
		Heart,
		Download,
		RefreshCw,
		ExternalLink,
		Settings,
		Upload,
		Package,
		Trash2,
		X,
		Globe,
		Loader2,
		Boxes,
		KeyRound,
		FileSearch
	} from '@lucide/svelte';
	import ManifestInspectorDialog from '$lib/components/manifest-inspector-dialog.svelte';
	import { CarbonErrorState } from '$lib/components/carbon';
	import { create } from '@bufbuild/protobuf';
	import type {
		IndexedModpack,
		SearchModpacksRequest,
		SearchModpacksResponse,
		GetIndexerStatusResponse
	} from '$lib/proto/carbonpanel/v1/modpack_pb';
	import { SearchModpacksRequestSchema } from '$lib/proto/carbonpanel/v1/modpack_pb';
	import { NodeStatus } from '$lib/proto/carbonpanel/v1/node_pb';
	import { rpcClient } from '$lib/api/rpc-client';
	import { debounce } from 'lodash-es';
	import { uploadFile, cancelUpload, type UploadProgress } from '$lib/utils/chunked-upload';
	import { formatBytes } from '$lib/utils';

	let searchParams = $state<SearchModpacksRequest>(
		create(SearchModpacksRequestSchema, {
			query: '',
			gameVersion: '',
			modLoader: '',
			indexer: '',
			page: 1,
			pageSize: 20
		})
	);

	let searchResults = $state<SearchModpacksResponse | null>(null);
	let searchError = $state('');
	let favoritesError = $state('');
	let favorites = $state<IndexedModpack[]>([]);
	let uploadedPacks = $state<IndexedModpack[]>([]);
	let loading = $state(false);
	let syncing = $state(false);
	// The library has one view control (Browse / Imported / Favorites); the two
	// booleans below are read-only projections of the selected tab.
	let activeView = $state('browse');
	let showFavorites = $derived(activeView === 'favorites');
	let showUploaded = $derived(activeView === 'imported');
	let showManifestInspector = $state(false);
	// Indexer health drives the "indexers offline" banner and the per-indexer
	// counts below; loaders populate the filter dropdown.
	let indexerStatus = $state<GetIndexerStatusResponse | null>(null);
	let fileInput = $state<HTMLInputElement | null>(null);
	let uploading = $state(false);
	let uploadProgress = $state<UploadProgress | null>(null);
	let uploadAbortController = $state<AbortController | null>(null);
	let selectedIndexer = $state('modrinth'); // Default Modrinth
	let indexerName = $derived(selectedIndexer === 'fuego' ? 'CurseForge' : 'Modrinth');

	let libraryTabs = $derived([
		{ id: 'browse', label: 'Browse' },
		{ id: 'imported', label: 'Imported', badge: uploadedPacks.length },
		{ id: 'favorites', label: 'Favorites', badge: favorites.length }
	]);

	const importTabs = [
		{ id: 'zip', label: 'Upload ZIP' },
		{ id: 'url', label: 'From URL' }
	];

	// Import modpack modal state
	let showImportModal = $state(false);
	let importMethod = $state('zip');
	let importingRemote = $state(false);
	let remoteUrl = $state('');
	let remoteName = $state('');
	let remoteDescription = $state('');
	let remoteMcVersion = $state('');
	let remoteModLoader = $state('');
	let remoteAuthToken = $state('');

	// Dynamic game versions from API
	let gameVersions = $state<string[]>([]);
	let modLoaders = $state<Array<{ value: string; label: string }>>([
		{ value: '', label: 'All Loaders' }
	]);

	onMount(async () => {
		await Promise.all([
			checkIndexerStatus(),
			loadFavorites(),
			loadUploadedPacks(),
			loadMinecraftVersions(),
			loadModLoaders(),
			searchModpacks()
		]);

		if (searchResults && searchResults.total === 0 && !searchParams.query) {
			syncModpacks();
		}
	});

	async function loadMinecraftVersions() {
		try {
			const response = await rpcClient.minecraft.getMinecraftVersions({});
			gameVersions = response.versions.map((v) => v.id);
		} catch (error) {
			console.error('Failed to load Minecraft versions:', error);
		}
	}

	async function loadModLoaders() {
		try {
			const response = await rpcClient.minecraft.getModLoaders({});
			const loaders = response.modloaders || [];
			modLoaders = [
				{ value: '', label: 'All Loaders' },
				...loaders.map((loader) => ({
					value: loader.name,
					label: loader.displayName || loader.name
				}))
			];
		} catch (error) {
			console.error('Failed to load mod loaders:', error);
		}
	}

	async function checkIndexerStatus() {
		try {
			const response = await rpcClient.modpack.getIndexerStatus({});
			indexerStatus = response;
		} catch (error) {
			console.error('Failed to check indexer status:', error);
		}
	}

	async function searchModpacks(resetPage = true) {
		loading = true;
		searchError = '';
		try {
			if (resetPage) {
				searchParams.page = 1;
			}
			const response = await rpcClient.modpack.searchModpacks({
				...searchParams,
				indexer: selectedIndexer
			});
			searchResults = response;
		} catch (error) {
			searchError = error instanceof Error ? error.message : 'Failed to search modpacks';
			toast.error('Failed to search modpacks');
			console.error(error);
		} finally {
			loading = false;
		}
	}

	async function _syncModpacks() {
		syncing = true;
		try {
			const result = await rpcClient.modpack.syncModpacks({
				query: searchParams.query || '',
				gameVersion: searchParams.gameVersion || '',
				modLoader: searchParams.modLoader || '',
				indexer: selectedIndexer
			});
			toast.success(`Synced ${result.syncedCount} modpacks from ${indexerName}`);
			await searchModpacks();
		} catch (error) {
			toast.error(error instanceof Error ? error.message : 'Failed to sync modpacks');
			console.error(error);
		} finally {
			syncing = false;
		}
	}

	const syncModpacks = debounce(_syncModpacks, 1000, { leading: true, trailing: false });

	async function toggleFavorite(modpack: IndexedModpack) {
		try {
			const result = await rpcClient.modpack.toggleFavorite({ id: modpack.id });

			if (searchResults) {
				searchResults.modpacks = searchResults.modpacks.map((m) =>
					m.id === modpack.id ? { ...m, isFavorited: result.isFavorited } : m
				);
			}

			uploadedPacks = uploadedPacks.map((m) =>
				m.id === modpack.id ? { ...m, isFavorited: result.isFavorited } : m
			);

			if (result.isFavorited) {
				if (!favorites.find((f) => f.id === modpack.id)) {
					favorites = [...favorites, { ...modpack, isFavorited: true }];
				}
				toast.success('Added to favorites');
			} else {
				favorites = favorites.filter((f) => f.id !== modpack.id);
				toast.success('Removed from favorites');
			}
		} catch (error) {
			toast.error('Failed to toggle favorite');
			console.error(error);
		}
	}

	async function loadFavorites() {
		favoritesError = '';
		try {
			const result = await rpcClient.modpack.listFavorites({});
			favorites = result.modpacks;
		} catch (error) {
			favoritesError = error instanceof Error ? error.message : 'Failed to load favorites';
			toast.error('Failed to load favorites');
			console.error(error);
		}
	}

	async function loadUploadedPacks() {
		try {
			const result = await rpcClient.modpack.searchModpacks({
				query: '',
				gameVersion: '',
				modLoader: '',
				indexer: 'manual',
				page: 1,
				pageSize: 100
			});
			uploadedPacks = result.modpacks || [];
		} catch (error) {
			console.error('Failed to load uploaded packs:', error);
		}
	}

	function formatNumber(num: number): string {
		if (num >= 1000000) {
			return `${(num / 1000000).toFixed(1)}M`;
		} else if (num >= 1000) {
			return `${(num / 1000).toFixed(1)}K`;
		}
		return num.toString();
	}

	function parseJsonArray(jsonStr: string): string[] {
		try {
			return JSON.parse(jsonStr);
		} catch {
			return [];
		}
	}

	async function handleModpackUpload(event: Event) {
		const input = event.target as HTMLInputElement;
		const files = input.files;
		if (!files || files.length === 0) return;

		const file = files[0];
		if (!file.name.endsWith('.zip')) {
			toast.error('Please select a valid modpack ZIP file');
			return;
		}

		uploading = true;
		uploadAbortController = new AbortController();
		uploadProgress = null;
		showImportModal = false;

		try {
			const uploadResult = await uploadFile(file, {
				onProgress: (progress) => {
					uploadProgress = progress;
				},
				signal: uploadAbortController.signal
			});
			if (!uploadResult.sessionId) {
				throw new Error('Upload completed but no session ID returned');
			}

			const result = await rpcClient.modpack.importUploadedModpack({
				uploadSessionId: uploadResult.sessionId,
				name: file.name.replace('.zip', ''),
				description: ''
			});

			toast.success(`Modpack "${result.modpack?.name}" imported successfully`);
			await Promise.all([searchModpacks(), loadUploadedPacks()]);
		} catch (error: unknown) {
			if (error instanceof Error && error.message === 'Upload cancelled') {
				toast.info('Import cancelled');
			}
		} finally {
			uploading = false;
			uploadProgress = null;
			uploadAbortController = null;
			input.value = '';
		}
	}

	function cancelCurrentUpload() {
		if (uploadAbortController) {
			uploadAbortController.abort();
		}
		if (uploadProgress?.sessionId) {
			cancelUpload(uploadProgress.sessionId).catch(() => {});
		}
	}

	function chooseZipFile() {
		fileInput?.click();
	}

	async function handleRemoteImport() {
		const trimmedUrl = remoteUrl.trim();
		if (!trimmedUrl) {
			toast.error('Please enter a valid modpack URL');
			return;
		}

		importingRemote = true;
		try {
			const res = await rpcClient.modpack.importRemoteModpack({
				url: trimmedUrl,
				name: remoteName.trim(),
				description: remoteDescription.trim(),
				mcVersion: remoteMcVersion.trim(),
				modLoader: remoteModLoader.trim(),
				authToken: remoteAuthToken.trim()
			});

			toast.success(res.message || 'Modpack imported successfully');
			showImportModal = false;
			remoteUrl = '';
			remoteName = '';
			remoteDescription = '';
			remoteMcVersion = '';
			remoteModLoader = '';
			remoteAuthToken = '';

			await Promise.all([searchModpacks(), loadUploadedPacks()]);
		} catch (err: unknown) {
			toast.error(err instanceof Error ? err.message : 'Failed to import modpack');
			console.error(err);
		} finally {
			importingRemote = false;
		}
	}

	async function handleUseInServer(modpack: IndexedModpack) {
		try {
			const res = await rpcClient.node.listNodes({});
			const active = (res.nodes || []).filter((n) => n.enabled && n.status === NodeStatus.ONLINE);
			if (active.length === 0) {
				toast.error(
					'No active Docker nodes available — add and enable a node in Settings → Docker Nodes before creating a server.'
				);
				return;
			}
		} catch (error) {
			toast.error('Could not verify available nodes. Please try again.');
			console.error(error);
			return;
		}
		goto(resolve(`/servers/new?modpack=${modpack.id}`));
	}

	let pendingDelete = $state<IndexedModpack | null>(null);
	let confirmOpen = $state(false);

	async function deleteModpack(modpack: IndexedModpack) {
		pendingDelete = modpack;
		confirmOpen = true;
	}

	async function confirmDeleteModpack() {
		const modpack = pendingDelete;
		if (!modpack) return;
		try {
			await rpcClient.modpack.deleteModpack({ id: modpack.id });
			toast.success(`Modpack "${modpack.name}" deleted successfully`);

			uploadedPacks = uploadedPacks.filter((m) => m.id !== modpack.id);
			favorites = favorites.filter((m) => m.id !== modpack.id);

			if (!showFavorites && !showUploaded) {
				await searchModpacks();
			}
		} catch (error) {
			toast.error(error instanceof Error ? error.message : 'Failed to delete modpack');
		} finally {
			pendingDelete = null;
		}
	}

	let displayModpacks = $derived(
		showFavorites
			? favorites
			: showUploaded
				? uploadedPacks
				: (() => {
						const results = searchResults?.modpacks || [];
						const uploaded: IndexedModpack[] = [];
						const indexed: IndexedModpack[] = [];
						results.forEach((m) => (m.indexer === 'manual' ? uploaded : indexed).push(m));
						return [...uploaded, ...indexed];
					})()
	);
</script>

<svelte:head>
	<title>Modpacks - Carbon Panel</title>
</svelte:head>

<div class="h-full flex-1 space-y-6 rounded-none font-sans text-[#f4f4f4]">
	{#if searchError && !showFavorites && !showUploaded}
		<CarbonErrorState
			title="Failed to search modpacks"
			message={searchError}
			onretry={() => searchModpacks()}
		/>
	{:else if favoritesError && showFavorites}
		<CarbonErrorState
			title="Failed to load favorites"
			message={favoritesError}
			onretry={loadFavorites}
		/>
	{/if}
	<!-- Top Bar -->
	<CarbonPageHeader title="Modpacks" description="Browse, import and manage modpack packages">
		{#snippet actions()}
			<CarbonButton kind="primary" size="md" onclick={() => (showImportModal = true)}>
				<Upload class="mr-1.5 h-4 w-4" />
				Import Modpack
			</CarbonButton>

			<CarbonButton
				kind="secondary"
				size="md"
				class="whitespace-nowrap"
				onclick={() => goto(resolve('/modpacks/studio'))}
			>
				<Boxes class="mr-1.5 h-4 w-4" />
				Modpack Studio
			</CarbonButton>

			<CarbonButton
				kind="tertiary"
				size="md"
				class="whitespace-nowrap"
				onclick={() => (showManifestInspector = true)}
			>
				<FileSearch class="mr-1.5 h-4 w-4" />
				Inspect Manifest
			</CarbonButton>
		{/snippet}
	</CarbonPageHeader>

	<!-- Library view control: Browse / Imported / Favorites -->
	<CarbonTabs tabs={libraryTabs} bind:selectedTab={activeView} />

	{#if selectedIndexer === 'fuego'}
		<div
			class="flex items-center justify-between rounded-none border-y border-r border-l-4 border-[#393939] border-l-[#0f62fe] bg-[#262626] p-3.5 text-xs text-[#c6c6c6]"
		>
			<div class="flex items-center gap-2">
				<KeyRound class="h-4 w-4 shrink-0 text-[#78a9ff]" />
				<span
					>CurseForge Keyless Mode: syncing and browsing modpacks operates through community
					proxies.</span
				>
			</div>
			<CarbonButton
				kind="ghost"
				size="sm"
				class="h-7 text-xs"
				href={resolve('/settings?tab=api-keys')}
			>
				<Settings class="mr-1 h-3.5 w-3.5" />
				Custom API Key
			</CarbonButton>
		</div>
	{/if}

	{#if !showFavorites && !showUploaded}
		<!-- Search & Filter Controls -->
		<div class="space-y-4">
			<div class="grid grid-cols-1 gap-2 md:grid-cols-12">
				<div class="md:col-span-4">
					<CarbonSearch
						placeholder="Search modpacks by name or description..."
						bind:value={searchParams.query}
						onkeydown={(e) => {
							if (e.key === 'Enter') searchModpacks();
							else if (e.key === 'Escape' && searchParams.query) {
								searchParams.query = '';
								searchModpacks();
							}
						}}
						size="md"
					/>
				</div>

				<div class="md:col-span-2">
					<CarbonSelect bind:value={searchParams.gameVersion} disabled={loading}>
						<option value="">All MC Versions</option>
						{#each gameVersions as version (version)}
							<option value={version}>{version}</option>
						{/each}
					</CarbonSelect>
				</div>

				<div class="md:col-span-2">
					<CarbonSelect bind:value={searchParams.modLoader} disabled={loading}>
						{#each modLoaders as loader (loader.value)}
							<option value={loader.value}>{loader.label}</option>
						{/each}
					</CarbonSelect>
				</div>

				<div class="md:col-span-2">
					<CarbonSelect
						bind:value={selectedIndexer}
						disabled={syncing}
						onchange={() => syncModpacks()}
					>
						<option value="modrinth">Modrinth</option>
						<option value="fuego">CurseForge</option>
					</CarbonSelect>
				</div>

				<div class="flex items-center gap-1 md:col-span-2">
					<CarbonButton
						kind="primary"
						class="w-full justify-center"
						onclick={() => searchModpacks(true)}
						disabled={loading}
					>
						Search
					</CarbonButton>
				</div>
			</div>

			<!-- Secondary Action Row -->
			<div class="flex flex-wrap items-center justify-between gap-2 pt-1">
				<div class="flex items-center gap-2">
					<CarbonButton kind="tertiary" size="sm" onclick={syncModpacks} disabled={syncing}>
						<RefreshCw class={`mr-1.5 h-3.5 w-3.5 ${syncing ? 'animate-spin' : ''}`} />
						Sync {indexerName}
					</CarbonButton>
				</div>

				<div class="flex items-center gap-3 font-mono text-xs text-[#8d8d8d]">
					{#if searchResults}
						<span>TOTAL: {searchResults.total} PACKS</span>
					{/if}
					{#if indexerStatus}
						{#each Object.entries(indexerStatus.modpacksByIndexer ?? {}) as [name, count] (`${name}:${count}`)}
							<span>{name.toUpperCase()}: {count}</span>
						{/each}
					{/if}
				</div>
			</div>
		</div>
	{/if}

	{#if uploading && uploadProgress}
		<div class="space-y-2 border border-[#393939] bg-[#262626] p-4">
			<div class="flex items-center justify-between text-xs">
				<span class="font-medium text-white">Importing modpack archive...</span>
				<div class="flex items-center gap-3 font-mono text-[#a8a8a8]">
					<span>{uploadProgress.percentComplete.toFixed(0)}%</span>
					<span
						>{formatBytes(uploadProgress.bytesUploaded)} / {formatBytes(
							uploadProgress.totalBytes
						)}</span
					>
					<button
						type="button"
						onclick={cancelCurrentUpload}
						class="cursor-pointer text-[#da1e28] hover:text-white"
						title="Cancel import"
						aria-label="Cancel import"
					>
						<X class="h-4 w-4" />
					</button>
				</div>
			</div>
			<div class="h-2 w-full overflow-hidden bg-[#161616]">
				<div
					class="h-full bg-[#0f62fe] transition-all"
					style="width: {uploadProgress.percentComplete}%"
				></div>
			</div>
		</div>
	{/if}

	<!-- Modpack Packages Grid with CarbonTiles -->
	<div class="motion-stagger grid gap-4 md:grid-cols-2 lg:grid-cols-3">
		{#each displayModpacks as modpack (modpack.id)}
			<CarbonTile
				class="group flex flex-col justify-between rounded-none border-[#393939] p-5 transition-colors hover:border-[#525252]"
			>
				<div class="space-y-3">
					<!-- Tile Header -->
					<div class="flex items-start gap-3">
						{#if modpack.logoUrl}
							<img
								src={modpack.logoUrl}
								alt={modpack.name}
								class="h-14 w-14 shrink-0 rounded-none border border-[#393939] bg-[#161616] object-cover"
							/>
						{:else}
							<div
								class="flex h-14 w-14 shrink-0 items-center justify-center rounded-none border border-[#393939] bg-[#161616] text-[#525252]"
							>
								<Package class="h-7 w-7" />
							</div>
						{/if}

						<div class="min-w-0 flex-1">
							<h3 class="truncate text-base font-semibold text-white" title={modpack.name}>
								{modpack.name}
							</h3>
							<div class="mt-1 flex flex-wrap items-center gap-2">
								<CarbonTag type={modpack.indexer === 'manual' ? 'purple' : 'blue'} size="sm">
									{modpack.indexer === 'manual' ? 'Imported' : modpack.indexer}
								</CarbonTag>
								<span class="flex items-center gap-1 font-mono text-xs text-[#8d8d8d]">
									<Download class="inline h-3 w-3" />
									{formatNumber(modpack.downloadCount)}
								</span>
							</div>
						</div>

						<button
							type="button"
							onclick={() => toggleFavorite(modpack)}
							class="cursor-pointer rounded-none p-2 text-[#8d8d8d] transition-colors hover:text-[#da1e28]"
							title={modpack.isFavorited ? 'Remove favorite' : 'Add favorite'}
							aria-label={modpack.isFavorited
								? `Remove ${modpack.name} from favorites`
								: `Add ${modpack.name} to favorites`}
						>
							<Heart
								class={`h-4 w-4 ${modpack.isFavorited ? 'fill-[#da1e28] text-[#da1e28]' : ''}`}
							/>
						</button>
					</div>

					<!-- Description -->
					<p class="line-clamp-2 min-h-8 text-xs text-[#a8a8a8]">
						{modpack.summary || 'No summary available for this modpack.'}
					</p>

					<!-- Loaders & Versions -->
					<div class="space-y-1.5 pt-1">
						{#if parseJsonArray(modpack.modLoaders).length > 0}
							<div class="flex flex-wrap gap-1">
								{#each parseJsonArray(modpack.modLoaders) as loader (loader)}
									<CarbonTag type="cyan" size="sm">{loader}</CarbonTag>
								{/each}
							</div>
						{/if}

						{#if parseJsonArray(modpack.gameVersions).length > 0}
							<div class="truncate font-mono text-xs text-[#8d8d8d]">
								MC: {parseJsonArray(modpack.gameVersions).slice(0, 3).join(', ')}
								{#if parseJsonArray(modpack.gameVersions).length > 3}
									+{parseJsonArray(modpack.gameVersions).length - 3} more
								{/if}
							</div>
						{/if}
					</div>
				</div>

				<!-- Tile Actions -->
				<div class="mt-4 flex items-center justify-between gap-2 border-t border-[#393939] pt-3">
					<div class="flex items-center gap-1">
						{#if modpack.websiteUrl}
							<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- external modpack homepage URL used directly; resolve() throws on non-absolute pathnames -->
							<a href={modpack.websiteUrl} target="_blank" rel="noopener noreferrer">
								<!-- eslint-enable svelte/no-navigation-without-resolve -->
								<CarbonButton kind="ghost" size="sm">
									<ExternalLink class="mr-1 h-3 w-3" />
									View
								</CarbonButton>
							</a>
						{/if}
						{#if modpack.indexer === 'manual'}
							<CarbonButton kind="danger" size="sm" onclick={() => deleteModpack(modpack)}>
								<Trash2 class="mr-1 h-3 w-3" />
								Delete
							</CarbonButton>
						{/if}
					</div>

					<CarbonButton kind="primary" size="sm" onclick={() => handleUseInServer(modpack)}>
						Use in Server
					</CarbonButton>
				</div>
			</CarbonTile>
		{/each}
	</div>

	<!-- Empty state -->
	{#if displayModpacks.length === 0}
		<div class="rounded-none border border-dashed border-[#393939] bg-[#262626] py-16 text-center">
			<Package class="mx-auto mb-3 h-10 w-10 text-[#525252]" />
			<p class="text-sm font-semibold text-[#f4f4f4]">
				{#if showFavorites}
					No favorite modpacks found
				{:else if loading}
					Loading modpack catalog...
				{:else if syncing}
					Syncing modpacks from indexers...
				{:else if searchParams.query}
					No modpacks matching "{searchParams.query}"
				{:else}
					No modpacks available locally
				{/if}
			</p>
			<p class="mx-auto mt-1 max-w-sm text-xs text-[#8d8d8d]">
				{#if showFavorites}
					Click the heart icon on any package to add it to your pinned favorites.
				{:else if !loading && !syncing}
					Click "Sync" to index popular modpacks, or use "Import Modpack" to add a ZIP archive or a
					direct URL.
				{/if}
			</p>
			{#if !showFavorites && !loading && !syncing && (searchParams.query || searchParams.gameVersion || searchParams.modLoader)}
				<CarbonButton
					kind="secondary"
					size="sm"
					class="mt-4 justify-center"
					onclick={() => {
						searchParams.query = '';
						searchParams.gameVersion = '';
						searchParams.modLoader = '';
						searchModpacks();
					}}
				>
					Clear search & filters
				</CarbonButton>
			{/if}
		</div>
	{/if}

	<!-- Pagination -->
	{#if !showFavorites && !showUploaded && searchResults && searchResults.total > searchResults.pageSize}
		<div class="flex items-center justify-center gap-3 border-t border-[#393939] pt-4">
			<CarbonButton
				kind="tertiary"
				size="sm"
				disabled={(searchParams.page || 1) === 1}
				onclick={() => {
					searchParams.page = Math.max(1, (searchParams.page || 1) - 1);
					searchModpacks(false);
				}}
			>
				Previous
			</CarbonButton>
			<span class="font-mono text-xs text-[#a8a8a8]">
				Page {searchParams.page} of {Math.ceil(searchResults.total / searchResults.pageSize)}
			</span>
			<CarbonButton
				kind="tertiary"
				size="sm"
				disabled={(searchParams.page || 1) >=
					Math.ceil(searchResults.total / searchResults.pageSize)}
				onclick={() => {
					searchParams.page = (searchParams.page || 1) + 1;
					searchModpacks(false);
				}}
			>
				Next
			</CarbonButton>
		</div>
	{/if}

	<!-- Import Modpack: one entry point, two methods -->
	<CarbonModal
		bind:open={showImportModal}
		title="Import Modpack"
		description="Add a modpack package to your library from a ZIP archive or a direct URL"
		hasFooter={false}
		size="3xl"
	>
		<div class="space-y-5">
			<CarbonTabs tabs={importTabs} bind:selectedTab={importMethod} idPrefix="import-method" />

			{#if importMethod === 'zip'}
				<section
					id="import-method-panel-zip"
					role="tabpanel"
					aria-labelledby="import-method-tab-zip"
					class="space-y-3"
				>
					<div>
						<h4 class="font-mono text-xs font-semibold tracking-[0.32px] text-[#c6c6c6] uppercase">
							Upload ZIP
						</h4>
						<p class="mt-1 text-xs text-[#a8a8a8]">
							Import a modpack archive (.zip) from this computer. The pack becomes an Imported entry
							in your library and can be deployed to a server.
						</p>
					</div>
					<CarbonButton kind="primary" size="md" onclick={chooseZipFile} disabled={uploading}>
						<Upload class="mr-2 h-4 w-4" />
						{uploading ? 'Importing...' : 'Choose ZIP File'}
					</CarbonButton>
				</section>
			{:else}
				<section
					id="import-method-panel-url"
					role="tabpanel"
					aria-labelledby="import-method-tab-url"
					class="space-y-4"
				>
					<div>
						<h4
							class="flex items-center gap-1.5 font-mono text-xs font-semibold tracking-[0.32px] text-[#c6c6c6] uppercase"
						>
							<Globe class="h-3.5 w-3.5 text-accent-text" />
							From URL
						</h4>
						<p class="mt-1 text-xs text-[#a8a8a8]">
							Import an archive hosted on GitHub Releases, Cloudflare R2 or a static CDN.
						</p>
					</div>

					<CarbonTextInput
						label="Modpack Archive URL *"
						placeholder="https://github.com/owner/repo/releases/download/v1.0/modpack.zip"
						bind:value={remoteUrl}
						disabled={importingRemote}
						helperText="Direct link to .zip or .mrpack archive file"
					/>

					<div class="grid grid-cols-1 gap-4 md:grid-cols-2">
						<CarbonTextInput
							label="Pack Name (Optional)"
							placeholder="Auto-detected if blank"
							bind:value={remoteName}
							disabled={importingRemote}
						/>
						<CarbonTextInput
							label="Minecraft Version"
							placeholder="e.g. 1.20.1 (or auto-detect)"
							bind:value={remoteMcVersion}
							disabled={importingRemote}
						/>
					</div>

					<div class="grid grid-cols-1 gap-4 md:grid-cols-2">
						<CarbonSelect
							label="Mod Loader"
							bind:value={remoteModLoader}
							disabled={importingRemote}
						>
							<option value="">Auto-detect from manifest</option>
							<option value="fabric">Fabric</option>
							<option value="forge">Forge</option>
							<option value="neoforge">NeoForge</option>
							<option value="quilt">Quilt</option>
							<option value="custom">Custom</option>
						</CarbonSelect>

						<CarbonTextInput
							type="password"
							label="Auth Token (Optional)"
							placeholder="Bearer token or GitHub PAT"
							bind:value={remoteAuthToken}
							disabled={importingRemote}
						/>
					</div>

					<CarbonTextInput
						label="Description (Optional)"
						placeholder="Brief description or release summary"
						bind:value={remoteDescription}
						disabled={importingRemote}
					/>

					<div class="flex items-center justify-end gap-3 border-t border-[#393939] pt-4">
						<CarbonButton
							kind="secondary"
							size="md"
							onclick={() => (showImportModal = false)}
							disabled={importingRemote}
						>
							Cancel
						</CarbonButton>
						<CarbonButton
							kind="primary"
							size="md"
							onclick={handleRemoteImport}
							disabled={importingRemote || !remoteUrl.trim()}
						>
							{#if importingRemote}
								<Loader2 class="mr-2 h-4 w-4 animate-spin" />
								Downloading & Importing...
							{:else}
								<Download class="mr-2 h-4 w-4" />
								Import Modpack
							{/if}
						</CarbonButton>
					</div>
				</section>
			{/if}
		</div>
	</CarbonModal>

	<input
		bind:this={fileInput}
		type="file"
		accept=".zip"
		onchange={handleModpackUpload}
		class="hidden"
	/>

	<ManifestInspectorDialog
		bind:open={showManifestInspector}
		onOpenChange={(v) => (showManifestInspector = v)}
	/>
	<CarbonConfirm
		bind:open={confirmOpen}
		title="Delete modpack?"
		message="This action cannot be undone."
		confirmLabel="Delete Modpack"
		danger
		onconfirm={confirmDeleteModpack}
		onclose={() => (pendingDelete = null)}
	>
		{#snippet details()}
			{#if pendingDelete}{pendingDelete.name}{/if}
		{/snippet}
	</CarbonConfirm>
</div>
