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
	import { page } from '$app/state';
	import { displayPreferences } from '$lib/display-preferences.svelte';

	const { metricsExpanded } = displayPreferences;
	import { onMount, untrack } from 'svelte';
	import { SvelteURL } from 'svelte/reactivity';
	import { rpcClient, wasRpcErrorToasted } from '$lib/api/rpc-client';
	import { triggerStreamDownload } from '$lib/api/stream-download';
	import {
		serverStatusLabel as getStatusDisplayName,
		serverStatusTone as getStatusTagType
	} from '$lib/features/servers/presentation';
	import { serversStore, serverInventory, serverActions } from '$lib/stores/servers';
	import { dirtySections, dirtySectionLabels } from '$lib/stores/dirty-sections.svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import ServerLifecycleActions from '$lib/components/fragments/ServerLifecycleActions.svelte';
	import ServerArchiveExport from '$lib/features/servers/ServerArchiveExport.svelte';
	import ScrollToTop from '$lib/components/scroll-to-top.svelte';
	import { toast } from 'svelte-sonner';
	import {
		ChevronDown,
		RefreshCcw,
		MoreVertical,
		Package,
		Loader2,
		Copy,
		Trash2,
		Cpu,
		Info,
		ArrowLeft,
		HardDrive,
		Terminal,
		Settings,
		Boxes,
		Files,
		ListTodo,
		Radio,
		History,
		Users,
		Archive,
		Database,
		Globe2,
		Snowflake,
		Moon
	} from '@lucide/svelte';
	import {
		DropdownMenu,
		DropdownMenuContent,
		DropdownMenuItem,
		DropdownMenuTrigger
	} from '$lib/components/ui/dropdown-menu';
	import type { Timestamp } from '@bufbuild/protobuf/wkt';
	import { ServerStatus, ModLoader } from '$lib/proto/carbonpanel/v1/common_pb';
	import type { GetServerRoutingResponse } from '$lib/proto/carbonpanel/v1/proxy_pb';

	import { enumToString } from '$lib/utils';
	import { effectiveLoader, isModpackPlatform } from '$lib/utils/mod-loader';
	import { copyToClipboard as copyText } from '$lib/utils/clipboard';
	import {
		CarbonTag,
		CarbonTabs,
		CarbonConfirm,
		CarbonModal,
		CarbonErrorState,
		CarbonFloatingActionBar,
		CarbonPageHeader,
		CarbonSpinner
	} from '$lib/components/carbon';
	import CarbonInactiveTabPanels from '$lib/components/carbon/CarbonInactiveTabPanels.svelte';
	import { joinIsPlayable, joinKindLabel } from '$lib/server-join';
	import { memoryUsagePercent } from '$lib/utils/safe-percent';
	import { memoryLimitCopy, worldDiskCopy } from '$lib/utils/resource-enforcement';
	import ServerConsole from '$lib/components/server-console.svelte';
	import ServerConfiguration from '$lib/components/server-configuration.svelte';
	import ServerSettings from '$lib/components/server-settings.svelte';
	import ServerMods from '$lib/components/server-mods.svelte';
	import ServerDatapacks from '$lib/components/server-datapacks.svelte';
	import ServerFiles from '$lib/components/files/server-files.svelte';
	import ServerRouting from '$lib/components/server-routing.svelte';
	import ServerTasks from '$lib/components/server-tasks.svelte';
	import ServerBackups from '$lib/components/server-backups.svelte';
	import ServerActivity from '$lib/components/server-activity.svelte';
	import ServerOperations from '$lib/components/server-operations.svelte';
	import ServerPlayers from '$lib/components/server-players.svelte';
	import ServerModules from '$lib/components/server-ui/ServerModules.svelte';
	import ServerWorld from '$lib/components/server-ui/ServerWorld.svelte';
	import ServerTabPanel from '$lib/components/server-ui/ServerTabPanel.svelte';
	import ServerRuntimeStatus from '$lib/components/server-ui/ServerRuntimeStatus.svelte';
	import WorldViewWorkspace from '$lib/components/worldview/WorldViewWorkspace.svelte';

	let serverId = $derived(page.params.id ?? '');
	let server = $derived($serversStore.find((entry) => entry.id === serverId) ?? null);
	let loading = $state(true);
	let loadError = $state<string | null>(null);
	let actionLoading = $state(false);
	const subViewTabs = [
		{ id: 'overview', label: 'Overview', icon: Settings },
		{ id: 'console', label: 'Console', icon: Terminal },
		{ id: 'configuration', label: 'Configuration', icon: HardDrive },
		{ id: 'mods', label: 'Mods', icon: Package },
		{ id: 'datapacks', label: 'Datapacks', icon: Database },
		{ id: 'world', label: 'World', icon: Globe2 },
		{ id: 'modules', label: 'Modules', icon: Boxes },
		{ id: 'files', label: 'Files', icon: Files },
		{ id: 'tasks', label: 'Tasks', icon: ListTodo },
		{ id: 'backups', label: 'Backups', icon: Archive },
		{ id: 'players', label: 'Players', icon: Users },
		{ id: 'activity', label: 'Activity', icon: History },
		{ id: 'routing', label: 'Routing', icon: Radio }
	];
	let detailUrl = $derived(page.shallow?.url ?? page.url);
	let activeTab = $derived(
		subViewTabs.find((tab) => tab.id === detailUrl.searchParams.get('tab'))?.id ?? 'overview'
	);

	// OSS-464: the editable sections (Overview → Server Settings, Configuration,
	// Routing) stay mounted once visited so unsaved edits — and their registry
	// entries — survive tab switches. Deep links start with their target
	// already visited.
	const persistentTabs: ReadonlySet<string> = new Set(['overview', 'configuration', 'routing']);
	// The on-demand placeholder panels only cover the tabs that mount on demand.
	const transientTabs = subViewTabs.filter((tab) => !persistentTabs.has(tab.id));
	let visitedOverview = $state(activeTab === 'overview');
	let visitedConfiguration = $state(activeTab === 'configuration');
	let visitedRouting = $state(activeTab === 'routing');
	$effect(() => {
		if (activeTab === 'overview') visitedOverview = true;
		else if (activeTab === 'configuration') visitedConfiguration = true;
		else if (activeTab === 'routing') visitedRouting = true;
	});

	// Older copied Configuration links contained only a category or property fragment.
	// Confirm it against this server's catalog before changing the default tab.
	$effect(() => {
		const hash = window.location.hash.slice(1);
		if (!serverId || detailUrl.searchParams.has('tab') || !hash || hash === 'carbon-main') return;
		let current = true;
		void rpcClient.config
			.getServerConfig({ serverId })
			.then(({ categories }) => {
				if (!current) return;
				const known = categories.some(
					(category) =>
						category.name.toLowerCase().replace(/\s+/g, '-') === hash ||
						category.properties.some((property) => property.key === hash)
				);
				if (known && window.location.hash.slice(1) === hash) {
					const url = new SvelteURL(window.location.href);
					url.searchParams.set('tab', 'configuration');
					// eslint-disable-next-line svelte/no-navigation-without-resolve
					void goto(url, { shallow: true, replace: true, reset: false });
				}
			})
			.catch(() => {
				/* An unavailable configuration cannot identify a legacy fragment. */
			});
		return () => {
			current = false;
		};
	});

	function selectServerTab(id: string) {
		const url = new SvelteURL(detailUrl.href);
		if (id === 'overview') url.searchParams.delete('tab');
		else url.searchParams.set('tab', id);
		url.hash = '';
		activeTab = id;
		// Keep the selected tab bookmarkable without reloading its server inventory.
		// eslint-disable-next-line svelte/no-navigation-without-resolve
		void goto(url, { shallow: true, replace: true, state: page.state, reset: false });
	}
	let requestedMap = $derived(page.url.searchParams.get('map'));
	let routingInfo = $state<GetServerRoutingResponse | null>(null);
	function openMap(worldKey = '') {
		const url = new URL(detailUrl.href);
		url.searchParams.set('map', worldKey);
		// This opens a dialog on the current server route.
		// eslint-disable-next-line svelte/no-navigation-without-resolve
		void goto(url, { reset: false });
	}

	function closeMap() {
		const url = new URL(detailUrl.href);
		for (const key of ['map', 'dimension', 'x', 'z', 'layer']) url.searchParams.delete(key);
		// Only map-related query parameters are removed.
		// eslint-disable-next-line svelte/no-navigation-without-resolve
		void goto(url, { replaceState: true, reset: false });
	}

	function timestampToDate(timestamp: Timestamp | undefined): Date {
		if (!timestamp) return new Date();
		return new Date(Number(timestamp.seconds) * 1000 + timestamp.nanos / 1_000_000);
	}

	let proxyStatus = $state<{ enabled: boolean; baseUrl: string } | null>(null);

	onMount(() => {
		rpcClient.proxy
			.getProxyStatus({})
			.then((res) => {
				proxyStatus = { enabled: res.enabled, baseUrl: res.baseUrl };
			})
			.catch(() => {
				// Proxy might be disabled or unreachable, ignore
			});
	});

	const isProxied = $derived(
		Boolean(proxyStatus?.enabled && server?.proxyHostname && (server?.proxyPort ?? 0) > 0)
	);

	const connectAddress = $derived.by(() => {
		if (!server) return '';
		if (isProxied) {
			const portSuffix =
				server.proxyPort && server.proxyPort !== 25565 ? `:${server.proxyPort}` : '';
			return `${server.proxyHostname}${portSuffix}`;
		}
		const host =
			typeof window !== 'undefined' && window.location.hostname
				? window.location.hostname
				: 'localhost';
		return `${host}:${server.port}`;
	});

	$effect(() => {
		const id = serverId;
		if (!id) return;
		untrack(() => {
			loading = true;
			loadError = null;
			void loadServer(true);
		});
		return serverInventory.watch({ id });
	});

	$effect(() => {
		if (server) {
			loading = false;
			loadError = null;
		}
	});

	async function loadServer(skipLoading = false) {
		if (!serverId) return;
		const requestedId = serverId;
		const generation = serverInventory.generation();
		if (!skipLoading) {
			loading = true;
			loadError = null;
		}
		try {
			const loaded = await serverInventory.load(requestedId, skipLoading);
			if (serverId !== requestedId || generation !== serverInventory.generation()) return;
			if (loaded) {
				loadError = null;
				loading = false;
			} else {
				// A successful call with no server means it was deleted or moved;
				// stop the spinner instead of leaving the page loading forever.
				loading = false;
				loadError = 'This server no longer exists or was moved.';
				if (!skipLoading) {
					toast.error('Server not found');
				}
			}
		} catch (error) {
			if (serverId !== requestedId || generation !== serverInventory.generation()) return;
			loading = false;

			if (!server) {
				// First load failed: stop the spinner and report (unless the RPC
				// interceptor already toasted).
				loading = false;
				loadError = error instanceof Error ? error.message : 'Failed to load server';
				if (!wasRpcErrorToasted(error)) {
					toast.error('Failed to load server');
				}
			} else {
				// Background poll failure while data is on screen: keep the last
				// known values and log rather than spamming toasts every 5s.
				console.debug('Server status poll failed:', error);
			}
		}
	}

	/**
	 * OSS-464: Start, Restart and Force Recreate are refused while any
	 * registered section holds unsaved edits — the floating action bar shakes
	 * and a toast names the sections. Stop and Delete are deliberately not
	 * gated: they neither consume nor destroy unsaved edits (see
	 * docs/plans/live-terrain-and-save-flow.md, Part B3).
	 */
	const gatedServerActions: ReadonlySet<string> = new Set(['start', 'restart', 'recreate']);

	function refuseGatedAction(
		action: 'start' | 'stop' | 'restart' | 'recreate' | 'pause' | 'deepsleep'
	): boolean {
		const labels = dirtySectionLabels();
		if (labels.length === 0) return true;
		dirtySections.requestShake();
		const verb =
			action === 'start' ? 'starting' : action === 'restart' ? 'restarting' : 'recreating';
		toast.error(
			labels.length === 1
				? `Save or undo your changes in ${labels[0]} before ${verb} the server.`
				: `Unsaved changes in: ${labels.join(', ')}. Save or undo before ${verb} the server.`
		);
		return false;
	}

	async function handleServerAction(
		action: 'start' | 'stop' | 'restart' | 'recreate' | 'pause' | 'deepsleep'
	) {
		if (!server) return;

		if (gatedServerActions.has(action) && !refuseGatedAction(action)) return;

		actionLoading = true;

		const target = server;
		try {
			const outcome = await serverActions.perform(target.id, action);
			if (outcome.kind === 'superseded') return;
			toast.success(
				action === 'recreate'
					? 'Server is recreating...'
					: action === 'stop'
						? 'Server is stopping...'
						: action === 'restart'
							? 'Server is restarting...'
							: action === 'pause'
								? 'Server is freezing...'
								: action === 'deepsleep'
									? 'Server is going into deep sleep...'
									: 'Server is starting...'
			);
		} catch (error) {
			toast.error(
				`Failed to ${action} server: ${error instanceof Error ? error.message : 'Unknown error'}`
			);
			await loadServer(true);
		} finally {
			actionLoading = false;
		}
	}

	let confirmDeleteOpen = $state(false);
	let deleteDisposition = $state<'delete' | 'keep' | 'detach' | 'export'>('delete');

	async function handleDeleteServer() {
		if (!server) return;
		deleteDisposition = 'delete';
		confirmDeleteOpen = true;
	}

	async function confirmDeleteServer() {
		if (!server) return;
		actionLoading = true;
		const disposition = deleteDisposition;
		const target = server;
		try {
			const outcome = await serverActions.perform(target.id, 'delete', disposition);
			if (outcome.kind === 'superseded') return;
			if (outcome.kind === 'exported') {
				triggerStreamDownload(outcome.sessionId, outcome.filename);
				toast.success('World exported. The server was left in place.');
				return;
			}
			toast.success(
				disposition === 'detach'
					? 'Server detached. The container and world files were left in place.'
					: disposition === 'keep'
						? 'Server removed. World files were kept.'
						: 'Server deleted successfully'
			);
			if (serverId === target.id) goto(resolve('/servers'));
		} catch (error) {
			toast.error(
				`Failed to ${disposition === 'export' ? 'export' : 'delete'} server: ${error instanceof Error ? error.message : 'Unknown error'}`
			);
		} finally {
			actionLoading = false;
		}
	}

	function copyConnection(text: string) {
		copyText(text);
		toast.success('Address copied to clipboard');
	}
