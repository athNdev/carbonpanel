<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { untrack } from 'svelte';
	import { effectiveLoader, loaderDisplayName, loaderSearchName } from '$lib/utils/mod-loader';
	import { Dialog as DialogPrimitive } from 'bits-ui';
	import {
		DialogContent,
		DialogDescription,
		DialogHeader,
		DialogTitle
	} from '$lib/components/ui/dialog';
	import { CarbonButton, CarbonTag } from '$lib/components/carbon';
	import { Input } from '$lib/components/ui/input';
	import { Badge } from '$lib/components/ui/badge';
	import { Tabs, TabsList, TabsTrigger } from '$lib/components/ui/tabs';
	import { Card, CardContent } from '$lib/components/ui/card';
	import { Alert, AlertDescription, AlertTitle } from '$lib/components/ui/alert';
	import {
		Search,
		Download,
		Package,
		Loader2,
		ArrowLeft,
		Boxes,
		AlertTriangle,
		Layers,
		Filter,
		X,
		Check,
		ChevronDown
	} from '@lucide/svelte';
	import { toast } from 'svelte-sonner';
	import type { Server } from '$lib/proto/carbonpanel/v1/common_pb';
	import { formatBytes } from '$lib/utils';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { createRequestSequence } from '$lib/utils/request-sequence';
	import { resolveWizardMods, type QueuedServerMod } from '$lib/features/servers/create-content';
	import type {
		BrowseModResult as RpcBrowseMod,
		ModVersion as RpcModVersion,
		ModDependency as RpcModDependency
	} from '$lib/proto/carbonpanel/v1/mod_browse_pb';

	interface Props {
		open: boolean;
		server: Pick<Server, 'id' | 'mcVersion' | 'modLoader' | 'modpackLoader'>;
		onInstalled?: () => void;
		onQueue?: (mods: QueuedServerMod[]) => void;
		queuedMods?: QueuedServerMod[];
	}

	interface SearchMod {
		id: string;
		slug: string;
		title: string;
		description: string;
		icon_url: string;
		author: string;
		downloads: number;
		categories: string[];
		platform: string;
		client_side?: string;
		server_side?: string;
		installed: boolean;
	}

	interface ModDependency {
		project_id: string;
		version_id?: string;
		dependency_type: string;
		title?: string;
	}

	interface ModVersion {
		id: string;
		version_number: string;
		name: string;
		version_type: string;
		file_name: string;
		download_url: string;
		file_size: number;
		dependencies: ModDependency[];
	}

	interface ActiveFilter {
		key: string;
		value: string;
		removable: boolean;
	}

	// The generated contract uses camelCase and int64 (bigint) fields. Map at
	// the transport boundary so the dialog's render state stays unchanged.
	function toSearchMod(r: RpcBrowseMod): SearchMod {
		return {
			id: r.id,
			slug: r.slug,
			title: r.title,
			description: r.description,
			icon_url: r.iconUrl,
			author: r.author,
			downloads: Number(r.downloads),
			categories: r.categories,
			platform: r.platform,
			client_side: r.clientSide,
			server_side: r.serverSide,
			installed: r.installed
		};
	}

	function toModDependency(d: RpcModDependency): ModDependency {
		return {
			project_id: d.projectId,
			version_id: d.versionId,
			dependency_type: d.dependencyType,
			title: d.title || undefined
		};
	}

	function toModVersion(v: RpcModVersion): ModVersion {
		return {
			id: v.id,
			version_number: v.versionNumber,
			name: v.name,
			version_type: v.versionType,
			file_name: v.fileName,
			download_url: v.downloadUrl,
			file_size: Number(v.fileSize),
			dependencies: v.dependencies.map(toModDependency)
		};
	}

	let { open = $bindable(false), server, onInstalled, onQueue, queuedMods = [] }: Props = $props();

	let searchQuery = $state('');
	let platform = $state<'modrinth' | 'curseforge'>('modrinth');
	let searching = $state(false);
	let mods = $state<SearchMod[]>([]);
	let searchError = $state('');

	// Version selection view
	let selectedMod = $state<SearchMod | null>(null);
	let loadingVersions = $state(false);
	let versions = $state<ModVersion[]>([]);
	let versionRecords = $state<RpcModVersion[]>([]);
	let selectedVersion = $state<ModVersion | null>(null);
	let selectedDependencies = $state<Record<string, boolean>>({});
	let installing = $state(false);

	// Mods installed through this dialog session. Backs the header count chip
	// and the session summary so a completed install stays visible after the
	// version view closes instead of dumping back to a bare search list.
	interface SessionInstalledMod {
		key: string;
		title: string;
		version: string;
		depCount: number;
	}
	let sessionInstalled = $state<SessionInstalledMod[]>([]);
	let sessionSummaryOpen = $state(true);
	let sessionInstalledCount = $derived(sessionInstalled.length);

	function sessionKey(mod: { platform: string; id: string }): string {
		return `${mod.platform}:${mod.id}`;
	}

	let selectedDepIds = $derived(
		Object.keys(selectedDependencies).filter((k) => selectedDependencies[k])
	);
	// Real game loader underneath a modpack platform; plain modLoader otherwise.
	let gameLoader = $derived(effectiveLoader(server.modLoader, server.modpackLoader));

	let installLabel = $derived(
		onQueue
			? 'Add to server setup'
			: selectedDepIds.length > 0
				? 'Install with all dependencies'
				: 'Install'
	);

	let filterByVersion = $state(true);

	// Filter Bar State
	let activeFilters = $state<ActiveFilter[]>([
		{ key: 'side', value: 'server', removable: true },
		{ key: 'loader', value: loaderSearchName(gameLoader), removable: false },
		...(server.mcVersion ? [{ key: 'version', value: server.mcVersion, removable: true }] : [])
	]);

	let filterInput = $state('');
	let filterInputFocused = $state(false);

	const FILTER_VOCABULARY = [
		{ label: 'side:server', desc: 'Server-compatible only (omits client-only)' },
		{ label: 'side:both', desc: 'Runs on client and server' },
		{ label: 'side:client', desc: 'Client-side mods' },
		{ label: 'loader:fabric', desc: 'Fabric mod loader' },
		{ label: 'loader:forge', desc: 'Forge mod loader' },
		{ label: 'loader:neoforge', desc: 'NeoForge mod loader' },
		{ label: 'loader:quilt', desc: 'Quilt mod loader' },
		{ label: 'version:1.21.4', desc: 'Minecraft 1.21.4' },
		{ label: 'version:1.21.1', desc: 'Minecraft 1.21.1' },
		{ label: 'version:1.20.4', desc: 'Minecraft 1.20.4' },
		{ label: 'version:1.20.1', desc: 'Minecraft 1.20.1' },
		{ label: 'version:1.19.4', desc: 'Minecraft 1.19.4' },
		{ label: 'version:1.19.2', desc: 'Minecraft 1.19.2' },
		{ label: 'version:1.18.2', desc: 'Minecraft 1.18.2' },
		{ label: 'version:1.16.5', desc: 'Minecraft 1.16.5' },
		{ label: 'category:optimization', desc: 'Performance and FPS' },
		{ label: 'category:technology', desc: 'Tech & machinery' },
		{ label: 'category:magic', desc: 'Spells, rituals, sorcery' },
		{ label: 'category:storage', desc: 'Chests & backpacks' },
		{ label: 'category:adventure', desc: 'Dungeons & exploration' },
		{ label: 'category:utility', desc: 'QoL tools & utilities' },
		{ label: 'category:worldgen', desc: 'Biomes & structures' }
	];

	let suggestions = $derived.by(() => {
		const q = filterInput.trim().toLowerCase();
		if (!q) {
			return FILTER_VOCABULARY.slice(0, 6);
		}
		return FILTER_VOCABULARY.filter(
			(item) => item.label.toLowerCase().includes(q) || item.desc.toLowerCase().includes(q)
		).slice(0, 6);
	});

	function addFilter(filterStr: string) {
		const parts = filterStr.split(':');
		if (parts.length >= 2) {
			const key = parts[0].trim().toLowerCase();
			const value = parts.slice(1).join(':').trim().toLowerCase();
			activeFilters = activeFilters.filter((f) => f.key !== key);
			activeFilters.push({ key, value, removable: true });
			filterInput = '';
			filterInputFocused = false;
			searchMods();
		}
	}

	function removeFilter(key: string) {
		activeFilters = activeFilters.filter((f) => f.key !== key);
		searchMods();
	}

	// Filtered mods list - omits client-only mods by default in server context
	let displayMods = $derived.by(() => {
		if (onQueue) return mods.filter((mod) => mod.server_side !== 'unsupported');
		const sideFilter = activeFilters.find((f) => f.key === 'side')?.value;
		if (sideFilter === 'server') {
			return mods.filter((m) => m.server_side !== 'unsupported');
		} else if (sideFilter === 'client') {
			return mods.filter((m) => m.client_side !== 'unsupported');
		}
		return mods;
	});

	// Sequence guards: drop responses from superseded requests so a slow
	// earlier keystroke/filter change can never overwrite newer results.
	const searchSequence = createRequestSequence();
	const versionsSequence = createRequestSequence();

	function browseTarget() {
		return {
			loader: onQueue
				? loaderSearchName(gameLoader)
				: activeFilters.find((filter) => filter.key === 'loader')?.value ||
					loaderSearchName(gameLoader),
			mcVersion: onQueue
				? server.mcVersion
				: activeFilters.find((filter) => filter.key === 'version')?.value ||
					(filterByVersion ? server.mcVersion || '' : ''),
			side: onQueue ? 'server' : activeFilters.find((filter) => filter.key === 'side')?.value || ''
		};
	}

	async function searchMods() {
		searching = true;
		const seq = searchSequence.next();
		searchError = '';
		mods = [];
		try {
			// The legacy route ignored a `category` query param; the generated
			// contract exposes only the filters the shared operation applies.

			const response = await rpcClient.modBrowse.searchMods(
				{
					serverId: server.id,
					query: searchQuery.trim(),
					platform,
					...browseTarget()
				},
				silentCallOptions
			);
			if (!searchSequence.isCurrent(seq)) return;
			mods = response.results.map(toSearchMod);
		} catch (err) {
			if (!searchSequence.isCurrent(seq)) return;
			console.error('Failed to search mods:', err);
			searchError = `Failed to search mods: ${(err instanceof Error ? err.message : undefined) || 'Please verify server connection.'}`;
		} finally {
			if (searchSequence.isCurrent(seq)) searching = false;
		}
	}

	async function viewModVersions(mod: SearchMod) {
		selectedMod = mod;
		loadingVersions = true;
		const seq = versionsSequence.next();
		versions = [];
		versionRecords = [];
		selectedVersion = null;
		selectedDependencies = {};

		try {
			const { loader, mcVersion } = browseTarget();
			const modTarget = mod.platform === 'curseforge' && mod.id ? mod.id : mod.slug || mod.id;
			const response = await rpcClient.modBrowse.getModVersions(
				{
					serverId: server.id,
					project: modTarget,
					platform: mod.platform,
					loader,
					mcVersion
				},
				silentCallOptions
			);
			if (!versionsSequence.isCurrent(seq)) return;
			versionRecords = response.versions;
			versions = response.versions.map(toModVersion);
			if (versions.length > 0) {
				selectVersion(versions[0]);
			}
		} catch (err) {
			if (!versionsSequence.isCurrent(seq)) return;
			console.error('Failed to load versions:', err);
			toast.error('Failed to load compatible versions for this mod');
		} finally {
			if (versionsSequence.isCurrent(seq)) loadingVersions = false;
		}
	}

	function selectVersion(ver: ModVersion) {
		selectedVersion = ver;
		selectedDependencies = {};
		for (const dep of ver.dependencies || []) {
			if (!dep.project_id) continue;
			if ((dep.dependency_type || '').toLowerCase() === 'required') {
				selectedDependencies[dep.project_id] = true;
			}
		}
	}

	interface InstallItem {
		name: string;
		file_name: string;
		download_url: string;
	}

	async function resolveDependencyInstallItems(
		deps: ModDependency[],
		platform: string,
		loaderVal: string,
		versionVal: string,
		depth: number,
		visited: Set<string>
	): Promise<InstallItem[]> {
		if (depth > 3) return [];
		const collected: InstallItem[] = [];
		const wanted = (deps || []).filter((d) => d.project_id && selectedDependencies[d.project_id]);
		for (const dep of wanted) {
			const depId = dep.project_id as string;
			const key = `${platform.toLowerCase()}:${depId.toLowerCase()}`;
			if (visited.has(key)) continue;
			visited.add(key);
			try {
				const response = await rpcClient.modBrowse.getModVersions(
					{
						serverId: server.id,
						project: depId,
						platform,
						loader: loaderVal,
						mcVersion: versionVal
					},
					silentCallOptions
				);
				const depVersions = response.versions.map(toModVersion);
				if (depVersions.length === 0) continue;
				const ver = depVersions[0];
				if (!ver.file_name || !ver.download_url) continue;
				// Fill in a human-readable name when the versions response
				// only carried the raw project ID.
				if (!dep.title) dep.title = ver.name || ver.version_number || depId;
				// Pre-select this dependency's own required deps so transitive
				// requirements are included in the same install.
				for (const trans of ver.dependencies || []) {
					if (!trans.project_id) continue;
					if ((trans.dependency_type || '').toLowerCase() === 'required') {
						const tKey = `${platform.toLowerCase()}:${trans.project_id.toLowerCase()}`;
						if (!visited.has(tKey) && selectedDependencies[trans.project_id] === undefined) {
							selectedDependencies[trans.project_id] = true;
						}
					}
				}
				collected.push({
					name: dep.title || ver.name || ver.version_number || depId,
					file_name: ver.file_name,
					download_url: ver.download_url
				});
				const transitive = await resolveDependencyInstallItems(
					ver.dependencies || [],
					platform,
					loaderVal,
					versionVal,
					depth + 1,
					visited
				);
				collected.push(...transitive);
			} catch (err) {
				console.warn(`Skipping unresolvable dependency ${depId}:`, err);
			}
		}
		return collected;
	}

	async function installMod() {
		if (!selectedVersion || !selectedMod) return;

		installing = true;
		try {
			if (onQueue) {
				const project = selectedMod;
				const version = versionRecords.find((entry) => entry.id === selectedVersion?.id);
				if (!version) return;
				const { loader, mcVersion } = browseTarget();
				const key = `${project.platform}:${project.id}`;
				const selections = queuedMods
					.filter((mod) => mod.project !== key)
					.map((mod) => mod.selection);
				const plan = await resolveWizardMods(
					{ loader, mcVersion },
					[
						...selections,
						{ id: project.id, name: project.title, platform: project.platform, version }
					],
					async (target) =>
						(await rpcClient.modBrowse.getModVersions(target, silentCallOptions)).versions
				);
				if (!open || selectedMod !== project || selectedVersion?.id !== version.id) return;
				onQueue(plan);
				selectedMod = null;
				return;
			}
			const loaderVal =
				activeFilters.find((f) => f.key === 'loader')?.value || loaderSearchName(gameLoader);
			const versionVal =
				activeFilters.find((f) => f.key === 'version')?.value ||
				(filterByVersion ? server.mcVersion || '' : '');
			const platform = selectedMod.platform;

			const items: InstallItem[] = [
				{
					name: selectedMod.title,
					file_name: selectedVersion.file_name,
					download_url: selectedVersion.download_url
				}
			];

			const visited = new Set<string>([
				`${platform.toLowerCase()}:${selectedMod.id.toLowerCase()}`,
				`slug:${(selectedMod.slug || '').toLowerCase()}`
			]);
			const depItems = await resolveDependencyInstallItems(
				selectedVersion.dependencies || [],
				platform,
				loaderVal,
				versionVal,
				0,
				visited
			);
			items.push(...depItems);

			const data = await rpcClient.modBrowse.installMods(
				{
					serverId: server.id,
					items: items.map((item) => ({
						name: item.name,
						fileName: item.file_name,
						downloadUrl: item.download_url
					}))
				},
				silentCallOptions
			);

			if (data.success) {
				toast.success(data.message || `Installed ${selectedMod.title}`);
				selectedMod.installed = true;
				const installedKey = sessionKey(selectedMod);
				sessionInstalled = [
					...sessionInstalled.filter((entry) => entry.key !== installedKey),
					{
						key: installedKey,
						title: selectedMod.title,
						version: selectedVersion.version_number || selectedVersion.name,
						depCount: (selectedVersion.dependencies || []).length
					}
				];
				sessionSummaryOpen = true;
				if (onInstalled) {
					onInstalled();
				}
				selectedMod = null;
			} else {
				const detail = data.errors.join('; ');
				toast.error(detail ? `${data.message} ${detail}` : data.message || 'Failed to install mod');
			}
		} catch (err) {
			console.error('Failed to install mod:', err);
			toast.error(
				(err instanceof Error ? err.message : undefined) || 'Installation request failed'
			);
		} finally {
			installing = false;
		}
	}

	function formatDownloads(count: number): string {
		if (!count) return '0';
		if (count >= 1_000_000) {
			return `${(count / 1_000_000).toFixed(1)}M`;
		}
		if (count >= 1_000) {
			return `${(count / 1_000).toFixed(1)}k`;
		}
		return count.toString();
	}

	let wasOpen = false;
	$effect(() => {
		if (open && !wasOpen) {
			wasOpen = true;
			untrack(() => {
				searchMods();
			});
		} else if (!open && wasOpen) {
			wasOpen = false;
			selectedMod = null;
			sessionInstalled = [];
			sessionSummaryOpen = true;
		}
	});
