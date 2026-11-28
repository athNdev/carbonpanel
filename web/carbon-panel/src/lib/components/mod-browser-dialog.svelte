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
	import { CarbonButton, CarbonInlineLoading, CarbonTag } from '$lib/components/carbon';
	import {
		Download,
		Package,
		ArrowLeft,
		Boxes,
		AlertTriangle,
		Layers,
		Filter,
		Check,
		ChevronDown
	} from '@lucide/svelte';
	import { toast } from 'svelte-sonner';
	import type { Server } from '$lib/proto/carbonpanel/v1/common_pb';
	import { formatBytes } from '$lib/utils';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { resolveWizardMods, type QueuedServerMod } from '$lib/features/servers/create-content';
	import {
		createModsBrowserStore,
		type ModsBrowserPorts,
		type ModsBrowserStore,
		type ModsCatalogPlatform,
		type ModsCatalogResult,
		type ModsCatalogVersion
	} from '$lib/mods/mods-browser-store.svelte';
	import type {
		BrowseModResult as RpcBrowseMod,
		ModVersion as RpcModVersion,
		ModDependency as RpcModDependency
	} from '$lib/proto/carbonpanel/v1/mod_browse_pb';
	import {
		MAX_DEPENDENCY_DEPTH,
		sameModInstallContext,
		type DependencyPlanResult,
		type InstallItem,
		type ModInstallContext,
		type PlanDependency
	} from '$lib/mods/dependency-plan';
	import ModsCatalogSearchBar from '$lib/mods/ModsCatalogSearchBar.svelte';
	import ModsCatalogResultCard from '$lib/mods/ModsCatalogResultCard.svelte';
	import ModsInstallOutcomeList from '$lib/mods/ModsInstallOutcomeList.svelte';
	import {
		buildFilterVocabulary,
		matchFilterSuggestions,
		type FilterSuggestion
	} from '$lib/mods/filter-vocabulary';
	import { contentDirectoryFor, isPluginLoader } from '$lib/mods/loader-directories';

	interface Props {
		open: boolean;
		server: Pick<Server, 'id' | 'mcVersion' | 'modLoader' | 'modpackLoader'>;
		onInstalled?: () => void;
		onQueue?: (mods: QueuedServerMod[]) => void;
		queuedMods?: QueuedServerMod[];
	}

	interface ActiveFilter {
		key: string;
		value: string;
		removable: boolean;
	}

	// The generated contract uses camelCase and int64 (bigint) fields. Map at
	// the transport boundary so the store's render state stays framework-free.
	function toCatalogResult(r: RpcBrowseMod): ModsCatalogResult {
		return {
			id: r.id,
			slug: r.slug,
			title: r.title,
			description: r.description,
			iconUrl: r.iconUrl,
			author: r.author,
			downloads: Number(r.downloads),
			categories: r.categories,
			platform: r.platform,
			clientSide: r.clientSide,
			serverSide: r.serverSide,
			installed: r.installed
		};
	}

	function toPlanDependency(d: RpcModDependency): PlanDependency {
		return {
			project_id: d.projectId,
			version_id: d.versionId,
			dependency_type: d.dependencyType,
			title: d.title || undefined
		};
	}

	function toCatalogVersion(v: RpcModVersion): ModsCatalogVersion {
		return {
			id: v.id,
			version_number: v.versionNumber,
			name: v.name,
			version_type: v.versionType,
			file_name: v.fileName,
			download_url: v.downloadUrl,
			file_size: Number(v.fileSize),
			dependencies: v.dependencies.map(toPlanDependency)
		};
	}

	let { open = $bindable(false), server, onInstalled, onQueue, queuedMods = [] }: Props = $props();

	// Real game loader underneath a modpack platform; plain modLoader otherwise.
	let gameLoader = $derived(effectiveLoader(server.modLoader, server.modpackLoader));
	// Bukkit/Paper family installs into plugins/ and browses Hangar plugins;
	// every other loader installs into mods/.
	let contentDirectory = $derived(contentDirectoryFor(gameLoader));

	// Raw versions are kept beside the mapped ones: the wizard solver consumes
	// the generated proto messages, not the render mapping.
	let rawVersionsById: Record<string, RpcModVersion> = {};

	const ports: ModsBrowserPorts = {
		search: async (request) => {
			const response = await rpcClient.modBrowse.searchMods(
				{
					serverId: request.serverId,
					query: request.query,
					platform: request.platform,
					loader: request.loader,
					mcVersion: request.mcVersion,
					side: request.side ?? ''
				},
				silentCallOptions
			);
			return { results: response.results.map(toCatalogResult) };
		},
		versions: async (request) => {
			const response = await rpcClient.modBrowse.getModVersions(
				{
					serverId: request.serverId,
					project: request.project,
					platform: request.platform,
					loader: request.loader,
					mcVersion: request.mcVersion
				},
				silentCallOptions
			);
			rawVersionsById = {};
			for (const version of response.versions) rawVersionsById[version.id] = version;
			return { versions: response.versions.map(toCatalogVersion) };
		},
		install: async (request) => {
			const data = await rpcClient.modBrowse.installMods(
				{
					serverId: request.serverId,
					items: request.items.map((item) => ({
						name: item.name,
						fileName: item.fileName,
						downloadUrl: item.downloadUrl
					}))
				},
				silentCallOptions
			);
			return {
				success: data.success,
				installed: data.installed,
				errors: data.errors,
				message: data.message
			};
		}
	};

	const store: ModsBrowserStore = createModsBrowserStore(ports);

	// Hangar has no client/server sides, so a plugin loader never sends or
	// suggests a side filter. Mod loaders default to server-compatible.
	function defaultSide(): string | undefined {
		return isPluginLoader(gameLoader) ? undefined : 'server';
	}

	// Filter bar state — key:value chips are per-dialog UI, not machine state.
	function buildDefaultFilters(): ActiveFilter[] {
		return [
			...(isPluginLoader(gameLoader) ? [] : [{ key: 'side', value: 'server', removable: true }]),
			{ key: 'loader', value: loaderSearchName(gameLoader), removable: false },
			...(server.mcVersion ? [{ key: 'version', value: server.mcVersion, removable: true }] : [])
		];
	}
	let activeFilters = $state<ActiveFilter[]>(buildDefaultFilters());
	let filterByVersion = $state(true);
	let filterInput = $state('');
	let filterInputFocused = $state(false);

	// Filter suggestions are built from data: the server's MC version first,
	// and no `side:` entries for plugin loaders (Hangar has no sides).
	const FILTER_VOCABULARY: FilterSuggestion[] = buildFilterVocabulary({
		mcVersion: server.mcVersion,
		pluginLoader: isPluginLoader(gameLoader)
	});

	let suggestions = $derived(matchFilterSuggestions(FILTER_VOCABULARY, filterInput));

	const catalogPlatforms = [
		{ id: 'modrinth', label: 'Modrinth' },
		{ id: 'curseforge', label: 'CurseForge' }
	];

	// Store-backed state the template reads directly.
	let selectedMod = $derived(store.selectedResult);
	let selectedVersion = $derived(store.selectedVersion);
	let selectedDependencies = $derived(store.dependencySelection);
	let versions = $derived(store.versions);
	let loadingVersions = $derived(store.loadingVersions);
	let versionsError = $derived(store.versionsError);
	let searching = $derived(store.searching);
	let searchError = $derived(store.searchError);
	let mods = $derived(store.results);
	let sessionInstalled = $derived(store.sessionInstalled);
	let sessionInstalledCount = $derived(store.sessionCount);
	let sessionSummaryOpen = $derived(store.sessionSummaryOpen);

	function sessionKey(mod: { platform: string; id: string }): string {
		return store.sessionKeyFor(mod);
	}

	let selectedDepIds = $derived(
		Object.keys(selectedDependencies).filter((k) => selectedDependencies[k])
	);

	let installLabel = $derived(
		onQueue
			? 'Add to server setup'
			: selectedDepIds.length > 0
				? 'Install with all dependencies'
				: 'Install'
	);

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
		if (onQueue) return mods.filter((mod) => mod.serverSide !== 'unsupported');
		const sideFilter = activeFilters.find((f) => f.key === 'side')?.value;
		if (sideFilter === 'server') {
			return mods.filter((m) => m.serverSide !== 'unsupported');
		} else if (sideFilter === 'client') {
			return mods.filter((m) => m.clientSide !== 'unsupported');
		}
		return mods;
	});

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
			side: onQueue
				? defaultSide()
				: activeFilters.find((filter) => filter.key === 'side')?.value || ''
		};
	}

	function installContextFor(lookupLoader: string, lookupMcVersion: string): ModInstallContext {
		return {
			serverId: server.id,
			serverMcVersion: server.mcVersion,
			serverLoader: gameLoader,
			lookupLoader,
			lookupMcVersion
		};
	}

	function searchMods() {
		const target = browseTarget();
		void store.submitSearch({
			loader: target.loader,
			mcVersion: target.mcVersion,
			side: target.side
		});
	}

	function viewModVersions(mod: ModsCatalogResult) {
		void store.selectResult(mod);
	}

	function selectVersion(ver: ModsCatalogVersion) {
		store.selectVersion(ver);
	}

	function toggleDependency(projectId: string, checked: boolean) {
		store.toggleDependency(projectId, checked);
	}

	// Dependency resolution delegates to the shared planner; the plan is kept
	// so unresolved requirements surface in the install outcomes (E9) instead
	// of being silently dropped by a provider outage.
	let lastDependencyPlan: DependencyPlanResult | null = null;

	async function resolveDependencyInstallItems(
		platform: string,
		loaderVal: string,
		versionVal: string,
		visited: Set<string>
	): Promise<InstallItem[]> {
		const plan = await store.planDependencies({
			version: store.selectedVersion as ModsCatalogVersion,
			platform,
			loader: loaderVal,
			mcVersion: versionVal,
			isKnownProject: (projectId) =>
				visited.has(`${platform.toLowerCase()}:${projectId.toLowerCase()}`),
			maxDepth: MAX_DEPENDENCY_DEPTH
		});
		lastDependencyPlan = plan;
		return plan.items;
	}

	async function installMod() {
		const mod = store.selectedResult;
		if (!mod || !selectedVersion) return;

		if (onQueue) {
			store.beginInstall();
			try {
				const version = rawVersionsById[selectedVersion.id];
				if (!version) return;
				const { loader, mcVersion } = browseTarget();
				const key = `${mod.platform}:${mod.id}`;
				const selections = queuedMods
					.filter((entry) => entry.project !== key)
					.map((entry) => entry.selection);
				const plan = await resolveWizardMods(
					{ loader, mcVersion },
					[...selections, { id: mod.id, name: mod.title, platform: mod.platform, version }],
					async (target) =>
						(await rpcClient.modBrowse.getModVersions(target, silentCallOptions)).versions
				);
				if (!open || store.selectedResult !== mod || store.selectedVersion?.id !== version.id)
					return;
				onQueue(plan);
				store.closeDetail();
			} catch (error) {
				console.error('Failed to queue mod:', error);
				toast.error(
					error instanceof Error ? error.message : 'Failed to plan this mod for the new server'
				);
			} finally {
				store.endInstall();
			}
			return;
		}

		store.beginInstall();
		// Identity captured before any await: a close/reopen or a re-selection
		// while the RPC is in flight makes this continuation stale. It must
		// then not toast, flip installed flags, close the version view or
		// release the installing flag of a newer install (E7 — the same
		// re-check the wizard path keeps).
		const versionId = selectedVersion.id;
		const staleAfter = (): boolean =>
			!open || store.selectedResult !== mod || store.selectedVersion?.id !== versionId;
		let currentInstall = true;
		try {
			const { loader: loaderVal, mcVersion: versionVal } = browseTarget();
			const installContext = installContextFor(loaderVal, versionVal);
			if (store.target.id !== installContext.serverId) {
				toast.error('The install target changed. Reopen the mod browser and try again.');
				return;
			}
			const platform = mod.platform;

			const items: InstallItem[] = [
				{
					name: mod.title,
					file_name: selectedVersion.file_name,
					download_url: selectedVersion.download_url
				}
			];

			const visited = new Set<string>([
				`${platform.toLowerCase()}:${mod.id.toLowerCase()}`,
				`slug:${(mod.slug || '').toLowerCase()}`
			]);
			const depItems = await resolveDependencyInstallItems(
				platform,
				loaderVal,
				versionVal,
				visited
			);
			if (staleAfter()) {
				currentInstall = false;
				return;
			}
			const currentTarget = browseTarget();
			const currentContext = installContextFor(currentTarget.loader, currentTarget.mcVersion);
			if (
				!sameModInstallContext(installContext, currentContext) ||
				store.target.id !== installContext.serverId
			) {
				toast.error('The server or compatibility context changed. Review the mod and try again.');
				return;
			}
			items.push(...depItems);

			const unresolved = lastDependencyPlan?.unresolved ?? [];
			const response = await store.installServerItems({
				items,
				root: {
					platform: mod.platform,
					id: mod.id,
					title: mod.title,
					version: selectedVersion.version_number || selectedVersion.name,
					depCount: (selectedVersion.dependencies || []).length
				},
				rootFileName: selectedVersion.file_name,
				unresolvedCount: unresolved.length
			});
			if (staleAfter()) {
				currentInstall = false;
				return;
			}
			// Unresolved dependencies become outcome rows, never a silent skip (E9).
			store.appendOutcomes(
				unresolved.map((dep) => ({
					key: `dep:${dep.projectId}`,
					label: dep.title,
					tone: 'yellow' as const,
					tag: 'Unresolved',
					detail: dep.reason
				}))
			);

			if (response.success) {
				toast.success(response.message || `Installed ${mod.title}`);
				mod.installed = true;
				if (onInstalled) {
					onInstalled();
				}
				store.closeDetail();
			} else {
				const detail = response.errors.join('; ');
				toast.error(
					detail ? `${response.message} ${detail}` : response.message || 'Failed to install mod'
				);
			}
		} catch (err) {
			if (staleAfter()) {
				currentInstall = false;
				return;
			}
			console.error('Failed to install mod:', err);
			toast.error(
				(err instanceof Error ? err.message : undefined) || 'Installation request failed'
			);
		} finally {
			// Only the live invocation may release the region: a stale one must
			// not clear installing while a newer install is still in flight.
			if (currentInstall) store.endInstall();
		}
	}

	function resultState(mod: ModsCatalogResult): 'idle' | 'installed' | 'queued' | 'added' {
		if (sessionInstalled.some((entry) => entry.key === sessionKey(mod))) return 'added';
		if (mod.installed) return 'installed';
		if (queuedMods.some((entry) => entry.project === sessionKey(mod))) return 'queued';
		return 'idle';
	}

	// TARGET_CHANGED when the host's server identity changes behind the dialog
	// (E22): installed flags, loader copy and results would otherwise go stale.
	let syncedTargetKey = '';
	$effect(() => {
		const key = `${server.id}|${gameLoader}|${server.mcVersion}|${onQueue ? 'wizard' : 'server'}`;
		if (key === syncedTargetKey) return;
		const rebuildFilters = syncedTargetKey !== '';
		syncedTargetKey = key;
		untrack(() => {
			store.setTarget({
				kind: onQueue ? 'wizard' : 'server',
				id: server.id,
				loader: loaderSearchName(gameLoader),
				mcVersion: server.mcVersion,
				side: defaultSide()
			});
			if (rebuildFilters) {
				activeFilters = buildDefaultFilters();
				if (open) store.openDialog();
			}
		});
	});

	let wasOpen = false;
	$effect(() => {
		if (open && !wasOpen) {
			wasOpen = true;
			untrack(() => {
				store.openDialog();
			});
		} else if (!open && wasOpen) {
			wasOpen = false;
			untrack(() => {
				store.closeDialog();
			});
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
							onclick={() => store.closeDetail()}
							class="h-8 w-8 p-0"
							aria-label="Back to search results"
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
							· Installs to <span class="font-semibold text-foreground">{contentDirectory}/</span>
						</DialogDescription>
					</div>
				</div>
				<div class="flex items-center gap-2">
					{#if sessionInstalledCount > 0}
						<button
							type="button"
							class="inline-flex cursor-pointer items-center"
							onclick={() => {
								store.sessionSummaryOpen = true;
								document.getElementById('session-summary')?.scrollIntoView({ block: 'nearest' });
							}}
							title="View mods added during this session"
						>
							<CarbonTag type="green" size="sm">{sessionInstalledCount} added</CarbonTag>
						</button>
					{/if}
					<CarbonTag type="gray" size="sm">{loaderSearchName(gameLoader)}</CarbonTag>
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
							class="inline-flex cursor-pointer items-center rounded-none px-2.5 py-0.5 font-mono text-xs font-semibold transition-colors {filterByVersion
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
				<ModsCatalogSearchBar
					platforms={catalogPlatforms}
					platform={store.platform}
					onplatformchange={(id) => void store.setPlatform(id as ModsCatalogPlatform)}
					bind:query={store.query}
					{searching}
					placeholder="Search {store.platform === 'modrinth' ? 'Modrinth' : 'CurseForge'} mods..."
					onsubmit={searchMods}
				/>

				{#if !onQueue}
					<!-- Interactive Filter Bar -->
					<div class="flex flex-wrap items-center gap-2 border-t border-border/40 pt-1">
						<div class="mr-1 flex items-center text-xs font-semibold text-muted-foreground">
							<Filter class="mr-1 h-3.5 w-3.5 text-primary" aria-hidden="true" />
							Filters:
						</div>

						{#each activeFilters as f (f.key)}
							<CarbonTag
								type="gray"
								size="sm"
								class="normal-case"
								filter={f.removable}
								onclose={f.removable ? () => removeFilter(f.key) : undefined}
								title={f.removable ? `Remove ${f.key} filter` : undefined}
							>
								<span class="text-muted-foreground">{f.key}:</span>
								<span>{f.value}</span>
							</CarbonTag>
						{/each}

						<!-- Filter input with auto-suggestions -->
						<div class="relative inline-block">
							<div class="flex items-center">
								<input
									type="text"
									placeholder="+ Add filter (e.g. {FILTER_VOCABULARY[0].label})..."
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
									class="h-7 w-72 rounded-none border border-dashed border-border bg-muted/50 px-2 text-xs placeholder:text-muted-foreground/70 focus:border-primary focus:bg-background focus:outline-none"
								/>
							</div>

							{#if filterInputFocused && suggestions.length > 0}
								<div
									class="absolute top-full left-0 z-50 mt-1 w-80 overflow-hidden rounded-none border bg-popover py-1 text-popover-foreground shadow-lg"
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
						onclick={() => (store.sessionSummaryOpen = !sessionSummaryOpen)}
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
										{#if entry.unresolvedCount > 0}
											<CarbonTag type="yellow" size="sm"
												>{entry.unresolvedCount} unresolved</CarbonTag
											>
										{/if}
									</span>
								</li>
							{/each}
						</ul>
					{/if}
				</div>
			{/if}

			<!-- Per-item outcomes of the last install (partial failures, unresolved deps) -->
			{#if store.installOutcomes.length > 0}
				<div class="mt-4 border border-border bg-card p-3">
					<ModsInstallOutcomeList
						rows={store.installOutcomes}
						title="Last install result"
						ariaLabel="Install results"
					/>
				</div>
			{/if}

			<!-- Search Results / Mod List -->
			<div
				class="mt-4 max-h-[500px] min-h-[280px] flex-1 space-y-3 overflow-y-auto pr-1 sm:min-h-[360px]"
			>
				{#if searchError}
					<div
						class="my-4 flex items-start gap-2 border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive-text"
						role="alert"
					>
						<AlertTriangle class="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
						<span>{searchError}</span>
					</div>
				{:else if searching}
					<div class="flex flex-col items-center justify-center py-20 text-muted-foreground">
						<CarbonInlineLoading
							description="Searching compatible mods for MC {server.mcVersion}..."
						/>
					</div>
				{:else if displayMods.length === 0}
					<div class="flex flex-col items-center justify-center py-20 text-muted-foreground">
						<Package class="h-10 w-10 stroke-[1.5]" aria-hidden="true" />
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
							<ModsCatalogResultCard
								result={mod}
								state={resultState(mod)}
								busy={store.installing}
								onselect={() => viewModVersions(mod)}
							/>
						{/each}
					</div>
				{/if}
			</div>
		{:else}
			<!-- Mod Versions & Install View -->
			<div
				class="mt-4 max-h-[500px] min-h-[280px] flex-1 space-y-4 overflow-y-auto pr-1 sm:min-h-[360px]"
			>
				<div class="space-y-2 rounded-none border bg-muted/20 p-4">
					<div class="flex items-center justify-between">
						<span class="text-base font-semibold">{selectedMod.title}</span>
						<CarbonTag type="cyan" size="sm">{selectedMod.platform.toUpperCase()}</CarbonTag>
					</div>
					<p class="text-sm text-muted-foreground">{selectedMod.description}</p>
				</div>

				{#if loadingVersions}
					<div class="flex flex-col items-center justify-center py-16 text-muted-foreground">
						<CarbonInlineLoading description="Fetching compatible versions and dependencies..." />
					</div>
				{:else if versionsError}
					<div
						class="flex items-start gap-2 border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive-text"
						role="alert"
					>
						<AlertTriangle class="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
						<span>{versionsError}</span>
					</div>
				{:else if versions.length === 0}
					<div
						class="flex flex-col gap-1 border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive-text"
						role="alert"
					>
						<p class="font-semibold">No Compatible Versions</p>
						<p class="text-xs">
							No versions were found matching Minecraft {server.mcVersion} and {loaderSearchName(
								gameLoader
							)}.
						</p>
					</div>
				{:else}
					<div class="space-y-3">
						<label class="text-sm font-medium"
							>Compatible Versions ({versions.length} available)</label
						>
						<div class="max-h-48 space-y-2 overflow-y-auto rounded-none border p-2">
							{#each versions as ver (ver.id)}
								<div
									role="button"
									tabindex="0"
									onclick={() => selectVersion(ver)}
									onkeydown={(e) => e.key === 'Enter' && selectVersion(ver)}
									class="flex cursor-pointer items-center justify-between rounded-none p-2 text-sm transition-colors
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
										<CarbonTag
											type={ver.version_type === 'release' ? 'green' : 'gray'}
											size="sm"
											class="capitalize"
										>
											{ver.version_type}
										</CarbonTag>
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
						<div class="space-y-2 rounded-none border border-primary/30 bg-primary/5 p-4">
							<div class="flex items-center gap-2 text-sm font-semibold text-foreground">
								<Layers class="h-4 w-4 text-primary" aria-hidden="true" />
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
													toggleDependency(dep.project_id, (e.target as HTMLInputElement).checked);
												}}
												class="h-3.5 w-3.5 accent-primary"
											/>
											<CarbonTag type="gray" size="sm" class="font-mono normal-case">
												{dep.dependency_type}
											</CarbonTag>
											<span class="font-medium text-foreground">{dep.title || dep.project_id}</span>
										</label>
									{:else}
										<div class="flex items-center gap-2 text-xs text-muted-foreground">
											<CarbonTag type="gray" size="sm" class="font-mono normal-case">
												{dep.dependency_type}
											</CarbonTag>
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

					<!-- Outcomes stay visible for a retry after a partial failure -->
					{#if store.installOutcomes.length > 0}
						<div class="border border-border bg-card p-3">
							<ModsInstallOutcomeList
								rows={store.installOutcomes}
								title="Last install result"
								ariaLabel="Install results"
							/>
						</div>
					{/if}

					<div class="flex justify-end gap-3 pt-3">
						<CarbonButton kind="tertiary" onclick={() => store.closeDetail()}>Cancel</CarbonButton>
						<CarbonButton
							onclick={installMod}
							disabled={store.installing || !selectedVersion}
							loading={store.installing}
						>
							{#if store.installing}
								{onQueue ? 'Checking dependencies…' : `Installing to ${contentDirectory}/…`}
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