</script>

<!--
	Shared render-failure UI for every tab boundary. The persistent editable
	panels (OSS-464) each own a boundary so a throw in a hidden section can
	never blank the visible tab.
-->
{#snippet tabFailed(error: unknown, reset: () => void)}
	<div
		class="flex min-h-64 flex-col items-center justify-center gap-3 border border-border bg-card p-6 text-center"
		role="alert"
	>
		<p class="font-mono text-sm text-destructive-text">This tab failed to render</p>
		<p class="max-w-lg text-xs break-words text-muted-foreground">
			{error instanceof Error && error.message ? error.message : String(error)}
		</p>
		<div class="mt-1 flex items-center gap-2">
			<button
				type="button"
				onclick={reset}
				class="flex h-8 cursor-pointer items-center border border-primary bg-primary px-4 font-sans text-xs text-primary-foreground transition-colors hover:bg-[var(--button-primary-hover)]"
			>
				Retry
			</button>
			<a
				href={resolve('/servers')}
				class="flex h-8 items-center border border-border bg-card px-4 font-sans text-xs text-foreground transition-colors hover:bg-accent"
			>
				Back to servers
			</a>
		</div>
	</div>
{/snippet}

{#if loading && !server}
	<div class="flex h-96 flex-col items-center justify-center gap-6 bg-background">
		<CarbonSpinner size="lg" label="Loading server" description="Connecting to server daemon..." />
		<!-- Skeleton tiles mirror the runtime tile grid below so it does not pop in. -->
		<div
			class="grid w-full grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]"
			aria-hidden="true"
		>
			<div class="motion-skeleton h-[140px] border border-border"></div>
			<div class="motion-skeleton h-[140px] border border-border"></div>
			<div class="motion-skeleton h-[140px] border border-border"></div>
			<div class="motion-skeleton h-[140px] border border-border"></div>
		</div>
	</div>
{:else if server}
	<div
		class="flex min-h-0 flex-1 flex-col gap-6 bg-background font-sans text-foreground"
		data-testid="showcase-server-detail"
	>
		<!-- Carbon Header & Server Management Actions -->
		<CarbonPageHeader
			title={server.name}
			description={`${server.description || 'Minecraft Server Instance'} • Created ${timestampToDate(
				server.createdAt
			).toLocaleDateString()}`}
		>
			{#snippet tag()}
				{#if server}
					<!-- Status Badge (Carbon Tag Requirement) -->
					<CarbonTag type={getStatusTagType(server.status)} size="md">
						{getStatusDisplayName(server.status)}
					</CarbonTag>
					{#if server.nodeId}
						<CarbonTag type="blue" size="sm" title={server.nodeId}>
							NODE: {server.nodeId.split('-')[0]}
						</CarbonTag>
					{/if}
				{/if}
			{/snippet}
			{#snippet actions()}
				<a
					href={resolve('/servers')}
					class="flex h-10 w-10 shrink-0 items-center justify-center rounded-none border border-border bg-card text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
					title="Back to Servers"
					aria-label="Back to Servers"
				>
					<ArrowLeft class="h-4 w-4" />
				</a>

				<!-- Sharp Action Buttons -->
				{#if server}
					<div class="flex flex-wrap items-center gap-2">
						<ServerLifecycleActions
							status={server.status}
							hasContainer={!!server.containerId}
							{actionLoading}
							onaction={handleServerAction}
						/>

						<ServerArchiveExport {server} disabled={actionLoading} />

						<!-- Dropdown for recreate and delete -->
						<DropdownMenu>
							<DropdownMenuTrigger>
								{#snippet child({ props })}
									<button
										type="button"
										{...props}
										disabled={actionLoading}
										aria-label="More server actions"
										class="flex h-10 w-10 cursor-pointer items-center justify-center rounded-none border border-border bg-card text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
									>
										<MoreVertical class="h-4 w-4" />
									</button>
								{/snippet}
							</DropdownMenuTrigger>
							<DropdownMenuContent
								align="end"
								class="rounded-none border border-border bg-background p-1 text-foreground"
							>
								<DropdownMenuItem
									class="flex cursor-pointer items-center gap-2 rounded-none px-3 py-2 text-xs text-foreground hover:bg-accent"
									onclick={() => copyConnection(connectAddress)}
								>
									<Copy class="h-3.5 w-3.5" />
									<span>Copy Address</span>
								</DropdownMenuItem>
								<DropdownMenuItem
									class="flex cursor-pointer items-center gap-2 rounded-none px-3 py-2 text-xs text-foreground hover:bg-accent"
									onclick={() => handleServerAction('recreate')}
								>
									<RefreshCcw class="h-3.5 w-3.5" />
									<span>Force Recreate</span>
								</DropdownMenuItem>
								{#if server.status === ServerStatus.RUNNING}
									<DropdownMenuItem
										class="flex cursor-pointer items-center gap-2 rounded-none px-3 py-2 text-xs text-foreground hover:bg-accent"
										onclick={() => handleServerAction('pause')}
									>
										<Snowflake class="h-3.5 w-3.5" />
										<span>Freeze Server</span>
									</DropdownMenuItem>
									<DropdownMenuItem
										class="flex cursor-pointer items-center gap-2 rounded-none px-3 py-2 text-xs text-foreground hover:bg-accent"
										onclick={() => handleServerAction('deepsleep')}
									>
										<Moon class="h-3.5 w-3.5" />
										<span>Deep Sleep</span>
									</DropdownMenuItem>
								{/if}
								<DropdownMenuItem
									class="flex cursor-pointer items-center gap-2 rounded-none px-3 py-2 text-xs text-destructive-text hover:bg-destructive/20"
									onclick={() => handleDeleteServer()}
								>
									<Trash2 class="h-3.5 w-3.5" />
									<span>Delete Server</span>
								</DropdownMenuItem>
							</DropdownMenuContent>
						</DropdownMenu>
					</div>
				{/if}
			{/snippet}
		</CarbonPageHeader>

		<div>
			<button
				type="button"
				class="focus-ring inline-flex min-h-8 items-center gap-2 text-sm font-semibold text-foreground hover:text-accent-text"
				aria-expanded={$metricsExpanded}
				aria-controls="server-runtime-metrics"
				onclick={() => displayPreferences.setMetricsExpanded(!$metricsExpanded)}
			>
				<ChevronDown aria-hidden="true" class={`h-4 w-4 ${$metricsExpanded ? '' : '-rotate-90'}`} />
				Runtime metrics
			</button>
		</div>
		<div
			id="server-runtime-metrics"
			hidden={!$metricsExpanded}
			class="motion-stagger grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-[minmax(0,2fr)_minmax(0,3fr)] [&>*]:min-w-0"
			data-testid="showcase-runtime-tiles"
		>
			{#key server.id}
				<ServerRuntimeStatus
					status={server.status}
					statusReason={server.statusReason}
					proxied={isProxied}
				/>
			{/key}

			<!-- Current world -->
			<WorldViewWorkspace
				{serverId}
				presentation="preview"
				enabled={$metricsExpanded && requestedMap === null}
				onexpand={() => openMap()}
			/>

			<!-- Tile 3: Engine Details -->
			<div class="flex flex-col justify-between border border-border bg-card p-4 sm:p-5">
				<div class="flex items-center justify-between">
					<h2 class="text-sm font-semibold">Engine specs</h2>
					<Info class="h-4 w-4 text-[var(--chart-4)]" />
				</div>
				<dl class="mt-3 divide-y divide-border text-xs">
					<div class="flex items-center justify-between gap-3 py-2">
						<dt class="font-mono text-xs text-muted-foreground uppercase">Minecraft</dt>
						<dd class="text-right font-mono font-medium text-foreground">
							{server.mcVersion || 'Latest'}
						</dd>
					</div>
					{#if isModpackPlatform(server.modLoader)}
						<div class="flex items-center justify-between gap-3 py-2">
							<dt class="font-mono text-xs text-muted-foreground uppercase">Modpack</dt>
							<dd class="text-right font-mono text-foreground">
								{enumToString(ModLoader, server.modLoader)}
							</dd>
						</div>
					{/if}
					<div class="flex items-center justify-between gap-3 py-2">
						<dt class="font-mono text-xs text-muted-foreground uppercase">Loader</dt>
						<dd class="flex flex-wrap items-center justify-end gap-1.5 text-right">
							<CarbonTag type="teal" size="sm"
								>{enumToString(
									ModLoader,
									effectiveLoader(server.modLoader, server.modpackLoader)
								) || 'vanilla'}</CarbonTag
							>
							{#if server.loaderVersion}<span class="font-mono break-all text-foreground"
									>{server.loaderVersion}</span
								>{/if}
						</dd>
					</div>
					<div class="flex items-center justify-between gap-3 py-2">
						<dt class="font-mono text-xs text-muted-foreground uppercase">Java</dt>
						<dd class="text-right font-mono text-foreground">
							{#if server.javaVersion}
								JDK {server.javaVersion}
							{:else}
								<span class="text-muted-foreground">—</span>
							{/if}
						</dd>
					</div>
					<div class="flex items-center justify-between gap-3 py-2">
						<dt class="font-mono text-xs text-muted-foreground uppercase">Port</dt>
						<dd class="text-right font-mono text-foreground">{server.port}</dd>
					</div>
					<div class="flex items-center justify-between gap-3 py-2">
						<dt class="font-mono text-xs text-muted-foreground uppercase">Max players</dt>
						<dd class="text-right font-mono text-foreground">{server.maxPlayers}</dd>
					</div>
				</dl>
			</div>

			<!-- Tile 4: Hardware Quotas -->
			<div class="flex flex-col justify-between border border-border bg-card p-4 sm:p-5">
				<div class="flex items-center justify-between">
					<h2 class="text-sm font-semibold">Resource telemetry</h2>
					<Cpu class="h-4 w-4 text-destructive-text" />
				</div>
				<div class="mt-3 space-y-2.5">
					<!-- Memory Bar -->
					<div>
						<div class="flex flex-wrap items-baseline gap-x-2 font-mono text-xs">
							<span class="text-muted-foreground">RAM</span>
							<span class="text-foreground">
								{#if server.status === ServerStatus.CREATING}
									{memoryLimitCopy(server.resourceEnforcement).label}: {(
										server.memory / 1024
									).toFixed(1)} GB
								{:else}
									{server.memoryUsage ? (Number(server.memoryUsage) / 1024).toFixed(1) : '—'} / {(
										server.memory / 1024
									).toFixed(1)} GB
								{/if}
							</span>
						</div>
						<div class="telemetry-track mt-1.5" aria-hidden="true">
							<div
								class="h-full bg-accent-text transition-[width]"
								style="width: {server.status === ServerStatus.CREATING
									? '100'
									: memoryUsagePercent(server.memoryUsage, server.memory)}%"
							></div>
						</div>
					</div>

					<!-- CPU Bar -->
					<div>
						<div class="flex flex-wrap items-baseline gap-x-2 font-mono text-xs">
							<span class="text-muted-foreground">CPU</span>
							<span class="text-accent-text">
								{server.cpuPercent !== undefined ? `${server.cpuPercent.toFixed(1)}%` : '—'}
							</span>
						</div>
						<div class="telemetry-track mt-1.5" aria-hidden="true">
							<div
								class="h-full bg-[var(--chart-4)] transition-[width]"
								style="width: {Math.min(server.cpuPercent || 0, 100)}%"
							></div>
						</div>
					</div>
				</div>

				<p class="mt-2 font-sans text-[11px] text-muted-foreground">
					{memoryLimitCopy(server.resourceEnforcement).label}
					· {worldDiskCopy(server.resourceEnforcement).label}
				</p>
				<dl
					class="mt-3 flex flex-wrap items-start gap-x-6 gap-y-2 border-t border-border pt-3 text-xs"
				>
					<div class="flex items-baseline gap-2">
						<dt class="text-muted-foreground">TPS</dt>
						<dd
							class="font-mono font-semibold {server.status === ServerStatus.RUNNING &&
							server.tps > 0
								? server.tps >= 18
									? 'text-success-text'
									: 'text-warning-text'
								: ''}"
						>
							{server.status === ServerStatus.RUNNING && server.tps > 0
								? server.tps.toFixed(1)
								: '—'}
						</dd>
					</div>
					<div class="flex items-baseline gap-2">
						<dt class="text-muted-foreground">Players</dt>
						<dd class="font-medium {joinIsPlayable(server) ? 'text-success-text' : ''}">
							{joinIsPlayable(server)
								? server.playersOnline
								: joinKindLabel(server.joinObservationKind)}
						</dd>
					</div>
				</dl>
				{#if server.joinObservationKind === 'stale'}
					<p class="mt-2 text-xs text-muted-foreground">
						The last server reading is too old to confirm the current player count.
					</p>
				{/if}
			</div>
		</div>

		<!-- Startup & Creation Progress Banner (MINE-125 / MINE-128) -->
		{#if server.status === ServerStatus.STARTING}
			<div
				class="flex flex-col items-start justify-between gap-3 border border-l-4 border-border border-l-primary bg-background p-4 sm:flex-row sm:items-center"
			>
				<div class="flex items-center gap-3">
					<Loader2 class="h-5 w-5 shrink-0 animate-spin text-accent-text" />
					<div>
						<p class="text-sm font-semibold text-foreground">Server is starting up</p>
						<p class="mt-0.5 text-xs text-muted-foreground">
							First-boot modpack downloads (manifests, loader installers, mod jars) can take 2–5
							minutes. Monitor real-time logs in the Console tab.
						</p>
					</div>
				</div>
				<button
					type="button"
					onclick={() => selectServerTab('console')}
					class="flex h-8 shrink-0 cursor-pointer items-center gap-1.5 border border-border bg-card px-3 font-mono text-xs text-accent-text transition-colors hover:bg-accent"
				>
					<Terminal class="h-3.5 w-3.5" />
					<span>Open Console</span>
				</button>
			</div>
		{:else if server.status === ServerStatus.CREATING}
			<div
				class="flex items-center gap-3 border border-l-4 border-border border-l-primary bg-background p-4"
			>
				<Loader2 class="h-5 w-5 shrink-0 animate-spin text-accent-text" />
				<div>
					<p class="text-sm font-semibold text-foreground">Creating server environment</p>
					<p class="mt-0.5 text-xs text-muted-foreground">
						Checking Docker image and allocating filesystem storage...
					</p>
				</div>
			</div>
		{/if}

		<!-- Carbon Tabs for Sub-Views with bottom blue line indicator (Requirement 4) -->
		<!-- Keep a usable tab viewport when stacked runtime cards fill the screen.
		     The mods tab carries a status line and a wrapped action row; on a
		     phone that leaves the installed-mod list too short, so its floor is
		     raised and the page scrolls instead of squeezing the list. -->
		<div
			class="flex min-h-[28rem] flex-1 flex-col gap-4 {activeTab === 'mods'
				? 'max-sm:min-h-[35rem]'
				: ''}"
		>
			<!-- Tab Bar -->
			<CarbonTabs
				tabs={subViewTabs}
				selectedTab={activeTab}
				onselect={selectServerTab}
				idPrefix="server-detail-tabs"
				class="sticky top-0 z-20 shrink-0 bg-background"
			/>

			<!-- Tab Content Areas -->
			<CarbonInactiveTabPanels
				tabs={transientTabs}
				selectedTab={activeTab}
				idPrefix="server-detail-tabs"
			/>

			<!--
					OSS-464: CarbonInactiveTabPanels only covers the transient tabs, so
					a persistent tab that has never been visited would leave its tab
					button's aria-controls pointing at a nonexistent panel id. Inert
					placeholder panels hold the id until the keep-mounted panel below
					takes it over on first visit.
				-->
			{#if !visitedOverview}
				<div
					id="server-detail-tabs-panel-overview"
					role="tabpanel"
					aria-labelledby="server-detail-tabs-tab-overview"
					hidden
				></div>
			{/if}
			{#if !visitedConfiguration}
				<div
					id="server-detail-tabs-panel-configuration"
					role="tabpanel"
					aria-labelledby="server-detail-tabs-tab-configuration"
					hidden
				></div>
			{/if}
			{#if !visitedRouting}
				<div
					id="server-detail-tabs-panel-routing"
					role="tabpanel"
					aria-labelledby="server-detail-tabs-tab-routing"
					hidden
				></div>
			{/if}

			<!--
					OSS-464: the editable sections stay mounted once visited so unsaved
					edits — and their dirty-section registry entries — survive tab
					switches. Inactive visits keep them in the DOM behind the hidden
					attribute (Tailwind's preflight forces display: none !important on
					[hidden], which wins over the display utilities below). Each panel
					owns a boundary so a throw in a hidden section cannot blank the
					visible tab; the remaining tabs keep mounting on demand below.
				-->
			{#if visitedOverview}
				<div
					id="server-detail-tabs-panel-overview"
					role="tabpanel"
					aria-labelledby="server-detail-tabs-tab-overview"
					hidden={activeTab !== 'overview'}
					class="focus-ring motion-fade-in flex min-h-0 flex-1 flex-col overflow-hidden"
					tabindex="0"
				>
					<div class="flex h-full min-h-0 flex-1 flex-col">
						<svelte:boundary>
							<ServerTabPanel
								title="Server Settings"
								description="Modify runtime container settings and server parameters"
								mode="form"
							>
								<ServerSettings {server} onUpdate={loadServer} />
							</ServerTabPanel>
							{#snippet failed(error, reset)}
								{@render tabFailed(error, reset)}
							{/snippet}
						</svelte:boundary>
					</div>
				</div>
			{/if}

			{#if visitedConfiguration}
				<div
					id="server-detail-tabs-panel-configuration"
					role="tabpanel"
					aria-labelledby="server-detail-tabs-tab-configuration"
					hidden={activeTab !== 'configuration'}
					class="focus-ring motion-fade-in flex min-h-0 flex-1 flex-col overflow-hidden"
					tabindex="0"
				>
					<div class="flex h-full min-h-0 flex-1 flex-col">
						<svelte:boundary>
							<ServerConfiguration {server} />
							{#snippet failed(error, reset)}
								{@render tabFailed(error, reset)}
							{/snippet}
						</svelte:boundary>
					</div>
				</div>
			{/if}

			{#if visitedRouting}
				<div
					id="server-detail-tabs-panel-routing"
					role="tabpanel"
					aria-labelledby="server-detail-tabs-tab-routing"
					hidden={activeTab !== 'routing'}
					class="focus-ring motion-fade-in flex min-h-0 flex-1 flex-col overflow-hidden"
					tabindex="0"
				>
					<div class="flex h-full min-h-0 flex-1 flex-col">
						<svelte:boundary>
							<ServerRouting {server} bind:router={routingInfo} active={activeTab === 'routing'} />
							{#snippet failed(error, reset)}
								{@render tabFailed(error, reset)}
							{/snippet}
						</svelte:boundary>
					</div>
				</div>
			{/if}

			{#if !persistentTabs.has(activeTab)}
				<div
					id={`server-detail-tabs-panel-${activeTab}`}
					role="tabpanel"
					aria-labelledby={`server-detail-tabs-tab-${activeTab}`}
					class="focus-ring motion-fade-in flex min-h-0 flex-1 flex-col overflow-hidden"
					tabindex="0"
				>
					<!--
							A throw while a tab panel renders must never be able to wedge the
							whole content area. Without this boundary, Svelte's effect flush
							abandons the batch: the tab indicator (a separate binding) still
							moves, while the panel below keeps rendering the previous tab's
							content. That is exactly the reported "Console is selected but shows
							the Settings panel" symptom, and it recurred for months because a
							single render-path throw permanently wedged this `{#if}`.

							`svelte:boundary` contains the failure to this panel, keeps the rest
							of the page interactive, and offers a retry instead of a dead tab.
						-->
					<div class="flex h-full min-h-0 flex-1 flex-col">
						<svelte:boundary>
							{#if activeTab === 'console'}
								<ServerConsole {server} active={activeTab === 'console'} />
							{:else if activeTab === 'mods'}
								<ServerMods {server} active={activeTab === 'mods'} />
							{:else if activeTab === 'datapacks'}
								<ServerDatapacks {server} />
							{:else if activeTab === 'world'}
								<ServerWorld {server} active={activeTab === 'world'} onopenmap={openMap} />
							{:else if activeTab === 'modules'}
								<ServerModules {server} active={activeTab === 'modules'} />
							{:else if activeTab === 'files'}
								<ServerFiles {server} active={activeTab === 'files'} />
							{:else if activeTab === 'tasks'}
								<ServerTasks {server} active={activeTab === 'tasks'} />
							{:else if activeTab === 'backups'}
								<ServerBackups {server} active={activeTab === 'backups'} />
							{:else if activeTab === 'players'}
								<ServerPlayers {server} active={activeTab === 'players'} />
							{:else if activeTab === 'activity'}
								<ServerTabPanel
									title="Activity"
									description="Lifecycle outcomes and recorded actions for this server"
									mode="form"
								>
									<div class="flex flex-col gap-6 p-4 sm:p-6">
										{#key serverId}
											<ServerOperations {serverId} active={activeTab === 'activity'} />
											<ServerActivity {serverId} active={activeTab === 'activity'} />
										{/key}
									</div>
								</ServerTabPanel>
							{/if}

							{#snippet failed(error, reset)}
								{@render tabFailed(error, reset)}
							{/snippet}
						</svelte:boundary>
					</div>
				</div>
			{/if}
		</div>
	</div>
{:else}
	<div class="flex h-96 flex-col items-center justify-center gap-4 bg-background p-6">
		<CarbonErrorState
			title="Failed to load server"
			message={loadError ?? 'The server could not be loaded. Try again.'}
			onretry={() => loadServer()}
			class="w-full max-w-lg"
		/>
		<a
			href={resolve('/servers')}
			class="inline-flex h-8 items-center rounded-none bg-secondary px-4 font-sans text-xs text-foreground transition-colors hover:bg-accent"
		>
			Back to servers
		</a>
	</div>
{/if}

<ScrollToTop />

{#if server}
	<!-- OSS-464: floating save/undo bar over the server view sections. -->
	<CarbonFloatingActionBar onSelectSection={selectServerTab} />
	<CarbonConfirm
		bind:open={confirmDeleteOpen}
		title={deleteDisposition === 'export'
			? 'Export world?'
			: deleteDisposition === 'detach'
				? 'Detach server?'
				: deleteDisposition === 'keep'
					? 'Remove server from the panel?'
					: 'Delete server?'}
		message={deleteDisposition === 'export'
			? 'A zip of the world downloads now. The container, the world, and this server stay in the panel.'
			: deleteDisposition === 'detach'
				? 'The container keeps running and the world stays on disk. The server is removed from the panel. Its host port stays reserved; the proxy name is dropped at the next panel start.'
				: deleteDisposition === 'keep'
					? 'This stops and removes the container and drops the server from the panel. The world directory stays on disk.'
					: 'This will stop and remove the container, delete all files and data, and remove mods and configurations. This action cannot be undone.'}
		confirmLabel={deleteDisposition === 'export'
			? 'Export world'
			: deleteDisposition === 'detach'
				? 'Detach server'
				: deleteDisposition === 'keep'
					? 'Remove from panel'
					: 'Delete Server'}
		danger={deleteDisposition !== 'export'}
		confirming={actionLoading}
		onconfirm={confirmDeleteServer}
	>
		{#snippet details()}
			{#if server}{server.name}{/if}
			<fieldset class="mt-3 space-y-2 font-sans text-xs text-foreground">
				<legend class="mb-1 text-muted-foreground">What to do with the server</legend>
				<label class="flex items-start gap-2">
					<input
						type="radio"
						name="server-detail-delete-disposition"
						value="delete"
						bind:group={deleteDisposition}
						disabled={actionLoading}
					/>
					<span>Delete the container and the world files</span>
				</label>
				<label class="flex items-start gap-2">
					<input
						type="radio"
						name="server-detail-delete-disposition"
						value="keep"
						bind:group={deleteDisposition}
						disabled={actionLoading}
					/>
					<span>Remove the container and keep the world files</span>
				</label>
				<label class="flex items-start gap-2">
					<input
						type="radio"
						name="server-detail-delete-disposition"
						value="detach"
						bind:group={deleteDisposition}
						disabled={actionLoading}
					/>
					<span>Leave the container and the world files</span>
				</label>
				<label class="flex items-start gap-2">
					<input
						type="radio"
						name="server-detail-delete-disposition"
						value="export"
						bind:group={deleteDisposition}
						disabled={actionLoading}
					/>
					<span>Export the world and leave the server</span>
				</label>
			</fieldset>
		{/snippet}
	</CarbonConfirm>
	<CarbonModal
		open={requestedMap !== null}
		title="World map"
		size="full"
		hasFooter={false}
		onclose={closeMap}
	>
		{#if requestedMap !== null}
			{#key JSON.stringify([serverId, requestedMap])}
				<WorldViewWorkspace {serverId} worldKey={requestedMap || undefined} presentation="dialog" />
			{/key}
		{/if}
	</CarbonModal>
{/if}