</script>

<DialogPrimitive.Root bind:open>
	<DialogContent class="flex max-h-[90vh] !max-w-4xl flex-col overflow-hidden p-4 sm:p-6">
		<DialogHeader>
			<div class="flex items-center justify-between">
				<div class="flex items-center gap-3">
					{#if selectedMod}
						<CarbonButton
							kind="ghost"
							size="sm"
							onclick={() => (selectedMod = null)}
							class="h-8 w-8 p-0"
						>
							<ArrowLeft class="h-4 w-4" />
						</CarbonButton>
					{/if}
					<div>
						<DialogTitle class="flex items-center gap-2 text-xl font-bold">
							<Boxes class="h-5 w-5 text-primary" />
							{selectedMod
								? selectedMod.title
								: onQueue
									? 'Browse compatible mods'
									: 'Browse & Install Online Mods'}
						</DialogTitle>
						<DialogDescription>
							Loader: <span class="font-semibold text-foreground"
								>{loaderDisplayName(gameLoader)}</span
							>
							{#if filterByVersion && server.mcVersion}
								· Filtering for MC <span class="font-semibold text-foreground"
									>{server.mcVersion}</span
								>
							{:else}
								· <span class="text-muted-foreground">All MC versions</span>
							{/if}
						</DialogDescription>
					</div>
				</div>
				<div class="flex items-center gap-2">
					{#if sessionInstalledCount > 0}
						<button
							type="button"
							class="inline-flex cursor-pointer items-center"
							onclick={() => {
								sessionSummaryOpen = true;
								document.getElementById('session-summary')?.scrollIntoView({ block: 'nearest' });
							}}
							title="View mods added during this session"
						>
							<CarbonTag type="green" size="sm">{sessionInstalledCount} added</CarbonTag>
						</button>
					{/if}
					<Badge variant="outline" class="font-mono text-xs uppercase">
						{loaderSearchName(gameLoader)}
					</Badge>
					{#if server.mcVersion && !onQueue}
						<button
							type="button"
							onclick={() => {
								filterByVersion = !filterByVersion;
								if (filterByVersion) {
									if (!activeFilters.some((f) => f.key === 'version')) {
										activeFilters.push({
											key: 'version',
											value: server.mcVersion,
											removable: true
										});
									}
								} else {
									activeFilters = activeFilters.filter((f) => f.key !== 'version');
								}
								searchMods();
							}}
							class="inline-flex cursor-pointer items-center rounded-md px-2.5 py-0.5 font-mono text-xs font-semibold transition-colors {filterByVersion
								? 'bg-secondary text-secondary-foreground hover:bg-secondary/80'
								: 'border border-dashed bg-muted text-muted-foreground hover:text-foreground'}"
							title="Click to toggle version filter"
						>
							MC {server.mcVersion}
							{filterByVersion ? '✓' : '(any)'}
						</button>
					{/if}
				</div>
			</div>
		</DialogHeader>

		{#if !selectedMod}
			<!-- Search & Platform Bar -->
			<div class="mt-4 flex flex-col gap-3">
				<div class="flex items-center gap-2">
					<Tabs
						value={platform}
						onValueChange={(val) => {
							platform = val as 'modrinth' | 'curseforge';
							searchMods();
						}}
						class="w-auto"
					>
						<TabsList>
							<TabsTrigger value="modrinth" class="px-3">Modrinth</TabsTrigger>
							<TabsTrigger value="curseforge" class="px-3">CurseForge</TabsTrigger>
						</TabsList>
					</Tabs>

					<div class="relative flex-1">
						<Input
							placeholder={`Search ${platform === 'modrinth' ? 'Modrinth' : 'CurseForge'} mods...`}
							bind:value={searchQuery}
							onkeydown={(e) => e.key === 'Enter' && searchMods()}
							class="pl-9"
						/>
						<Search
							class="absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2 text-muted-foreground"
						/>
					</div>

					<CarbonButton onclick={searchMods} disabled={searching}>
						{#if searching}
							<Loader2 class="h-4 w-4 animate-spin" />
						{:else}
							Search
						{/if}
					</CarbonButton>
				</div>

				{#if !onQueue}
					<!-- Interactive Filter Bar -->
					<div class="flex flex-wrap items-center gap-2 border-t border-border/40 pt-1">
						<div class="mr-1 flex items-center text-xs font-semibold text-muted-foreground">
							<Filter class="mr-1 h-3.5 w-3.5 text-primary" />
							Filters:
						</div>

						{#each activeFilters as f (f.key)}
							<span
								class="inline-flex items-center gap-1 rounded-md border border-border/50 bg-secondary px-2 py-0.5 text-xs font-medium text-secondary-foreground"
							>
								<span class="font-mono text-xs text-muted-foreground">{f.key}:</span>
								<span class="font-semibold">{f.value}</span>
								{#if f.removable}
									<button
										type="button"
										onclick={() => removeFilter(f.key)}
										class="ml-0.5 cursor-pointer transition-colors hover:text-destructive"
										title={`Remove ${f.key} filter`}
									>
										<X class="h-3 w-3" />
									</button>
								{/if}
							</span>
						{/each}

						<!-- Filter input with auto-suggestions -->
						<div class="relative inline-block">
							<div class="flex items-center">
								<input
									type="text"
									placeholder="+ Add filter (e.g. side:server, category:optimization)..."
									bind:value={filterInput}
									onfocus={() => (filterInputFocused = true)}
									onblur={() => setTimeout(() => (filterInputFocused = false), 200)}
									onkeydown={(e) => {
										if (e.key === 'Enter' && filterInput.trim()) {
											addFilter(filterInput.trim());
										} else if (e.key === 'Escape') {
											filterInputFocused = false;
										}
									}}
									class="h-7 w-72 rounded-md border border-dashed border-border bg-muted/50 px-2 text-xs placeholder:text-muted-foreground/70 focus:border-primary focus:bg-background focus:outline-none"
								/>
							</div>

							{#if filterInputFocused && suggestions.length > 0}
								<div
									class="absolute top-full left-0 z-50 mt-1 w-80 overflow-hidden rounded-lg border bg-popover py-1 text-popover-foreground shadow-lg"
								>
									<div
										class="mb-1 border-b px-2 py-1 text-xs font-semibold tracking-wider text-muted-foreground uppercase"
									>
										Filter Suggestions (type key:value)
									</div>
									{#each suggestions as s (s.label)}
										<button
											type="button"
											onmousedown={() => addFilter(s.label)}
											class="flex w-full cursor-pointer flex-col px-2.5 py-1.5 text-left text-xs transition-colors hover:bg-accent hover:text-accent-foreground"
										>
											<span class="font-mono font-medium text-primary">{s.label}</span>
											<span class="text-xs text-muted-foreground">{s.desc}</span>
										</button>
									{/each}
								</div>
							{/if}
						</div>
					</div>
				{/if}
			</div>

			{#if sessionInstalledCount > 0}
				<!-- Session Summary: mods installed through this dialog session -->
				<div id="session-summary" class="mt-4 border border-border bg-card">
					<button
						type="button"
						class="flex w-full cursor-pointer items-center justify-between gap-2 px-3 py-2 text-left transition-colors hover:bg-secondary"
						onclick={() => (sessionSummaryOpen = !sessionSummaryOpen)}
						aria-expanded={sessionSummaryOpen}
						aria-controls="session-summary-list"
					>
						<span
							class="flex items-center gap-2 text-xs font-semibold tracking-wide text-muted-foreground uppercase"
						>
							Added this session
						</span>
						<span class="flex items-center gap-2">
							<CarbonTag type="green" size="sm">{sessionInstalledCount} added</CarbonTag>
							<ChevronDown
								class="h-3.5 w-3.5 text-muted-foreground transition-transform {sessionSummaryOpen
									? ''
									: 'rotate-180'}"
							/>
						</span>
					</button>
					{#if sessionSummaryOpen}
						<ul id="session-summary-list" class="divide-y divide-border border-t border-border">
							{#each sessionInstalled as entry (entry.key)}
								<li class="flex items-center justify-between gap-3 px-3 py-2 text-xs">
									<span class="flex min-w-0 items-center gap-2">
										<Check class="h-3.5 w-3.5 shrink-0 text-success-text" />
										<span class="truncate font-medium text-foreground">{entry.title}</span>
									</span>
									<span class="flex shrink-0 items-center gap-3 font-mono text-muted-foreground">
										<span>v{entry.version}</span>
										<span>{entry.depCount} {entry.depCount === 1 ? 'dep' : 'deps'}</span>
									</span>
								</li>
							{/each}
						</ul>
					{/if}
				</div>
			{/if}

			<!-- Search Results / Mod List -->
			<div
				class="mt-4 max-h-[500px] min-h-[280px] flex-1 space-y-3 overflow-y-auto pr-1 sm:min-h-[360px]"
			>
				{#if searchError}
					<Alert variant="destructive" class="my-4">
						<AlertTriangle class="h-4 w-4" />
						<AlertTitle>Search Notice</AlertTitle>
						<AlertDescription>{searchError}</AlertDescription>
					</Alert>
				{:else if searching}
					<div class="flex flex-col items-center justify-center py-20 text-muted-foreground">
						<Loader2 class="h-8 w-8 animate-spin text-primary" />
						<p class="mt-3 text-sm">Searching compatible mods for MC {server.mcVersion}...</p>
					</div>
				{:else if displayMods.length === 0}
					<div class="flex flex-col items-center justify-center py-20 text-muted-foreground">
						<Package class="h-10 w-10 stroke-[1.5]" />
						<p class="mt-3 text-base font-medium">No matching mods found</p>
						<p class="text-xs text-muted-foreground">
							{onQueue
								? 'Try another search for this Minecraft version and loader.'
								: 'Try a different query or remove some active filters.'}
						</p>
					</div>
				{:else}
					<div class="grid grid-cols-1 gap-3">
						{#each displayMods as mod (mod.id)}
							<Card class="transition-colors hover:border-primary/50">
								<CardContent class="flex items-start justify-between gap-4 p-4">
									<div class="flex min-w-0 flex-1 items-start gap-3.5">
										{#if mod.icon_url}
											<img
												src={mod.icon_url}
												alt={mod.title}
												class="h-12 w-12 flex-shrink-0 rounded-lg bg-muted/40 object-contain p-1"
											/>
										{:else}
											<div
												class="flex h-12 w-12 flex-shrink-0 items-center justify-center rounded-lg bg-muted"
											>
												<Package class="h-6 w-6 text-muted-foreground" />
											</div>
										{/if}

										<div class="min-w-0 flex-1 space-y-1">
											<div class="flex items-center gap-2">
												<span class="truncate text-base font-semibold text-foreground"
													>{mod.title}</span
												>
												{#if mod.author}
													<span class="truncate text-xs text-muted-foreground">by {mod.author}</span
													>
												{/if}
												{#if sessionInstalled.some((entry) => entry.key === sessionKey(mod))}
													<CarbonTag type="green" size="sm">Added just now</CarbonTag>
												{:else if mod.installed}
													<CarbonTag type="green" size="sm">Installed</CarbonTag>
												{:else if queuedMods.some((entry) => entry.project === sessionKey(mod))}
													<CarbonTag type="teal" size="sm">Selected</CarbonTag>
												{/if}
											</div>
											<p class="line-clamp-2 text-xs text-muted-foreground">{mod.description}</p>

											<div class="flex flex-wrap items-center gap-1.5 pt-1">
												<Badge variant="secondary" class="h-5 px-1.5 text-xs">
													<Download class="mr-1 h-3 w-3" />
													{formatDownloads(mod.downloads)}
												</Badge>
												{#if mod.server_side}
													<Badge
														variant="outline"
														class="h-5 px-1.5 text-xs {mod.server_side === 'required'
															? 'border-primary/60 text-primary'
															: 'text-muted-foreground'}"
													>
														Server: {mod.server_side}
													</Badge>
												{/if}
												{#if mod.client_side}
													<Badge variant="outline" class="h-5 px-1.5 text-xs text-muted-foreground">
														Client: {mod.client_side}
													</Badge>
												{/if}
												{#each (mod.categories || []).slice(0, 3) as cat (cat)}
													<Badge variant="outline" class="h-5 px-1.5 text-xs capitalize">
														{cat}
													</Badge>
												{/each}
											</div>
										</div>
									</div>

									<div class="flex flex-shrink-0 flex-col items-end gap-2">
										<CarbonButton size="sm" onclick={() => viewModVersions(mod)}
											>Select Version</CarbonButton
										>
									</div>
								</CardContent>
							</Card>
						{/each}
					</div>
				{/if}
			</div>
		{:else}
			<!-- Mod Versions & Install View -->
			<div
				class="mt-4 max-h-[500px] min-h-[280px] flex-1 space-y-4 overflow-y-auto pr-1 sm:min-h-[360px]"
			>
				<div class="space-y-2 rounded-lg border bg-muted/20 p-4">
					<div class="flex items-center justify-between">
						<span class="text-base font-semibold">{selectedMod.title}</span>
						<Badge variant="outline">{selectedMod.platform.toUpperCase()}</Badge>
					</div>
					<p class="text-sm text-muted-foreground">{selectedMod.description}</p>
				</div>

				{#if loadingVersions}
					<div class="flex flex-col items-center justify-center py-16 text-muted-foreground">
						<Loader2 class="h-8 w-8 animate-spin text-primary" />
						<p class="mt-3 text-sm">Fetching compatible versions and dependencies...</p>
					</div>
				{:else if versions.length === 0}
					<Alert variant="destructive">
						<AlertTriangle class="h-4 w-4" />
						<AlertTitle>No Compatible Versions</AlertTitle>
						<AlertDescription>
							No versions were found matching Minecraft {server.mcVersion} and {loaderSearchName(
								gameLoader
							)}.
						</AlertDescription>
					</Alert>
				{:else}
					<div class="space-y-3">
						<label class="text-sm font-medium"
							>Compatible Versions ({versions.length} available)</label
						>
						<div class="max-h-48 space-y-2 overflow-y-auto rounded-lg border p-2">
							{#each versions as ver (ver.id)}
								<div
									role="button"
									tabindex="0"
									onclick={() => selectVersion(ver)}
									onkeydown={(e) => e.key === 'Enter' && selectVersion(ver)}
									class="flex cursor-pointer items-center justify-between rounded-md p-2 text-sm transition-colors
										{selectedVersion?.id === ver.id
										? 'border border-primary/40 bg-primary/15 font-medium'
										: 'hover:bg-muted/50'}"
								>
									<div class="flex items-center gap-2">
										<span class="flex w-4 shrink-0 items-center justify-center">
											{#if selectedVersion?.id === ver.id}
												<Check class="h-4 w-4 text-primary" />
											{/if}
										</span>
										<span>{ver.version_number || ver.name}</span>
										<Badge
											variant={ver.version_type === 'release' ? 'default' : 'secondary'}
											class="h-4 px-1 text-xs capitalize"
										>
											{ver.version_type}
										</Badge>
									</div>
									<div class="flex items-center gap-2 font-mono text-xs text-muted-foreground">
										<span>{ver.file_name}</span>
										<span>({formatBytes(ver.file_size)})</span>
									</div>
								</div>
							{/each}
						</div>
					</div>

					<!-- Dependencies Section -->
					{#if selectedVersion && (selectedVersion.dependencies || []).length > 0}
						<div class="space-y-2 rounded-lg border border-primary/30 bg-primary/5 p-4">
							<div class="flex items-center gap-2 text-sm font-semibold text-foreground">
								<Layers class="h-4 w-4 text-primary" />
								Dependencies
							</div>
							<p class="text-xs text-muted-foreground">
								{onQueue
									? 'Required dependencies are included and checked before the mod is added.'
									: 'Select dependencies to install together with this mod. Required dependencies are pre-selected.'}
							</p>
							<div class="space-y-1.5 pt-1">
								{#each selectedVersion.dependencies as dep (dep.project_id ?? dep.title)}
									{#if dep.project_id && dep.dependency_type.toLowerCase() !== 'incompatible'}
										<label class="flex cursor-pointer items-center gap-2 text-xs">
											<input
												type="checkbox"
												checked={!!selectedDependencies[dep.project_id]}
												disabled={Boolean(onQueue)}
												onchange={(e) => {
													selectedDependencies[dep.project_id] = (
														e.target as HTMLInputElement
													).checked;
												}}
												class="h-3.5 w-3.5 accent-primary"
											/>
											<Badge variant="outline" class="font-mono text-xs">
												{dep.dependency_type}
											</Badge>
											<span class="font-medium text-foreground">{dep.title || dep.project_id}</span>
										</label>
									{:else}
										<div class="flex items-center gap-2 text-xs text-muted-foreground">
											<Badge variant="outline" class="font-mono text-xs">
												{dep.dependency_type}
											</Badge>
											<span
												>{dep.title ||
													dep.project_id ||
													dep.version_id ||
													'Dependency identity unavailable'}</span
											>
										</div>
									{/if}
								{/each}
							</div>
						</div>
					{/if}

					<div class="flex justify-end gap-3 pt-3">
						<CarbonButton kind="tertiary" onclick={() => (selectedMod = null)}>Cancel</CarbonButton>
						<CarbonButton onclick={installMod} disabled={installing || !selectedVersion}>
							{#if installing}
								<Loader2 class="mr-2 h-4 w-4 animate-spin" />
								{onQueue ? 'Checking dependencies…' : 'Installing to /mods…'}
							{:else}
								<Download class="mr-2 h-4 w-4" />
								{installLabel}
							{/if}
						</CarbonButton>
					</div>
				{/if}
			</div>
		{/if}
	</DialogContent>
</DialogPrimitive.Root>
