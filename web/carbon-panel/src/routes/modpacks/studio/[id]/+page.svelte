<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import StudioModTable from '$lib/components/fragments/StudioModTable.svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import {
		CarbonButton,
		CarbonTile,
		CarbonTag,
		CarbonTabs,
		CarbonModal,
		CarbonTextInput,
		CarbonSelect,
		CarbonSearch,
		CarbonStructuredList,
		CarbonInlineLoading,
		CarbonConfirm,
		CarbonEmptyState
	} from '$lib/components/carbon';
	import CarbonInactiveTabPanels from '$lib/components/carbon/CarbonInactiveTabPanels.svelte';
	import {
		ArrowLeft,
		Save,
		Download,
		Rocket,
		Plus,
		Trash2,
		Package,
		Loader2,
		CheckCircle2,
		AlertTriangle,
		RefreshCw,
		ArrowRightLeft,
		Copy,
		Sparkles,
		Upload
	} from '@lucide/svelte';
	import { toast } from 'svelte-sonner';
	import { CarbonErrorState } from '$lib/components/carbon';
	import { apiFetch } from '$lib/api/fetch';
	import { installRequiredDependencies } from '$lib/features/modpacks/studio/dependencies';
	import {
		migrationErrorMessage,
		runMigration,
		type MigrationReport
	} from '$lib/features/modpacks/studio/migration';
	import { fetchLoaderVersions as resolveLoaderVersions } from '$lib/features/modpacks/studio/loader-versions';
	import {
		deletePackFile,
		fetchPackFiles,
		fetchPackToml,
		installDatapack,
		savePackFile,
		type PackFileInfo
	} from '$lib/features/modpacks/studio/pack-resources';
	import {
		fetchPackUpdates,
		refreshPack,
		type ModUpdateInfo
	} from '$lib/features/modpacks/studio/pack-maintenance';
	import {
		addModToPack as postMod,
		addUrlMod as postUrlMod,
		batchModAction,
		findModVersions,
		modFromSearch,
		nextModSide,
		patchMod,
		removeModFromPack,
		searchOnlineMods,
		type ModItem,
		type SearchModResult
	} from '$lib/features/modpacks/studio/mod-actions';
	import {
		allPackContentMods,
		fetchStudioPack,
		isMcVersionChanged,
		saveStudioPack,
		summarizePackContent,
		type StudioPack
	} from '$lib/features/modpacks/studio/pack-metadata';
	import { uploadFile } from '$lib/utils/chunked-upload';
	import { createRequestSequence } from '$lib/utils/request-sequence';
	import ModsCatalogSearchBar from '$lib/mods/ModsCatalogSearchBar.svelte';
	import ModsCatalogResultCard from '$lib/mods/ModsCatalogResultCard.svelte';
	import ModpackDeployDialog from '$lib/components/modpack-deploy-dialog.svelte';
	import ManifestInspectorDialog from '$lib/components/manifest-inspector-dialog.svelte';
	import DatapackBrowserDialog from '$lib/components/datapack-browser-dialog.svelte';
	import PackContentPanel from '$lib/components/pack-content-panel.svelte';
	import ExportDialog from '../export-dialog.svelte';
	import { formatBytes } from '$lib/utils';

	type Pack = StudioPack;

	let packId = $derived(page.params.id);

	let pack = $state<Pack | null>(null);
	let loading = $state(true);
	let saving = $state(false);
	// MC version the pack had when last loaded/saved — used to detect an MC
	// version change that marks installed mods stale (ticket: stale + resolve).
	let loadedMCVersion = $state('');
	let availableLoaderVersions = $state<string[]>(['latest']);
	let loadingLoaderVersions = $state(false);
	let exportDialogOpen = $state(false);
	let inspectorOpen = $state(false);

	// Primary View Tab: "mods", "datapacks", "resourcepacks", "shaderpacks",
	// "overrides", "migrate", "export", "maintenance"
	let activeTab = $state<
		| 'mods'
		| 'datapacks'
		| 'resourcepacks'
		| 'shaderpacks'
		| 'overrides'
		| 'migrate'
		| 'export'
		| 'maintenance'
	>('mods');

	// Overrides / Files State (declared before studioTabs which references packFiles)
	let packFiles = $state<PackFileInfo[]>([]);

	const studioTabs = $derived([
		{ id: 'mods', label: 'Mods', badge: pack?.mods?.length ?? 0 },
		{ id: 'datapacks', label: 'Datapacks', badge: pack?.datapacks?.length ?? 0 },
		{ id: 'resourcepacks', label: 'Resource Packs', badge: pack?.resourcepacks?.length ?? 0 },
		{ id: 'shaderpacks', label: 'Shaders', badge: pack?.shaderpacks?.length ?? 0 },
		{ id: 'overrides', label: 'Overrides', badge: packFiles.length },
		{ id: 'migrate', label: 'Migration' },
		{ id: 'export', label: 'Export' },
		{ id: 'maintenance', label: 'Maintenance' }
	]);

	// Mod Filter & Selection
	let modFilter = $state('');
	let sideFilter = $state<'all' | 'both' | 'client' | 'server' | 'pinned' | 'updates'>('all');
	let selectedSlugs = $state<string[]>([]);

	// Update Engine State
	let checkingUpdates = $state(false);
	let updatesMap = $state<Record<string, ModUpdateInfo>>({});

	// Overrides / Files State
	let loadingFiles = $state(false);
	let uploadFileDialogOpen = $state(false);
	let uploadFilePath = $state('config/settings.json');
	let uploadFileContent = $state('');
	let savingFile = $state(false);
	let datapackBrowseOpen = $state(false);
	let datapackFileInput = $state<HTMLInputElement | null>(null);
	let uploadingDatapack = $state(false);

	async function handleDatapackUpload(event: Event) {
		const input = event.target as HTMLInputElement;
		const file = input.files?.[0];
		input.value = '';
		if (!file) return;
		if (!file.name.toLowerCase().endsWith('.zip')) {
			toast.error('Datapacks must be .zip archives containing pack.mcmeta');
			return;
		}
		uploadingDatapack = true;
		try {
			const { sessionId } = await uploadFile(file);
			const res = await apiFetch(`/api/v1/packwiz/packs/${packId}/datapacks/upload`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ upload_session_id: sessionId, file_name: file.name })
			});
			if (!res.ok) {
				throw new Error((await res.text()) || `HTTP ${res.status}`);
			}
			toast.success(`Uploaded datapack ${file.name}`);
			await loadFiles();
		} catch (e) {
			toast.error(e instanceof Error ? e.message : 'Upload failed');
		} finally {
			uploadingDatapack = false;
		}
	}

	async function installStudioDatapack(datapackId: string, gameVersion: string, indexer: string) {
		await installDatapack(packId!, datapackId, gameVersion, indexer, apiFetch);
		await loadFiles();
	}

	// Migration State
	let migrateMC = $state('1.21.1');
	let migrateLoader = $state('fabric');
	let migrating = $state(false);
	let migrationReport = $state<MigrationReport | null>(null);

	// Search / Add Mods Modal State
	let searchDrawerOpen = $state(false);
	let addTab = $state<'modrinth' | 'curseforge' | 'url'>('modrinth');
	let searchQuery = $state('');
	let searching = $state(false);
	let searchResults = $state<SearchModResult[]>([]);
	let searchError = $state('');
	// E15: a slow earlier response must never overwrite newer results when the
	// source tab switches or Enter is pressed rapidly.
	const searchSequence = createRequestSequence();
	// E16: per-row pending state so double-clicks cannot double-POST a mod.
	let addingSlugs = $state<Record<string, 'both' | 'client' | 'server'>>({});
	// E17: batch removal is destructive and needs an explicit confirmation.
	let batchRemoveConfirmOpen = $state(false);
	let batchRemoving = $state(false);

	const addModSourceTabs = [
		{ id: 'modrinth', label: 'Modrinth' },
		{ id: 'curseforge', label: 'CurseForge' },
		{ id: 'url', label: 'Direct Download URL' }
	];

	// Direct URL Mod Input State
	let urlModName = $state('');
	let urlModFileName = $state('');
	let urlModDownloadUrl = $state('');
	let urlModSide = $state<'both' | 'client' | 'server'>('both');
	let urlModPinned = $state(false);
	let addingUrlMod = $state(false);

	// Maintenance State
	let refreshing = $state(false);
	let rawPackToml = $state('');
	let loadingRaw = $state(false);

	// Deploy Dialog State
	let deployDialogOpen = $state(false);

	const MC_VERSIONS = [
		'1.21.4',
		'1.21.1',
		'1.20.6',
		'1.20.4',
		'1.20.1',
		'1.19.4',
		'1.19.2',
		'1.18.2',
		'1.16.5',
		'1.12.2'
	];

	async function fetchLoaderVersions(loader: string, mcVer: string) {
		if (!loader) return;
		loadingLoaderVersions = true;
		try {
			const versions = await resolveLoaderVersions(loader, mcVer, pack?.loader_version, apiFetch);
			if (versions !== null) availableLoaderVersions = versions;
		} finally {
			loadingLoaderVersions = false;
		}
	}

	$effect(() => {
		if (pack?.mod_loader) {
			fetchLoaderVersions(pack.mod_loader, pack.mc_version);
		}
	});

	let loadError = $state('');

	async function loadPack() {
		if (!packId) return;
		loadError = '';
		loading = true;
		try {
			const data = await fetchStudioPack(packId, apiFetch);
			pack = data;
			loadedMCVersion = data?.mc_version || '';
		} catch (err) {
			console.error('Failed to load pack:', err);
			loadError = err instanceof Error ? err.message : 'Failed to load modpack project';
			toast.error('Failed to load modpack project');
		} finally {
			loading = false;
		}
	}

	async function saveMetadata() {
		if (!pack) return;
		const mcChanged = isMcVersionChanged(loadedMCVersion, pack.mc_version);
		saving = true;
		try {
			await saveStudioPack(packId!, pack, apiFetch);
			if (mcChanged) {
				// MC version change marks installed mods stale: jump to the
				// migration tab and simulate so the user can update & resolve
				// conflicts in one flow.
				loadedMCVersion = pack.mc_version;
				migrateMC = pack.mc_version;
				activeTab = 'migrate';
				toast.warning(
					`MC version changed to ${pack.mc_version} — installed mods are now stale. Review compatibility below.`
				);
				await simulateMigration(false);
			} else {
				toast.success('Modpack settings saved');
			}
		} catch (err) {
			console.error('Failed to save pack:', err);
			toast.error('Failed to save modpack settings');
		} finally {
			saving = false;
		}
	}

	async function toggleSide(mod: ModItem) {
		if (!pack) return;
		const newSide = nextModSide(mod.side);
		try {
			await patchMod(packId!, mod.slug, newSide, mod.pinned ?? false, apiFetch);
			mod.side = newSide;
			toast.success(`Updated ${mod.name} side to ${newSide.toUpperCase()}`);
		} catch (err) {
			console.error('Failed to update mod side:', err);
			toast.error('Failed to update mod side');
		}
	}

	async function togglePin(mod: ModItem) {
		if (!pack) return;
		const newPinned = !mod.pinned;
		try {
			await patchMod(packId!, mod.slug, mod.side, newPinned, apiFetch);
			mod.pinned = newPinned;
			toast.success(newPinned ? `Pinned ${mod.name}` : `Unpinned ${mod.name}`);
		} catch (err) {
			console.error('Failed to update mod pin:', err);
			toast.error('Failed to update mod');
		}
	}

	async function removeMod(mod: ModItem) {
		if (!pack) return;
		try {
			await removeModFromPack(packId!, mod.slug, apiFetch);
			pack.mods = (pack.mods || []).filter((m) => m.slug !== mod.slug);
			selectedSlugs = selectedSlugs.filter((s) => s !== mod.slug);
			toast.success(`Removed ${mod.name} from modpack`);
		} catch (err) {
			console.error('Failed to delete mod:', err);
			toast.error('Failed to remove mod');
		}
	}

	async function runBatchAction(action: 'set_side' | 'pin' | 'unpin' | 'remove', side?: string) {
		if (!pack || selectedSlugs.length === 0) return;
		try {
			await batchModAction(packId!, action, selectedSlugs, side, apiFetch);
			toast.success(`Batch action applied to ${selectedSlugs.length} mods`);
			selectedSlugs = [];
			await loadPack();
		} catch (err) {
			console.error('Batch action failed:', err);
			toast.error((err instanceof Error ? err.message : undefined) || 'Batch action failed');
		}
	}

	async function checkUpdates() {
		if (!pack) return;
		checkingUpdates = true;
		try {
			const { updates, availableCount } = await fetchPackUpdates(packId!, apiFetch);
			updatesMap = updates;
			toast.success(`Checked updates: ${availableCount} newer versions available`);
		} catch (err) {
			console.error('Check updates failed:', err);
			toast.error('Failed to check mod updates');
		} finally {
			checkingUpdates = false;
		}
	}

	async function loadFiles() {
		if (!packId) return;
		loadingFiles = true;
		try {
			packFiles = await fetchPackFiles(packId!, apiFetch);
		} catch (err) {
			console.error('Failed to load files:', err);
		} finally {
			loadingFiles = false;
		}
	}

	async function handleSaveFile() {
		if (!uploadFilePath.trim()) {
			toast.error('Please specify a valid file path');
			return;
		}
		savingFile = true;
		try {
			await savePackFile(packId!, uploadFilePath.trim(), uploadFileContent, apiFetch);
			toast.success(`Saved override file: ${uploadFilePath}`);
			uploadFileDialogOpen = false;
			await loadFiles();
		} catch (err) {
			console.error('Failed to save file:', err);
			toast.error('Failed to save override file');
		} finally {
			savingFile = false;
		}
	}

	let pendingDeleteFile: string | null = $state(null);
	let confirmDeleteOpen = $state(false);

	async function handleDeleteFile(filePath: string) {
		pendingDeleteFile = filePath;
		confirmDeleteOpen = true;
	}

	async function confirmDeleteFile() {
		const filePath = pendingDeleteFile;
		if (!filePath) return;
		try {
			await deletePackFile(packId!, filePath, apiFetch);
			toast.success(`Deleted file "${filePath}"`);
			await loadFiles();
		} catch (err) {
			console.error('Failed to delete file:', err);
			toast.error('Failed to delete override file');
		} finally {
			pendingDeleteFile = null;
		}
	}

	async function simulateMigration(apply: boolean = false) {
		if (!pack) return;
		migrating = true;
		try {
			const data = await runMigration(
				{ packId: packId!, targetMc: migrateMC, targetLoader: migrateLoader },
				apply,
				apiFetch
			);
			migrationReport = data;
			if (apply) {
				toast.success(`Migrated modpack to MC ${migrateMC} (${migrateLoader})`);
				await loadPack();
			} else {
				toast.success(`Simulation complete: ${data.compatible_count} compatible mods found`);
			}
		} catch (err) {
			console.error('Migration failed:', err);
			toast.error(migrationErrorMessage(err));
		} finally {
			migrating = false;
		}
	}

	async function handleRefreshPack() {
		if (!packId) return;
		refreshing = true;
		try {
			await refreshPack(packId!, apiFetch);
			toast.success('Packwiz re-indexed & refreshed successfully');
			await loadPack();
			await loadRawTomls();
		} catch (err) {
			console.error('Refresh failed:', err);
			toast.error('Failed to refresh pack index');
		} finally {
			refreshing = false;
		}
	}

	async function loadRawTomls() {
		if (!packId) return;
		loadingRaw = true;
		try {
			const text = await fetchPackToml(packId!, apiFetch);
			if (text !== null) rawPackToml = text;
		} finally {
			loadingRaw = false;
		}
	}

	async function searchMods() {
		if (!pack) return;
		searching = true;
		searchError = '';
		searchResults = [];
		const seq = searchSequence.next();

		try {
			const platform = addTab === 'curseforge' ? 'curseforge' : 'modrinth';
			const result = await searchOnlineMods(
				searchQuery,
				platform,
				pack.mod_loader,
				pack.mc_version,
				apiFetch
			);
			if (!searchSequence.isCurrent(seq)) return;
			if (result.error) {
				searchError = result.error;
			} else {
				searchResults = result.results;
			}
		} catch (err) {
			if (!searchSequence.isCurrent(seq)) return;
			console.error('Search failed:', err);
			searchError = 'Failed to fetch online mods';
		} finally {
			if (searchSequence.isCurrent(seq)) searching = false;
		}
	}

	async function addModToPack(item: SearchModResult, side: 'both' | 'client' | 'server' = 'both') {
		if (!pack || addingSlugs[item.id]) return;
		addingSlugs = { ...addingSlugs, [item.id]: side };
		try {
			const versions = await findModVersions(item, pack.mod_loader, pack.mc_version, apiFetch);
			if (versions.length === 0) {
				toast.error(
					`No compatible versions of ${item.title} for MC ${pack.mc_version} on ${pack.mod_loader}`
				);
				return;
			}

			const selectedVer = versions[0];
			const newMod = modFromSearch(item, selectedVer, side);
			await postMod(packId!, newMod, apiFetch);

			toast.success(`Added ${item.title} (${side.toUpperCase()}) to pack!`);

			// Resolve transitive required dependencies (best-effort, depth-bounded).
			try {
				const depCount = await installRequiredDependencies(
					{
						deps: selectedVer.dependencies || [],
						platform: item.platform,
						id: item.id,
						side,
						packId: packId || '',
						modLoader: pack.mod_loader,
						mcVersion: pack.mc_version,
						existingMods: pack.mods || []
					},
					apiFetch
				);
				if (depCount > 0) {
					toast.success(
						`Auto-installed ${depCount} required dependenc${depCount === 1 ? 'y' : 'ies'}`
					);
				}
			} catch (depErr) {
				console.warn('Dependency auto-resolve failed (non-fatal):', depErr);
			}
			await loadPack();
		} catch (err) {
			console.error('Failed to add mod:', err);
			toast.error((err instanceof Error ? err.message : undefined) || 'Failed to add mod to pack');
		} finally {
			const { [item.id]: _settled, ...rest } = addingSlugs;
			addingSlugs = rest;
		}
	}

	async function confirmBatchRemove() {
		batchRemoving = true;
		try {
			await runBatchAction('remove');
			batchRemoveConfirmOpen = false;
		} finally {
			batchRemoving = false;
		}
	}

	async function handleAddUrlMod() {
		if (!urlModDownloadUrl.trim()) {
			toast.error('Download URL is required');
			return;
		}
		addingUrlMod = true;
		try {
			const created = await postUrlMod(
				packId!,
				{
					name: urlModName,
					fileName: urlModFileName,
					downloadUrl: urlModDownloadUrl,
					side: urlModSide,
					pinned: urlModPinned
				},
				apiFetch
			);
			toast.success(`Added custom mod "${created.name}" from URL`);
			searchDrawerOpen = false;
			urlModName = '';
			urlModFileName = '';
			urlModDownloadUrl = '';
			await loadPack();
		} catch (err) {
			console.error('Failed to add mod from URL:', err);
			toast.error(
				(err instanceof Error ? err.message : undefined) || 'Failed to add direct URL mod'
			);
		} finally {
			addingUrlMod = false;
		}
	}

	let filteredMods = $derived(
		(pack?.mods || []).filter((m) => {
			const matchesText =
				(m.name || '').toLowerCase().includes(modFilter.toLowerCase()) ||
				(m.file_name || '').toLowerCase().includes(modFilter.toLowerCase());
			if (!matchesText) return false;

			if (sideFilter === 'both') return m.side === 'both';
			if (sideFilter === 'client') return m.side === 'client';
			if (sideFilter === 'server') return m.side === 'server';
			if (sideFilter === 'pinned') return !!m.pinned;
			if (sideFilter === 'updates') return !!updatesMap[m.slug]?.update_available;
			return true;
		})
	);

	let contentBreakdown = $derived(pack ? summarizePackContent(pack.mods, packFiles.length) : null);

	let prevLoadedId = $state<string | undefined>(undefined);
	$effect(() => {
		if (packId && packId !== prevLoadedId) {
			prevLoadedId = packId;
			loadPack();
			loadFiles();
			loadRawTomls();
		}
	});
