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
	import { onMount, untrack } from 'svelte';
	import { rpcClient, wasRpcErrorToasted } from '$lib/api/rpc-client';
	import { triggerStreamDownload } from '$lib/api/stream-download';
	import {
		serverStatusLabel as getStatusDisplayName,
		serverStatusTone as getStatusTagType
	} from '$lib/features/servers/presentation';
	import { serversStore, serverInventory, serverActions } from '$lib/stores/servers';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import ServerLifecycleActions from '$lib/components/fragments/ServerLifecycleActions.svelte';
	import ScrollToTop from '$lib/components/scroll-to-top.svelte';
	import { toast } from 'svelte-sonner';
	import {
		RefreshCcw,
		MoreVertical,
		Package,
		Loader2,
		Copy,
		ExternalLink,
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
		Database
	} from '@lucide/svelte';
	import {
		DropdownMenu,
		DropdownMenuContent,
		DropdownMenuItem,
		DropdownMenuTrigger
	} from '$lib/components/ui/dropdown-menu';
	import type { Timestamp } from '@bufbuild/protobuf/wkt';
	import type { Server } from '$lib/proto/carbonpanel/v1/common_pb';
	import { ServerStatus, ModLoader } from '$lib/proto/carbonpanel/v1/common_pb';
	import type { GetServerRoutingResponse } from '$lib/proto/carbonpanel/v1/proxy_pb';

	import { enumToString } from '$lib/utils';
	import { effectiveLoader } from '$lib/utils/mod-loader';
	import { copyToClipboard as copyText } from '$lib/utils/clipboard';
	import {
		CarbonTag,
		CarbonTabs,
		CarbonConfirm,
		CarbonErrorState,
		CarbonPageHeader
	} from '$lib/components/carbon';
	import { joinIsPlayable, joinKindLabel } from '$lib/server-join';
	import { memoryUsagePercent } from '$lib/utils/safe-percent';
	import { exitFailureCopy, memoryLimitCopy, worldDiskCopy } from '$lib/utils/resource-enforcement';
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
	import ServerModules from '$lib/components/server/ServerModules.svelte';
	import ServerTabPanel from '$lib/components/server/ServerTabPanel.svelte';

	let serverId = $derived(page.params.id);
	let server = $derived($serversStore.find((entry) => entry.id === serverId) ?? null);
	let loading = $state(true);
	let loadError = $state<string | null>(null);
	let actionLoading = $state(false);
	let activeTab = $state('overview');
	let routingInfo = $state<GetServerRoutingResponse | null>(null);

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

	async function handleServerAction(action: 'start' | 'stop' | 'restart' | 'recreate') {
		if (!server) return;

		actionLoading = true;

		const target = server;
		try {
			const outcome = await serverActions.perform(target.id, action);
			if (outcome.kind === 'superseded') return;
			toast.success(
				`Server is ${action === 'recreate' ? 'recreating' : action === 'stop' ? 'stopping' : action === 'restart' ? 'restarting' : 'starting'}...`
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

	const subViewTabs = [
		{ id: 'overview', label: 'Overview', icon: Settings },
		{ id: 'console', label: 'Console', icon: Terminal },
		{ id: 'configuration', label: 'Configuration', icon: HardDrive },
		{ id: 'mods', label: 'Mods', icon: Package },
		{ id: 'datapacks', label: 'Datapacks', icon: Database },
		{ id: 'modules', label: 'Modules', icon: Boxes },
		{ id: 'files', label: 'Files', icon: Files },
		{ id: 'tasks', label: 'Tasks', icon: ListTodo },
		{ id: 'backups', label: 'Backups', icon: Archive },
		{ id: 'players', label: 'Players', icon: Users },
		{ id: 'activity', label: 'Activity', icon: History },
		{ id: 'routing', label: 'Routing', icon: Radio }
	];
</script>

{#if loading && !server}
	<div class="flex h-96 items-center justify-center bg-[#161616]">
		<div class="flex flex-col items-center gap-3">
			<div class="h-10 w-10 animate-spin border-4 border-[#0f62fe] border-t-transparent"></div>
			<p class="font-mono text-xs text-[#a8a8a8]">Connecting to server daemon...</p>
		</div>
	</div>
{:else if server}
	<div
		class="flex min-h-0 flex-1 flex-col gap-6 bg-[#161616] font-sans text-[#f4f4f4]"
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
					class="flex h-10 w-10 shrink-0 items-center justify-center rounded-none border border-[#393939] bg-[#262626] text-[#c6c6c6] transition-colors hover:bg-[#353535] hover:text-white"
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

						<!-- Dropdown for recreate and delete -->
						<DropdownMenu>
							<DropdownMenuTrigger>
								{#snippet child({ props })}
									<button
										type="button"
										{...props}
										disabled={actionLoading}
										aria-label="More server actions"
										class="flex h-10 w-10 cursor-pointer items-center justify-center rounded-none border border-[#393939] bg-[#262626] text-[#c6c6c6] transition-colors hover:bg-[#353535] hover:text-white"
									>
										<MoreVertical class="h-4 w-4" />
									</button>
								{/snippet}
							</DropdownMenuTrigger>
							<DropdownMenuContent
								align="end"
								class="rounded-none border border-[#393939] bg-[#161616] p-1 text-[#f4f4f4]"
							>
								<DropdownMenuItem
									class="flex cursor-pointer items-center gap-2 rounded-none px-3 py-2 text-xs text-[#f4f4f4] hover:bg-[#353535]"
									onclick={() => handleServerAction('recreate')}
								>
									<RefreshCcw class="h-3.5 w-3.5" />
									<span>Force Recreate</span>
								</DropdownMenuItem>
								<DropdownMenuItem
									class="flex cursor-pointer items-center gap-2 rounded-none px-3 py-2 text-xs text-[#ff8389] hover:bg-[#da1e28]/20"
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

		<!-- Carbon Metric Tiles (ZERO rounded corners) -->
		<div
			class="motion-stagger grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4 [&>*]:min-w-0"
			data-testid="showcase-runtime-tiles"
		>
			<!-- Tile 1: Status & Heartbeat -->
			<div
				data-testid="showcase-runtime-tile"
				class="flex flex-col justify-between rounded-none border border-[#393939] bg-[#262626] p-6 transition-colors hover:border-[#525252]"
			>
				<div class="flex items-center justify-between">
					<span class="font-sans text-xs font-semibold tracking-wider text-[#a8a8a8] uppercase"
						>Runtime Status</span
					>
					<CarbonTag type={getStatusTagType(server.status)} size="sm">
						{getStatusDisplayName(server.status)}
					</CarbonTag>
				</div>
				<div class="mt-3 flex items-center justify-center gap-2 py-4">
					<div class="heartbeat-container">
						{#if server.status === ServerStatus.RUNNING}
							{#each Array(5) as _, i (i)}
								<div class="heartbeat-bar bg-[#24a148]" style="animation-delay: {i * 0.15}s"></div>
							{/each}
						{:else if server.status === ServerStatus.ERROR}
							{#each Array(5) as _, i (i)}
								<div
									class="heartbeat-bar heartbeat-erratic bg-[#da1e28]"
									style="animation-delay: {i * 0.1}s"
								></div>
							{/each}
						{:else if server.status === ServerStatus.UNHEALTHY}
							{#each Array(5) as _, i (i)}
								<div class="heartbeat-bar bg-[#f1c21b]" style="animation-delay: {i * 0.15}s"></div>
							{/each}
						{:else}
							{#each Array(5) as _, i (i)}
								<div class="heartbeat-bar bg-[#525252]" style="animation-delay: {i * 0.25}s"></div>
							{/each}
						{/if}
					</div>
				</div>
				<div class="mt-3 border-t border-[#393939] pt-3 text-center">
					<p class="font-sans text-[11px] text-[#8d8d8d]">
						{#if server.status === ServerStatus.RUNNING}
							Server healthy and processing ticks
						{:else if server.status === ServerStatus.STOPPED}
							Container offline
						{:else if server.status === ServerStatus.CREATING}
							Provisioning container & checking image
						{:else if server.status === ServerStatus.STARTING}
							Starting server (modpack downloads may take several minutes)
						{:else if server.status === ServerStatus.RESTARTING}
							Restarting server container
						{:else if server.status === ServerStatus.DEEPSLEEP}
							{isProxied
								? 'Deep sleeping (container stopped, proxy listening for login wake)'
								: 'Deep sleeping (container stopped, direct port listening for login wake)'}
						{:else if server.status === ServerStatus.PAUSED}
							Paused via cgroup freezer (instant resume available)
						{:else if server.status === ServerStatus.UNHEALTHY}
							Server running but failing health checks
						{:else if server.status === ServerStatus.STOPPING}
							Gracefully shutting down
						{:else if server.status === ServerStatus.ERROR}
							{exitFailureCopy(server.statusReason)}
						{:else}
							Daemon status: {getStatusDisplayName(server.status)}
						{/if}
					</p>
				</div>
			</div>

			<!-- Tile 2: Connection Address -->
			<div
				class="flex flex-col justify-between rounded-none border border-[#393939] bg-[#262626] p-6 transition-colors hover:border-[#525252]"
			>
				<div class="flex items-center justify-between">
					<span class="font-sans text-xs font-semibold tracking-wider text-[#a8a8a8] uppercase"
						>Network Ingress</span
					>
					<div class="flex items-center gap-2">
						{#if isProxied}
							<span
								class="rounded-none bg-[#0f62fe]/20 px-1.5 py-0.5 font-mono text-[10px] text-[#78a9ff]"
								>PROXIED</span
							>
						{:else}
							<span
								class="rounded-none bg-[#393939] px-1.5 py-0.5 font-mono text-[10px] text-[#c6c6c6]"
								>DIRECT PORT</span
							>
						{/if}
						<ExternalLink class="h-4 w-4 text-[#78a9ff]" />
					</div>
				</div>
				<div class="mt-3 py-1">
					<p class="font-mono text-xs text-[#8d8d8d] uppercase">Connect String</p>
					<p class="mt-0.5 truncate font-mono text-base font-semibold text-[#f4f4f4]">
						{connectAddress}
					</p>
				</div>
				<div class="mt-3 border-t border-[#393939] pt-3">
					<button
						type="button"
						onclick={() => {
							copyConnection(connectAddress);
						}}
						class="flex h-8 w-full cursor-pointer items-center justify-center gap-2 rounded-none border border-[#393939] bg-[#161616] px-3 font-mono text-xs text-[#78a9ff] transition-colors hover:bg-[#353535]"
					>
						<Copy class="h-3.5 w-3.5" />
						<span>Copy Address</span>
					</button>
				</div>
			</div>

			<!-- Tile 3: Engine Details -->
			<div
				class="flex flex-col justify-between rounded-none border border-[#393939] bg-[#262626] p-6 transition-colors hover:border-[#525252]"
			>
				<div class="flex items-center justify-between">
					<span class="font-sans text-xs font-semibold tracking-wider text-[#a8a8a8] uppercase"
						>Engine Specs</span
					>
					<Info class="h-4 w-4 text-[#d4bbff]" />
				</div>
				<div class="mt-3 space-y-2 font-mono text-xs">
					<div class="flex items-center justify-between">
						<span class="text-[#8d8d8d]">Minecraft:</span>
						<span class="font-semibold text-[#f4f4f4]">{server.mcVersion || 'Latest'}</span>
					</div>
					<div class="flex items-center justify-between">
						<span class="text-[#8d8d8d]">Loader:</span>
						<span class="flex items-center gap-1.5">
							{#if server.loaderVersion}
								<span class="font-mono text-xs text-[#c6c6c6]">{server.loaderVersion}</span>
							{/if}
							<CarbonTag type="teal" size="sm">
								{enumToString(ModLoader, effectiveLoader(server.modLoader, server.modpackLoader)) ||
									'vanilla'}
							</CarbonTag>
						</span>
					</div>
					{#if server.javaVersion}
						<div class="flex items-center justify-between">
							<span class="text-[#8d8d8d]">Java:</span>
							<span class="text-[#c6c6c6]">JDK {server.javaVersion}</span>
						</div>
					{/if}
				</div>
				<div
					class="mt-3 flex items-center justify-between border-t border-[#393939] pt-3 font-sans text-[11px] text-[#8d8d8d]"
				>
					<span>Port: {server.port}</span>
					<span>Max Players: {server.maxPlayers}</span>
				</div>
			</div>

			<!-- Tile 4: Hardware Quotas -->
			<div
				class="flex flex-col justify-between rounded-none border border-[#393939] bg-[#262626] p-6 transition-colors hover:border-[#525252]"
			>
				<div class="flex items-center justify-between">
					<span class="font-sans text-xs font-semibold tracking-wider text-[#a8a8a8] uppercase"
						>Resource Telemetry</span
					>
					<Cpu class="h-4 w-4 text-[#ff8389]" />
				</div>
				<div class="mt-3 space-y-2.5">
					<!-- Memory Bar -->
					<div>
						<div class="flex items-center justify-between font-mono text-xs">
							<span class="text-[#8d8d8d]">RAM</span>
							<span class="text-[#f4f4f4]">
								{#if server.status === ServerStatus.CREATING}
									{memoryLimitCopy(server.resourceEnforcement).label}: {(
										server.memory / 1024
									).toFixed(1)} GB
								{:else}
									{server.memoryUsage ? (Number(server.memoryUsage) / 1024).toFixed(1) : '0'} / {(
										server.memory / 1024
									).toFixed(1)} GB
								{/if}
							</span>
						</div>
						<div class="mt-1 h-1.5 w-full rounded-none border border-[#393939] bg-[#161616]">
							<div
								class="h-full rounded-none bg-[#0f62fe] transition-all"
								style="width: {server.status === ServerStatus.CREATING
									? '100'
									: memoryUsagePercent(server.memoryUsage, server.memory)}%"
							></div>
						</div>
					</div>

					<!-- CPU Bar -->
					<div>
						<div class="flex items-center justify-between font-mono text-xs">
							<span class="text-[#8d8d8d]">CPU</span>
							<span class="text-[#33b1ff]">
								{server.cpuPercent !== undefined ? `${server.cpuPercent.toFixed(1)}%` : '—'}
							</span>
						</div>
						<div class="mt-1 h-1.5 w-full rounded-none border border-[#393939] bg-[#161616]">
							<div
								class="h-full rounded-none bg-[#33b1ff] transition-all"
								style="width: {Math.min(server.cpuPercent || 0, 100)}%"
							></div>
						</div>
					</div>
				</div>

				<p class="mt-2 font-sans text-[11px] text-[#8d8d8d]">
					{memoryLimitCopy(server.resourceEnforcement).label}
					· {worldDiskCopy(server.resourceEnforcement).label}
				</p>
				<div
					class="mt-3 flex items-center justify-between border-t border-[#393939] pt-3 font-sans text-[11px]"
				>
					<span class="text-[#8d8d8d]">TPS:</span>
					<span
						class="{server.status === ServerStatus.RUNNING && server.tps > 0
							? server.tps >= 18
								? 'text-[#6fdc8c]'
								: 'text-[#f1c21b]'
							: 'text-[#8d8d8d]'} font-semibold"
					>
						{server.status === ServerStatus.RUNNING && server.tps > 0 ? server.tps.toFixed(1) : '—'}
					</span>
					<span class="text-[#8d8d8d]">Players:</span>
					<span
						class="font-semibold {joinIsPlayable(server) ? 'text-[#6fdc8c]' : 'text-[#8d8d8d]'}"
					>
						{joinIsPlayable(server)
							? server.playersOnline || 0
							: joinKindLabel(server.joinObservationKind)}
					</span>
				</div>
			</div>
		</div>

		<!-- Startup & Creation Progress Banner (MINE-125 / MINE-128) -->
		{#if server.status === ServerStatus.STARTING}
			<div
				class="flex flex-col items-start justify-between gap-3 border border-l-4 border-[#393939] border-l-[#0f62fe] bg-[#161616] p-4 sm:flex-row sm:items-center"
			>
				<div class="flex items-center gap-3">
					<Loader2 class="h-5 w-5 shrink-0 animate-spin text-accent-text" />
					<div>
						<p class="text-sm font-semibold text-[#f4f4f4]">Server is starting up</p>
						<p class="mt-0.5 text-xs text-[#a8a8a8]">
							First-boot modpack downloads (manifests, loader installers, mod jars) can take 2–5
							minutes. Monitor real-time logs in the Console tab.
						</p>
					</div>
				</div>
				<button
					type="button"
					onclick={() => (activeTab = 'console')}
					class="flex h-8 shrink-0 cursor-pointer items-center gap-1.5 border border-[#393939] bg-[#262626] px-3 font-mono text-xs text-[#78a9ff] transition-colors hover:bg-[#353535]"
				>
					<Terminal class="h-3.5 w-3.5" />
					<span>Open Console</span>
				</button>
			</div>
		{:else if server.status === ServerStatus.CREATING}
			<div
				class="flex items-center gap-3 border border-l-4 border-[#393939] border-l-[#0f62fe] bg-[#161616] p-4"
			>
				<Loader2 class="h-5 w-5 shrink-0 animate-spin text-accent-text" />
				<div>
					<p class="text-sm font-semibold text-[#f4f4f4]">Creating server environment</p>
					<p class="mt-0.5 text-xs text-[#a8a8a8]">
						Checking Docker image and allocating filesystem storage...
					</p>
				</div>
			</div>
		{/if}

		<!-- Carbon Tabs for Sub-Views with bottom blue line indicator (Requirement 4) -->
		<div class="flex min-h-0 flex-1 flex-col gap-4">
			<!-- Tab Bar -->
			<CarbonTabs
				tabs={subViewTabs}
				bind:selectedTab={activeTab}
				idPrefix="server-detail-tabs"
				class="sticky top-0 z-20 shrink-0 bg-[#161616]"
			/>

			<!-- Tab Content Areas -->
			<div
				id={`server-detail-tabs-panel-${activeTab}`}
				role="tabpanel"
				aria-labelledby={`server-detail-tabs-tab-${activeTab}`}
				class="motion-fade-in flex min-h-0 flex-1 flex-col overflow-hidden"
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
						{#if activeTab === 'overview'}
							<ServerTabPanel
								title="Server Settings"
								description="Modify runtime container settings and server parameters"
								mode="form"
							>
								<div class="p-4 sm:p-6">
									<ServerSettings {server} onUpdate={loadServer} />
								</div>
							</ServerTabPanel>
						{:else if activeTab === 'console'}
							<ServerConsole {server} active={activeTab === 'console'} />
						{:else if activeTab === 'configuration'}
							<ServerConfiguration {server} />
						{:else if activeTab === 'mods'}
							<ServerMods {server} active={activeTab === 'mods'} />
						{:else if activeTab === 'datapacks'}
							<ServerDatapacks {server} />
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
								description="Lifecycle journal and who did what on this server, newest first"
								mode="form"
							>
								<div class="flex flex-col gap-6 p-4 sm:p-6">
									<ServerOperations {server} active={activeTab === 'activity'} />
									<ServerActivity {server} active={activeTab === 'activity'} />
								</div>
							</ServerTabPanel>
						{:else if activeTab === 'routing'}
							<ServerRouting {server} bind:router={routingInfo} active={activeTab === 'routing'} />
						{/if}

						{#snippet failed(error, reset)}
							<div
								class="flex min-h-64 flex-col items-center justify-center gap-3 border border-[#393939] bg-[#262626] p-6 text-center"
								role="alert"
							>
								<p class="font-mono text-sm text-[#ff8389]">This tab failed to render</p>
								<p class="max-w-lg text-xs break-words text-[#a8a8a8]">
									{error instanceof Error && error.message ? error.message : String(error)}
								</p>
								<div class="mt-1 flex items-center gap-2">
									<button
										type="button"
										onclick={reset}
										class="flex h-8 cursor-pointer items-center border border-[#0f62fe] bg-[#0f62fe] px-4 font-sans text-xs text-white transition-colors hover:bg-[#0353e9]"
									>
										Retry
									</button>
									<a
										href={resolve('/servers')}
										class="flex h-8 items-center border border-[#393939] bg-[#262626] px-4 font-sans text-xs text-[#f4f4f4] transition-colors hover:bg-[#353535]"
									>
										Back to servers
									</a>
								</div>
							</div>
						{/snippet}
					</svelte:boundary>
				</div>
			</div>
		</div>
	</div>
{:else}
	<div class="flex h-96 flex-col items-center justify-center gap-4 bg-[#161616] p-6">
		<CarbonErrorState
			title="Failed to load server"
			message={loadError ?? 'The server could not be loaded. Try again.'}
			onretry={() => loadServer()}
			class="w-full max-w-lg"
		/>
		<a
			href={resolve('/servers')}
			class="inline-flex h-8 items-center rounded-none bg-[#393939] px-4 font-sans text-xs text-white transition-colors hover:bg-[#4c4c4c]"
		>
			Back to servers
		</a>
	</div>
{/if}

<ScrollToTop />

{#if server}
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
				? 'The container keeps running and the world stays on disk. The panel forgets this server. Its host port stays reserved. A proxy name is dropped the next time the panel starts.'
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
			<fieldset class="mt-3 space-y-2 font-sans text-xs text-[#f4f4f4]">
				<legend class="mb-1 text-[#c6c6c6]">What to do with the server</legend>
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
{/if}

<style>
	.heartbeat-container {
		display: flex;
		align-items: center;
		justify-content: center;
		gap: 3px;
		height: 36px;
	}

	.heartbeat-bar {
		width: 4px;
		height: 32px;
		border-radius: 0px;
		animation: heartbeat 3.5s ease-in-out infinite;
	}

	.heartbeat-bar.heartbeat-erratic {
		animation: heartbeat-erratic 1.5s ease-in-out infinite;
	}

	@keyframes heartbeat {
		0%,
		100% {
			height: 6px;
			opacity: 0.3;
		}
		50% {
			height: 32px;
			opacity: 1;
		}
	}

	@keyframes heartbeat-erratic {
		0%,
		100% {
			height: 4px;
			opacity: 0.2;
		}
		25% {
			height: 24px;
			opacity: 0.9;
		}
		50% {
			height: 12px;
			opacity: 0.5;
		}
		75% {
			height: 30px;
			opacity: 1;
		}
	}
</style>