</script>

<svelte:head>
	<title>{pack ? pack.name : 'Studio'} - Modpack Studio</title>
</svelte:head>

<div
	class="h-full flex-1 space-y-6 rounded-none font-sans text-foreground"
	data-testid="showcase-studio-workspace"
>
	{#if loadError}
		<CarbonErrorState
			title="Failed to load modpack project"
			message={loadError}
			onretry={loadPack}
		/>
	{/if}
	<!-- Top Header Bar -->
	<div
		class="flex flex-col justify-between gap-4 rounded-none border-b border-border pb-5 sm:flex-row sm:items-center"
	>
		<div class="flex items-center gap-4">
			<CarbonButton
				kind="ghost"
				size="md"
				iconOnly
				onclick={() => goto(resolve('/modpacks/studio'))}
				class="rounded-none text-muted-foreground hover:text-foreground"
				title="Back to Studio List"
				aria-label="Back to Studio List"
			>
				<ArrowLeft class="h-5 w-5" />
			</CarbonButton>

			{#if pack}
				<div class="min-w-0 space-y-0.5">
					<div class="flex items-center gap-3">
						<h1 class="truncate text-3xl font-semibold tracking-tight text-foreground">
							{pack.name}
						</h1>
						<CarbonTag type="blue" size="sm" class="uppercase">
							{pack.mod_loader}
						</CarbonTag>
						<CarbonTag type="gray" size="sm">
							MC {pack.mc_version}
						</CarbonTag>
					</div>
					<p class="text-xs text-muted-foreground">
						Packwiz Studio Project · v{pack.version} by {pack.author || 'Admin'}
					</p>
				</div>
			{/if}
		</div>

		<div class="flex flex-wrap items-center gap-2">
			{#if pack}
				<CarbonButton
					kind="tertiary"
					size="sm"
					class="rounded-none"
					onclick={() => (deployDialogOpen = true)}
				>
					<Rocket class="mr-1.5 h-3.5 w-3.5 text-accent-text" />
					Deploy to Server
				</CarbonButton>

				<CarbonButton
					kind="tertiary"
					size="sm"
					class="h-8 rounded-none text-xs"
					onclick={() => (exportDialogOpen = true)}
					title="Export .mrpack, CurseForge, instance.zip or Packwiz archive"
				>
					<Download class="mr-1.5 h-3.5 w-3.5" />
					Export
				</CarbonButton>

				<CarbonButton
					kind="primary"
					size="sm"
					class="rounded-none"
					onclick={saveMetadata}
					disabled={saving}
				>
					{#if saving}
						<Loader2 class="mr-2 h-4 w-4 animate-spin" />
						Saving...
					{:else}
						<Save class="mr-2 h-4 w-4" />
						Save
					{/if}
				</CarbonButton>
			{/if}
		</div>
	</div>

	{#if loading}
		<div class="flex items-center justify-center py-24">
			<CarbonInlineLoading description="Loading studio workspace..." />
		</div>
	{:else if pack}
		<!-- Studio Grid Layout -->
		<div class="grid grid-cols-1 gap-6 lg:grid-cols-4">
			<!-- Modpack Settings Sidebar Tile -->
			<CarbonTile class="h-fit space-y-4 rounded-none border-border lg:col-span-1">
				<div class="border-b border-border pb-3">
					<h2 class="text-base font-semibold text-foreground">Modpack Settings</h2>
					<p class="text-xs text-muted-foreground">Configure runtime parameters</p>
				</div>

				<div class="space-y-3">
					<CarbonTextInput label="Pack Name" bind:value={pack.name} />

					<CarbonTextInput label="Author" bind:value={pack.author} />

					<CarbonTextInput label="Pack Version" bind:value={pack.version} />

					<CarbonSelect label="Minecraft Version" bind:value={pack.mc_version}>
						{#each MC_VERSIONS as v (v)}
							<option value={v}>{v}</option>
						{/each}
					</CarbonSelect>

					<CarbonSelect label="Mod Loader" bind:value={pack.mod_loader}>
						<option value="fabric">Fabric</option>
						<option value="neoforge">NeoForge</option>
						<option value="forge">Forge</option>
						<option value="quilt">Quilt</option>
					</CarbonSelect>

					<CarbonSelect
						label="Loader Version"
						bind:value={pack.loader_version}
						helperText={loadingLoaderVersions ? 'Fetching versions...' : undefined}
					>
						{#each availableLoaderVersions as v (v)}
							<option value={v}>{v}</option>
						{/each}
					</CarbonSelect>

					<CarbonButton
						kind="primary"
						size="md"
						class="mt-2 w-full justify-center rounded-none"
						onclick={saveMetadata}
						disabled={saving}
					>
						{#if saving}
							<Loader2 class="mr-2 h-4 w-4 animate-spin" />
							Saving...
						{:else}
							<Save class="mr-2 h-4 w-4" />
							Save Settings
						{/if}
					</CarbonButton>
				</div>
			</CarbonTile>

			<!-- Studio Main Workspace Area -->
			<div class="space-y-4 lg:col-span-3">
				<!-- Workspace CarbonTabs (sticky) -->
				<div class="sticky top-0 z-10 bg-background/95 backdrop-blur">
					<CarbonTabs
						tabs={studioTabs}
						idPrefix="studio-workspace"
						selectedTab={activeTab}
						onselect={(id) => {
							if (
								id === 'mods' ||
								id === 'datapacks' ||
								id === 'resourcepacks' ||
								id === 'shaderpacks' ||
								id === 'overrides' ||
								id === 'migrate' ||
								id === 'export' ||
								id === 'maintenance'
							) {
								activeTab = id;
							}
						}}
					/>
				</div>

				<CarbonInactiveTabPanels
					tabs={studioTabs}
					selectedTab={activeTab}
					idPrefix="studio-workspace"
				/>

				<div
					id="studio-workspace-panel-{activeTab}"
					role="tabpanel"
					aria-labelledby="studio-workspace-tab-{activeTab}"
					class="focus-ring min-w-0 space-y-4"
					tabindex="0"
				>
					<!-- TAB 1: MODS MANAGEMENT -->
					{#if activeTab === 'mods'}
						<CarbonTile class="rounded-none border-border p-0">
							<div
								class="flex flex-col justify-between gap-3 border-b border-border bg-card p-4 sm:flex-row sm:items-center"
							>
								<div class="space-y-0.5">
									<div class="flex items-center gap-2">
										<h2 class="text-base font-semibold text-foreground">Mods in Modpack</h2>
										<CarbonTag type="cyan" size="sm">{pack.mods.length}</CarbonTag>
									</div>
									<p class="text-xs text-muted-foreground">
										Manage server/client sides, pinning, dependency versions, and update checks.
									</p>
								</div>

								<div class="flex flex-wrap items-center gap-2">
									<div class="w-44 sm:w-56">
										<CarbonSearch placeholder="Filter mods..." bind:value={modFilter} size="sm" />
									</div>

									<CarbonButton
										kind="tertiary"
										size="sm"
										class="rounded-none"
										onclick={checkUpdates}
										disabled={checkingUpdates}
										title="Check Modrinth for newer mod releases"
									>
										{#if checkingUpdates}
											<Loader2 class="mr-1.5 h-3.5 w-3.5 animate-spin" />
										{:else}
											<Sparkles class="mr-1.5 h-3.5 w-3.5 text-warning" />
										{/if}
										Check Updates
									</CarbonButton>

									<CarbonButton
										kind="primary"
										size="sm"
										class="rounded-none"
										onclick={() => (searchDrawerOpen = true)}
									>
										<Plus class="mr-1.5 h-4 w-4" />
										Add Mods
									</CarbonButton>
								</div>
							</div>

							<!-- Filter bar: Carbon buttons replace the raw filter pills -->
							<div
								class="flex flex-wrap items-center gap-1.5 border-b border-border bg-background p-3 text-xs"
							>
								<CarbonButton
									size="sm"
									kind={sideFilter === 'all' ? 'primary' : 'secondary'}
									class="rounded-none"
									onclick={() => (sideFilter = 'all')}
								>
									All ({pack.mods.length})
								</CarbonButton>
								<CarbonButton
									size="sm"
									kind={sideFilter === 'both' ? 'primary' : 'secondary'}
									class="rounded-none"
									onclick={() => (sideFilter = 'both')}
								>
									Both ({pack.mods.filter((m) => m.side === 'both').length})
								</CarbonButton>
								<CarbonButton
									size="sm"
									kind={sideFilter === 'client' ? 'primary' : 'secondary'}
									class="rounded-none"
									onclick={() => (sideFilter = 'client')}
								>
									Client Only ({pack.mods.filter((m) => m.side === 'client').length})
								</CarbonButton>
								<CarbonButton
									size="sm"
									kind={sideFilter === 'server' ? 'primary' : 'secondary'}
									class="rounded-none"
									onclick={() => (sideFilter = 'server')}
								>
									Server Only ({pack.mods.filter((m) => m.side === 'server').length})
								</CarbonButton>
								<CarbonButton
									size="sm"
									kind={sideFilter === 'pinned' ? 'primary' : 'secondary'}
									class="rounded-none"
									onclick={() => (sideFilter = 'pinned')}
								>
									Pinned ({pack.mods.filter((m) => m.pinned).length})
								</CarbonButton>
								<CarbonButton
									size="sm"
									kind={sideFilter === 'updates' ? 'primary' : 'secondary'}
									class="rounded-none"
									onclick={() => (sideFilter = 'updates')}
								>
									Updates ({Object.values(updatesMap).filter((u) => u.update_available).length})
								</CarbonButton>
							</div>

							<!-- Batch Action Bar -->
							{#if selectedSlugs.length > 0}
								<div
									class="flex items-center justify-between rounded-none border-b border-[var(--tag-blue-text)] bg-[var(--tag-blue-bg)] p-2.5"
								>
									<span class="font-mono text-xs font-semibold text-[var(--tag-blue-text)]">
										{selectedSlugs.length} mod(s) selected
									</span>
									<div class="flex flex-wrap items-center gap-1.5">
										<CarbonButton
											size="sm"
											kind="secondary"
											class="h-7 rounded-none text-xs"
											onclick={() => runBatchAction('set_side', 'both')}
										>
											Set Both
										</CarbonButton>
										<CarbonButton
											size="sm"
											kind="secondary"
											class="h-7 rounded-none text-xs"
											onclick={() => runBatchAction('set_side', 'server')}
										>
											Set Server
										</CarbonButton>
										<CarbonButton
											size="sm"
											kind="secondary"
											class="h-7 rounded-none text-xs"
											onclick={() => runBatchAction('set_side', 'client')}
										>
											Set Client
										</CarbonButton>
										<CarbonButton
											size="sm"
											kind="secondary"
											class="h-7 rounded-none text-xs"
											onclick={() => runBatchAction('pin')}
										>
											Pin
										</CarbonButton>
										<CarbonButton
											size="sm"
											kind="secondary"
											class="h-7 rounded-none text-xs"
											onclick={() => runBatchAction('unpin')}
										>
											Unpin
										</CarbonButton>
										<CarbonButton
											size="sm"
											kind="danger"
											class="h-7 rounded-none text-xs"
											onclick={() => (batchRemoveConfirmOpen = true)}
										>
											Remove
										</CarbonButton>
										<CarbonButton
											size="sm"
											kind="ghost"
											class="h-7 rounded-none text-xs"
											onclick={() => (selectedSlugs = [])}
										>
											Clear
										</CarbonButton>
									</div>
								</div>
							{/if}

							{#if filteredMods.length === 0}
								<div class="p-4">
									<CarbonEmptyState
										title="No mods match current filter"
										description="Adjust the side filter or search, or browse the catalog to add mods."
										actionLabel="Browse & Add Mods"
										onaction={() => (searchDrawerOpen = true)}
									/>
								</div>
							{:else}
								<StudioModTable
									{filteredMods}
									bind:selectedSlugs
									{updatesMap}
									{toggleSide}
									{togglePin}
									{removeMod}
								/>
							{/if}
						</CarbonTile>
					{/if}

					<!-- TAB: DATAPACKS -->
					{#if activeTab === 'datapacks'}
						<PackContentPanel
							packId={packId ?? ''}
							kind="datapack"
							items={pack.datapacks ?? []}
							mcVersion={pack.mc_version}
							datapackFolder={pack.datapack_folder ?? 'datapacks'}
							onchanged={loadPack}
						/>
					{/if}

					<!-- TAB: RESOURCE PACKS -->
					{#if activeTab === 'resourcepacks'}
						<PackContentPanel
							packId={packId ?? ''}
							kind="resourcepack"
							items={pack.resourcepacks ?? []}
							mcVersion={pack.mc_version}
							onchanged={loadPack}
						/>
					{/if}

					<!-- TAB: SHADER PACKS -->
					{#if activeTab === 'shaderpacks'}
						<PackContentPanel
							packId={packId ?? ''}
							kind="shaderpack"
							items={pack.shaderpacks ?? []}
							mcVersion={pack.mc_version}
							onchanged={loadPack}
						/>
					{/if}

					<!-- TAB 2: OVERRIDES & CONFIGS -->
					{#if activeTab === 'overrides'}
						<CarbonTile class="rounded-none border-border p-0">
							<div class="flex items-center justify-between border-b border-border bg-card p-4">
								<div>
									<h2 class="text-base font-semibold text-foreground">Overrides & Config Files</h2>
									<p class="text-xs text-muted-foreground">
										Packwiz overrides bundled with the modpack: server configurations, datapacks,
										and scripts.
									</p>
								</div>
								<div class="flex items-center gap-2">
									<input
										bind:this={datapackFileInput}
										type="file"
										accept=".zip,application/zip"
										class="hidden"
										onchange={handleDatapackUpload}
									/>
									<CarbonButton
										size="sm"
										kind="secondary"
										class="rounded-none"
										disabled={uploadingDatapack}
										onclick={() => datapackFileInput?.click()}
									>
										<Upload class="mr-1.5 h-4 w-4" />
										{#if uploadingDatapack}Uploading...{:else}Upload Datapack{/if}
									</CarbonButton>
									<CarbonButton
										size="sm"
										kind="secondary"
										class="rounded-none"
										onclick={() => (datapackBrowseOpen = true)}
									>
										<Package class="mr-1.5 h-4 w-4" />
										Browse Datapacks
									</CarbonButton>
									<CarbonButton
										size="sm"
										class="rounded-none"
										onclick={() => (uploadFileDialogOpen = true)}
									>
										<Plus class="mr-1.5 h-4 w-4" />
										Add Override File
									</CarbonButton>
								</div>
							</div>

							{#if loadingFiles}
								<div class="flex items-center justify-center py-16">
									<CarbonInlineLoading description="Scanning override files..." />
								</div>
							{:else if packFiles.length === 0}
								<div class="space-y-3 p-4">
									<CarbonEmptyState
										title="No override files found"
										description="Add custom server configs, datapacks, or options to bundle them directly into the modpack."
										actionLabel="Add Config File"
										onaction={() => (uploadFileDialogOpen = true)}
									/>
									<div class="flex justify-center">
										<CarbonButton
											size="sm"
											kind="secondary"
											class="rounded-none"
											onclick={() => (datapackBrowseOpen = true)}
										>
											<Package class="mr-1.5 h-4 w-4" />
											Browse Datapacks
										</CarbonButton>
									</div>
								</div>
							{:else}
								<div class="overflow-x-auto">
									<table class="w-full border-collapse text-left text-xs">
										<thead
											class="border-b border-border bg-secondary font-semibold text-foreground uppercase"
										>
											<tr>
												<th scope="col" class="px-4 py-2.5">File Path</th>
												<th scope="col" class="px-4 py-2.5">Category</th>
												<th scope="col" class="px-4 py-2.5">Size</th>
												<th scope="col" class="px-4 py-2.5 text-right">Actions</th>
											</tr>
										</thead>
										<tbody class="divide-y divide-border bg-card text-foreground">
											{#each packFiles as file (file.path)}
												<tr class="transition-colors hover:bg-secondary">
													<td class="px-4 py-2.5 font-mono text-xs font-semibold text-foreground">
														{file.path}
													</td>
													<td class="px-4 py-2.5">
														<CarbonTag type="gray" size="sm" class="capitalize">
															{file.category}
														</CarbonTag>
													</td>
													<td class="px-4 py-2.5 font-mono text-muted-foreground">
														{formatBytes(file.size)}
													</td>
													<td class="px-4 py-2.5 text-right">
														<CarbonButton
															kind="ghost"
															size="sm"
															iconOnly
															class="rounded-none text-destructive-text hover:bg-destructive/20"
															onclick={() => handleDeleteFile(file.path)}
															title="Delete file"
															aria-label="Delete file"
														>
															<Trash2 class="h-3.5 w-3.5" />
														</CarbonButton>
													</td>
												</tr>
											{/each}
										</tbody>
									</table>
								</div>
							{/if}
						</CarbonTile>
					{/if}

					<!-- TAB 3: VERSION MIGRATION -->
					{#if activeTab === 'migrate'}
						<CarbonTile class="space-y-4 rounded-none border-border">
							<div class="border-b border-border pb-3">
								<h2 class="text-base font-semibold text-foreground">
									Minecraft Version Migration Assistant
								</h2>
								<p class="text-xs text-muted-foreground">
									Simulate upgrading all pack mods to a newer Minecraft release or loader before
									applying changes.
								</p>
							</div>

							<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
								<CarbonSelect label="Target Minecraft Version" bind:value={migrateMC}>
									{#each MC_VERSIONS as v (v)}
										<option value={v}>{v}</option>
									{/each}
								</CarbonSelect>

								<CarbonSelect label="Target Mod Loader" bind:value={migrateLoader}>
									<option value="fabric">Fabric</option>
									<option value="neoforge">NeoForge</option>
									<option value="forge">Forge</option>
									<option value="quilt">Quilt</option>
								</CarbonSelect>
							</div>

							<div class="flex items-center gap-3 pt-2">
								<CarbonButton
									kind="secondary"
									size="sm"
									class="rounded-none"
									onclick={() => simulateMigration(false)}
									disabled={migrating}
								>
									{#if migrating}
										<Loader2 class="mr-1.5 h-3.5 w-3.5 animate-spin" />
									{:else}
										<Sparkles class="mr-1.5 h-3.5 w-3.5 text-accent-text" />
									{/if}
									Simulate Compatibility
								</CarbonButton>

								<CarbonButton
									kind="primary"
									size="sm"
									class="rounded-none"
									onclick={() => simulateMigration(true)}
									disabled={migrating}
								>
									<ArrowRightLeft class="mr-1.5 h-3.5 w-3.5" />
									Apply Migration
								</CarbonButton>
							</div>

							{#if migrationReport}
								<div class="mt-4 space-y-3 rounded-none border border-border bg-background p-4">
									<div class="flex items-center justify-between">
										<div class="flex items-center gap-2">
											<CarbonTag type="green" size="md">
												{migrationReport.compatible_count} Compatible
											</CarbonTag>
											<CarbonTag type="red" size="md">
												{migrationReport.incompatible_count} Missing Version
											</CarbonTag>
										</div>
										<span class="font-mono text-xs text-muted-foreground">
											Target: MC {migrationReport.target_mc} ({migrationReport.target_loader})
										</span>
									</div>

									<!-- Carbon Structured List for Migration Details -->
									<CarbonStructuredList class="mt-3">
										{#snippet header()}
											<div class="flex w-full justify-between">
												<span>Mod Slug / Name</span>
												<span>Target Compatibility Status</span>
											</div>
										{/snippet}

										{#each migrationReport.mods as m (m.slug)}
											<div class="flex items-center justify-between px-4 py-2.5 font-mono text-xs">
												<span class="font-medium text-foreground">{m.name}</span>
												{#if m.compatible}
													<span class="flex items-center gap-1 text-success-text">
														<CheckCircle2 class="h-3.5 w-3.5" />
														Compatible
													</span>
												{:else}
													<span class="flex items-center gap-1 text-destructive-text">
														<AlertTriangle class="h-3.5 w-3.5" />
														No version for {migrationReport.target_mc}
													</span>
												{/if}
											</div>
										{/each}
									</CarbonStructuredList>
								</div>
							{/if}
						</CarbonTile>
					{/if}

					<!-- TAB 4: EXPORT & MANIFEST -->
					{#if activeTab === 'export'}
						<CarbonTile class="space-y-6 rounded-none border-border">
							<div class="border-b border-border pb-3">
								<div class="flex flex-wrap items-center justify-between gap-2">
									<div>
										<h2 class="text-base font-semibold">Export & Manifest</h2>
										<p class="text-xs text-muted-foreground">
											Launcher exports, manifest breakdown, raw TOML, and manifest inspection.
										</p>
									</div>
									<div class="flex flex-wrap items-center gap-2">
										<CarbonButton
											size="sm"
											kind="secondary"
											class="rounded-none"
											onclick={() => (inspectorOpen = true)}
										>
											Inspect Manifest
										</CarbonButton>
										<CarbonButton
											size="sm"
											class="rounded-none"
											onclick={() => (exportDialogOpen = true)}
										>
											<Download class="mr-1.5 h-3.5 w-3.5" />
											Export Pack
										</CarbonButton>
									</div>
								</div>
							</div>

							<!-- Manifest breakdown -->
							<div class="space-y-2">
								<div class="flex flex-wrap items-center gap-2">
									<CarbonTag type="cyan" size="sm">Total {contentBreakdown?.total ?? 0}</CarbonTag>
									<CarbonTag type="blue" size="sm">
										Both {contentBreakdown?.both ?? 0}
									</CarbonTag>
									<CarbonTag type="purple" size="sm">
										Client {contentBreakdown?.client ?? 0}
									</CarbonTag>
									<CarbonTag type="green" size="sm">
										Server {contentBreakdown?.server ?? 0}
									</CarbonTag>
									<CarbonTag type="gray" size="sm">
										Pinned {contentBreakdown?.pinned ?? 0}
									</CarbonTag>
								</div>
								<CarbonStructuredList>
									{#snippet header()}
										<div class="flex w-full justify-between">
											<span>Side</span>
											<span>Count</span>
										</div>
									{/snippet}
									<div class="flex items-center justify-between px-4 py-2.5 font-mono text-xs">
										<span class="text-foreground">Client-only (excluded with server export)</span>
										<span>{contentBreakdown?.clientOnly ?? 0}</span>
									</div>
									<div class="flex items-center justify-between px-4 py-2.5 font-mono text-xs">
										<span class="text-foreground">CurseForge-ready (numeric IDs)</span>
										<span
											>{contentBreakdown?.curseforgeReady ?? 0} of {contentBreakdown?.total ??
												0}</span
										>
									</div>
									<div class="flex items-center justify-between px-4 py-2.5 font-mono text-xs">
										<span class="text-foreground">Override files bundled</span>
										<span>{contentBreakdown?.overrides ?? 0}</span>
									</div>
								</CarbonStructuredList>
							</div>

							<!-- Raw TOML Inspector -->
							<div class="space-y-2">
								<div class="flex items-center justify-between">
									<span class="text-xs font-semibold text-foreground">Raw pack.toml Preview</span>
									<CarbonButton
										kind="ghost"
										size="sm"
										class="rounded-none text-xs"
										onclick={loadRawTomls}
										disabled={loadingRaw}
									>
										Reload TOML
									</CarbonButton>
								</div>
								{#if loadingRaw}
									<div class="flex items-center justify-center py-8">
										<CarbonInlineLoading description="Loading pack.toml..." />
									</div>
								{:else if rawPackToml}
									<pre
										class="max-h-48 overflow-y-auto rounded-none border border-border bg-muted p-3 font-mono text-xs">{rawPackToml}</pre>
								{:else}
									<CarbonEmptyState
										title="No TOML preview"
										description="Reload to fetch the generated pack.toml for this project."
										actionLabel="Reload TOML"
										onaction={loadRawTomls}
									/>
								{/if}
							</div>
						</CarbonTile>
					{/if}

					<!-- TAB 5: MAINTENANCE -->
					{#if activeTab === 'maintenance'}
						<CarbonTile class="space-y-6 rounded-none border-border">
							<div class="border-b border-border pb-3">
								<h2 class="text-base font-semibold text-foreground">
									Packwiz Maintenance & Live Serving
								</h2>
								<p class="text-xs text-muted-foreground">
									Rebuild indices and view live container bootstrap URLs.
								</p>
							</div>

							<!-- Live Serving URL Tile -->
							<div class="space-y-2 rounded-none border border-border bg-background p-4">
								<div class="flex items-center justify-between">
									<span class="text-xs font-semibold text-foreground"
										>Live Container Bootstrap URL (PACKWIZ_URL)</span
									>
									<CarbonButton
										kind="ghost"
										size="sm"
										class="rounded-none text-xs"
										onclick={() => {
											const u = `${typeof window !== 'undefined' ? window.location.origin : ''}/api/v1/packwiz/${pack?.id}/pack.toml`;
											navigator.clipboard.writeText(u);
											toast.success('Copied PACKWIZ_URL to clipboard!');
										}}
									>
										<Copy class="mr-1.5 h-3.5 w-3.5" />
										Copy URL
									</CarbonButton>
								</div>
								<div
									class="rounded-none border border-border bg-card p-2.5 font-mono text-xs break-all text-success-text select-all"
								>
									{typeof window !== 'undefined'
										? `${window.location.origin}/api/v1/packwiz/${pack?.id}/pack.toml`
										: `/api/v1/packwiz/${pack?.id}/pack.toml`}
								</div>
								<p class="text-xs text-muted-foreground">
									Set this URL in Docker container environments or packwiz-installer to dynamically
									provision this modpack on startup.
								</p>
							</div>

							<!-- Refresh Index -->
							<div
								class="flex items-center justify-between rounded-none border border-border bg-background p-4"
							>
								<div class="space-y-0.5">
									<h3 class="text-sm font-semibold text-foreground">Refresh Packwiz Index</h3>
									<p class="text-xs text-muted-foreground">
										Recalculate SHA256 hashes for all .pw.toml and override files according to
										packwiz refresh.
									</p>
								</div>
								<CarbonButton
									kind="secondary"
									size="sm"
									class="rounded-none"
									onclick={handleRefreshPack}
									disabled={refreshing}
								>
									{#if refreshing}
										<Loader2 class="mr-1.5 h-3.5 w-3.5 animate-spin" />
									{:else}
										<RefreshCw class="mr-1.5 h-3.5 w-3.5 text-accent-text" />
									{/if}
									Run Refresh
								</CarbonButton>
							</div>
						</CarbonTile>
					{/if}
				</div>
			</div>
		</div>
	{/if}
</div>

<!-- Add Mods Modal (Modrinth, CurseForge, URL) with Carbon Design System Fidelity -->
<CarbonModal
	bind:open={searchDrawerOpen}
	title="Add Mods to Packwiz"
	description={pack
		? `Search online catalogs for ${pack.mod_loader.toUpperCase()} MC ${pack.mc_version}`
		: 'Search online mods'}
	hasFooter={false}
	size="4xl"
>
	<div class="space-y-4">
		<!-- Add Mod Source Tabs -->
		<CarbonTabs
			tabs={addModSourceTabs}
			idPrefix="studio-add-mod"
			selectedTab={addTab}
			onselect={(id) => {
				if (id === 'modrinth' || id === 'curseforge' || id === 'url') {
					addTab = id;
					if (addTab !== 'url') searchMods();
				}
			}}
		/>

		<CarbonInactiveTabPanels
			tabs={addModSourceTabs}
			selectedTab={addTab}
			idPrefix="studio-add-mod"
		/>

		<div
			id="studio-add-mod-panel-{addTab}"
			role="tabpanel"
			aria-labelledby="studio-add-mod-tab-{addTab}"
			class="focus-ring min-w-0 space-y-4"
			tabindex="0"
		>
			{#if addTab !== 'url'}
				<!-- Shared search bar: same idiom as the mods browser dialog. The
					source tabs above own the platform switch, so the bar renders
					input + button only. -->
				<ModsCatalogSearchBar
					platforms={[]}
					bind:query={searchQuery}
					{searching}
					placeholder="Search online mods..."
					onsubmit={searchMods}
				/>

				<!-- Search Results List -->
				<div class="mt-4 max-h-[460px] min-h-[300px] space-y-2.5 overflow-y-auto">
					{#if searchError}
						<div
							class="rounded-none border-l-4 border-destructive bg-destructive/10 p-4 text-xs text-destructive-text"
						>
							{searchError}
						</div>
					{:else if searching}
						<div class="flex items-center justify-center py-16">
							<CarbonInlineLoading description="Searching mods..." />
						</div>
					{:else if searchResults.length === 0}
						<CarbonEmptyState
							title="Enter a search query"
							description="Search the online catalog to add mods to this pack."
						/>
					{:else}
						{#each searchResults as item (item.id)}
							<!-- Shared result row with per-row pending state (E16). -->
							<ModsCatalogResultCard
								result={{
									title: item.title,
									description: item.description,
									iconUrl: item.icon_url,
									author: item.author,
									downloads: item.downloads,
									categories: item.categories,
									platform: item.platform
								}}
								maxCategories={2}
							>
								{#snippet actions()}
									<CarbonButton
										size="sm"
										kind="tertiary"
										class="rounded-none px-2 text-xs"
										disabled={addingSlugs[item.id] !== undefined}
										onclick={() => addModToPack(item, 'client')}
										title="Add as client-only"
									>
										+ Client
									</CarbonButton>
									<CarbonButton
										size="sm"
										kind="tertiary"
										class="rounded-none px-2 text-xs"
										disabled={addingSlugs[item.id] !== undefined}
										onclick={() => addModToPack(item, 'server')}
										title="Add as server-only"
									>
										+ Server
									</CarbonButton>
									<CarbonButton
										size="sm"
										kind="primary"
										class="rounded-none px-2.5 text-xs"
										loading={addingSlugs[item.id] === 'both'}
										disabled={addingSlugs[item.id] !== undefined}
										onclick={() => addModToPack(item, 'both')}
										title="Add for both"
									>
										<Plus class="mr-1 h-3.5 w-3.5" />
										Add (Both)
									</CarbonButton>
								{/snippet}
							</ModsCatalogResultCard>
						{/each}
					{/if}
				</div>
			{:else}
				<!-- Direct URL Tab -->
				<div class="space-y-4 py-2">
					<CarbonTextInput
						label="Direct Download URL (*.jar) *"
						placeholder="https://example.com/mods/my-custom-mod.jar"
						bind:value={urlModDownloadUrl}
						helperText="Direct download link. Packwiz will download and compute SHA256 hashes automatically."
					/>

					<div class="grid grid-cols-1 gap-4 md:grid-cols-2">
						<CarbonTextInput
							label="Mod Name (Optional)"
							placeholder="Custom Mod"
							bind:value={urlModName}
						/>
						<CarbonTextInput
							label="File Name (Optional)"
							placeholder="mod.jar"
							bind:value={urlModFileName}
						/>
					</div>

					<div class="grid grid-cols-1 gap-4 md:grid-cols-2">
						<CarbonSelect label="Side" bind:value={urlModSide}>
							<option value="both">Both (Client & Server)</option>
							<option value="server">Server Only</option>
							<option value="client">Client Only</option>
						</CarbonSelect>

						<div class="flex items-center gap-2 pt-6">
							<input
								type="checkbox"
								id="urlPin"
								bind:checked={urlModPinned}
								class="cursor-pointer rounded-none border-border bg-card"
							/>
							<label for="urlPin" class="cursor-pointer text-xs text-muted-foreground select-none"
								>Pin version (prevent automatic updates)</label
							>
						</div>
					</div>

					<CarbonButton
						class="mt-2 w-full justify-center rounded-none"
						onclick={handleAddUrlMod}
						disabled={addingUrlMod}
					>
						{#if addingUrlMod}
							<Loader2 class="mr-2 h-4 w-4 animate-spin" />
							Fetching & Hashing...
						{:else}
							<Plus class="mr-1.5 h-4 w-4" />
							Add Mod from URL
						{/if}
					</CarbonButton>
				</div>
			{/if}
		</div>
	</div>
</CarbonModal>

<DatapackBrowserDialog
	bind:open={datapackBrowseOpen}
	gameVersion={pack?.mc_version ?? ''}
	description="Search and install datapacks to bundle under the pack's datapacks/ override. Datapacks run on Vanilla servers with no mod loader."
	install={installStudioDatapack}
	onInstalled={loadFiles}
/>

<!-- E17: batch removal is destructive; it asks before deleting. -->
<CarbonConfirm
	bind:open={batchRemoveConfirmOpen}
	title="Remove selected mods?"
	message="The selected mods are removed from the modpack. This cannot be undone."
	confirmLabel="Remove {selectedSlugs.length} mod{selectedSlugs.length === 1 ? '' : 's'}"
	danger
	confirming={batchRemoving}
	onconfirm={confirmBatchRemove}
/>

<!-- Upload / Add Override File Modal -->
<CarbonModal
	bind:open={uploadFileDialogOpen}
	title="Add Override File"
	description="Bundle a custom configuration or options file into your Packwiz modpack"
	hasFooter={false}
	size="lg"
>
	<div class="space-y-4">
		<CarbonTextInput
			label="Relative File Path *"
			placeholder="e.g. config/options.txt"
			bind:value={uploadFilePath}
		/>

		<div class="space-y-1.5">
			<label class="text-xs font-normal tracking-[0.32px] text-muted-foreground"
				>File Content (Text / JSON / YAML)</label
			>
			<textarea
				bind:value={uploadFileContent}
				rows={8}
				class="w-full rounded-none border-b border-border bg-card p-3 font-mono text-xs text-foreground focus:border-primary focus:outline-none"
				placeholder="Enter configuration content here..."
			></textarea>
		</div>

		<div class="flex items-center justify-end gap-3 border-t border-border pt-4">
			<CarbonButton
				kind="secondary"
				onclick={() => (uploadFileDialogOpen = false)}
				disabled={savingFile}
				class="rounded-none"
			>
				Cancel
			</CarbonButton>
			<CarbonButton
				kind="primary"
				onclick={handleSaveFile}
				disabled={savingFile}
				class="rounded-none"
			>
				{#if savingFile}
					<Loader2 class="mr-2 h-4 w-4 animate-spin" />
					Saving...
				{:else}
					Save File
				{/if}
			</CarbonButton>
		</div>
	</div>
</CarbonModal>

<!-- Deploy Dialog -->
{#if pack}
	<ModpackDeployDialog
		bind:open={deployDialogOpen}
		packId={pack.id}
		packName={pack.name}
		mcVersion={pack.mc_version}
		modLoader={pack.mod_loader}
	/>
	<ExportDialog
		bind:open={exportDialogOpen}
		packId={pack.id}
		packName={pack.name}
		mods={allPackContentMods(pack)}
	/>
{/if}
<ManifestInspectorDialog bind:open={inspectorOpen} onOpenChange={(v) => (inspectorOpen = v)} />
<CarbonConfirm
	bind:open={confirmDeleteOpen}
	title="Delete override file?"
	message="The file is removed from the packwiz project. This cannot be undone."
	confirmLabel="Delete File"
	danger
	onconfirm={confirmDeleteFile}
	onclose={() => (pendingDeleteFile = null)}
>
	{#snippet details()}
		{#if pendingDeleteFile}{pendingDeleteFile}{/if}
	{/snippet}
</CarbonConfirm>
