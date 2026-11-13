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
	import { resolve } from '$app/paths';
	import { toast } from 'svelte-sonner';
	import {
		serverStatusTone as getStatusTagType,
		serverStatusLabel
	} from '$lib/features/servers/presentation';
	import { formatBytes, getStringForEnum } from '$lib/utils';
	import {
		Server,
		MemoryStick,
		Plus,
		Clock,
		Users,
		Zap,
		ChevronRight,
		Gauge,
		AlertTriangle,
		RefreshCw,
		PlayCircle,
		StopCircle
	} from '@lucide/svelte';
	import { ServerStatus, type Server as ServerType } from '$lib/proto/carbonpanel/v1/common_pb';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import {
		NodeStatus as NodeStatusEnum,
		type Node as NodeType
	} from '$lib/proto/carbonpanel/v1/node_pb';
	import { formatRpcError } from '$lib/api/rpc-error';
	import { prefersReducedMotion } from '$lib/motion';
	import {
		serversStore,
		serverInventory,
		serverActions,
		sortServersByActivity
	} from '$lib/stores/servers';
	import { joinIsPlayable, joinKindLabel } from '$lib/server-join';
	import {
		CarbonButton,
		CarbonTag,
		CarbonErrorState,
		CarbonPageHeader
	} from '$lib/components/carbon';
	import type { Timestamp } from '@bufbuild/protobuf/wkt';

	// Dashboard data
	let dashboardServers = $derived($serversStore);
	let dashboardLoadGeneration = 0;
	let mounted = false;
	let dashboardNodes: NodeType[] = $state([]);
	let isLoading = $state(true);
	let loadError = $state<string | null>(null);
	let isRefreshing = $state(false);
	let currentTime = $state(new Date());

	// Load dashboard data with full stats, plus node inventory fail-open.
	// NodeService.listNodes runs in parallel with listServers so a node
	// failure never blocks the server tiles; nodes stay empty on error.
	async function loadDashboardData() {
		const generation = ++dashboardLoadGeneration;
		loadError = null;
		try {
			await Promise.all([
				serverInventory.refresh(true),
				rpcClient.node
					.listNodes({}, silentCallOptions)
					.then((res) => {
						if (mounted && generation === dashboardLoadGeneration) dashboardNodes = res.nodes ?? [];
					})
					.catch((nodeError) => {
						console.error('Failed to load node inventory (fail-open):', nodeError);
					})
			]);
		} catch (error) {
			if (mounted && generation === dashboardLoadGeneration)
				loadError = formatRpcError(error, 'Could not load dashboard data. Try again.');
			console.error('Failed to load dashboard data:', error);
		}
	}

	// Refresh function for manual updates
	async function refreshDashboard() {
		isRefreshing = true;
		await loadDashboardData();
		isRefreshing = false;
	}

	async function handleServerAction(action: 'start' | 'stop', server: ServerType) {
		try {
			const outcome=await serverActions.perform(server.id, action);
if(outcome.kind==='superseded')return;
			toast.success(`${action === 'start' ? 'Starting' : 'Stopping'} ${server.name}...`);
		} catch (error) {
			toast.error(
				`Failed to ${action} server: ${error instanceof Error ? error.message : 'Unknown error'}`
			);
		}
	}

	let stats = $derived({
		total: dashboardServers.length,
		running: dashboardServers.filter((s) => s.status === ServerStatus.RUNNING).length,
		stopped: dashboardServers.filter((s) => s.status === ServerStatus.STOPPED).length,
		error: dashboardServers.filter((s) => s.status === ServerStatus.ERROR).length,
		warning: dashboardServers.filter((s) => s.status === ServerStatus.UNHEALTHY).length,
		totalMemory: dashboardServers.reduce((acc, s) => acc + (s.memory || 0), 0),
		usedMemory: dashboardServers
			.filter((s) => s.status === ServerStatus.RUNNING)
			.reduce((acc, s) => acc + Number(s.memoryUsage || s.memory || 0), 0),
		totalPlayers: dashboardServers
			.filter((s) => joinIsPlayable(s))
			.reduce((acc, s) => acc + (s.playersOnline || 0), 0),
		totalMaxPlayers: dashboardServers.reduce((acc, s) => acc + (s.maxPlayers || 0), 0),
		avgTps: dashboardServers
			.filter((s) => s.tps && s.tps > 0)
			.reduce((acc, s, _, arr) => acc + (s.tps || 0) / arr.length, 0),
		totalDiskUsage: dashboardServers.reduce((acc, s) => acc + Number(s.diskUsage || 0), 0),
		totalDiskSize:
			dashboardServers.length > 0 && dashboardServers[0]?.diskTotal
				? ` / ${formatBytes(Number(dashboardServers[0].diskTotal))}`
				: '',
		avgCpu: dashboardServers
			.filter((s) => s.cpuPercent && s.cpuPercent > 0)
			.reduce((acc, s, _, arr) => acc + (s.cpuPercent || 0) / arr.length, 0),
		activeRatio:
			dashboardServers.length > 0
				? Math.round(
						(dashboardServers.filter((s) => s.status === ServerStatus.RUNNING).length /
							dashboardServers.length) *
							100
					)
				: 0,
		memUsagePercent:
			dashboardServers.reduce((acc, s) => acc + (s.memory || 0), 0) > 0
				? Math.min(
						100,
						Math.round(
							(dashboardServers
								.filter((s) => s.status === ServerStatus.RUNNING)
								.reduce((acc, s) => acc + Number(s.memoryUsage || s.memory || 0), 0) /
								dashboardServers.reduce((acc, s) => acc + (s.memory || 0), 0)) *
								100
						)
					)
				: 0
	});

	let recentActivity = $derived(
		dashboardServers
			.filter((s) => s.lastStarted)
			.sort(
				(a, b) =>
					new Date(Number(b.lastStarted!.seconds) * 1000).getTime() -
					new Date(Number(a.lastStarted!.seconds) * 1000).getTime()
			)
			.slice(0, 5)
			.map((s) => ({
				server: s.name,
				action: s.status === ServerStatus.RUNNING ? 'Started' : 'Activity',
				time: s.lastStarted,
				status: s.status
			}))
	);

	let serversByStatus = $derived({
		healthy: dashboardServers.filter(
			(s) => s.status === ServerStatus.RUNNING && (!s.tps || s.tps >= 18)
		),
		warning: dashboardServers.filter(
			(s) =>
				s.status === ServerStatus.UNHEALTHY ||
				(s.status === ServerStatus.RUNNING && s.tps && s.tps < 18 && s.tps >= 15)
		),
		critical: dashboardServers.filter(
			(s) =>
				s.status === ServerStatus.ERROR ||
				(s.status === ServerStatus.RUNNING && s.tps && s.tps < 15)
		)
	});

	let nodeStats = $derived({
		total: dashboardNodes.length,
		online: dashboardNodes.filter((n) => n.status === NodeStatusEnum.ONLINE).length,
		offline: dashboardNodes.filter((n) => n.status !== NodeStatusEnum.ONLINE).length,
		totalServers: dashboardNodes.reduce((acc, n) => acc + (n.serverCount || 0), 0),
		totalAllocatedMb: dashboardNodes.reduce((acc, n) => acc + Number(n.allocatedMemoryMb || 0n), 0)
	});

	function getNodeTagType(status: NodeStatusEnum): 'green' | 'red' | 'gray' {
		switch (status) {
			case NodeStatusEnum.ONLINE:
				return 'green';
			case NodeStatusEnum.ERROR:
				return 'red';
			case NodeStatusEnum.OFFLINE:
			default:
				return 'gray';
		}
	}

	function describeNodeStatusLabel(status: NodeStatusEnum): string {
		switch (status) {
			case NodeStatusEnum.ONLINE:
				return 'ONLINE';
			case NodeStatusEnum.OFFLINE:
				return 'OFFLINE';
			case NodeStatusEnum.ERROR:
				return 'ERROR';
			default:
				return 'UNKNOWN';
		}
	}

	function formatNodeMemory(node: NodeType): string {
		const allocatedGb = Number(node.allocatedMemoryMb || 0n) / 1024;
		const totalMb = Number(node.totalMemoryMb || node.maxMemoryMb || 0n);
		if (totalMb <= 0) return `${allocatedGb.toFixed(1)} GB allocated`;
		return `${allocatedGb.toFixed(1)} / ${(totalMb / 1024).toFixed(1)} GB`;
	}

	onMount(() => {
		mounted = true;
		loadDashboardData().then(() => {
			isLoading = false;
		});

		const interval = setInterval(() => {
			currentTime = new Date();
		}, 1000);
		return () => {
			mounted = false;
			++dashboardLoadGeneration;
			clearInterval(interval);
		};
	});

	// Status pip colours derive from getStatusTagType so a transient state
	// (starting, sleeping, paused) is never shown as an error red. Only
	// ERROR is red; UNHEALTHY is a yellow degraded state, and deep sleep and
	// other transients are purple.
	const statusPipColor: Record<string, string> = {
		green: 'bg-[#24a148]',
		red: 'bg-[#da1e28]',
		yellow: 'bg-[#f1c21b]',
		purple: 'bg-[#a56eff]',
		blue: 'bg-[#0f62fe]',
		gray: 'bg-[#8d8d8d]',
		'warm-gray': 'bg-[#8d8d8d]'
	};

	const formatUptime = (lastStarted?: Timestamp) => {
		if (!lastStarted) return 'Never';
		const start = new Date(Number(lastStarted.seconds) * 1000);
		const diff = currentTime.getTime() - start.getTime();
		const days = Math.floor(diff / (1000 * 60 * 60 * 24));
		const hours = Math.floor((diff % (1000 * 60 * 60 * 24)) / (1000 * 60 * 60));
		const minutes = Math.floor((diff % (1000 * 60 * 60)) / (1000 * 60));

		if (days > 0) return `${days}d ${hours}h`;
		if (hours > 0) return `${hours}h ${minutes}m`;
		return `${minutes}m`;
	};

	const getTpsColor = (tps: number | undefined) => {
		if (!tps) return 'text-[#8d8d8d]';
		if (tps >= 19) return 'text-[#24a148]';
		if (tps >= 17) return 'text-[#f1c21b]';
		return 'text-[#da1e28]';
	};

	function countUp(node: HTMLElement, params: { value: number; decimals?: number }) {
		const decimals = params.decimals ?? 0;
		let raf = 0;
		let current = 0;
		const reduce = prefersReducedMotion();
		const render = (v: number) => {
			node.textContent = v.toFixed(decimals);
		};
		const animate = (to: number) => {
			cancelAnimationFrame(raf);
			if (reduce) {
				current = to;
				render(to);
				return;
			}
			const from = current;
			const start = performance.now();
			const duration = 500;
			const tick = (now: number) => {
				const p = Math.min(1, (now - start) / duration);
				const eased = 1 - Math.pow(1 - p, 3);
				current = from + (to - from) * eased;
				render(current);
				if (p < 1) raf = requestAnimationFrame(tick);
			};
			raf = requestAnimationFrame(tick);
		};
		animate(params.value);
		return {
			update(next: { value: number; decimals?: number }) {
				animate(next.value);
			},
			destroy() {
				cancelAnimationFrame(raf);
			}
		};
	}
</script>

{#if isLoading}
	<div class="space-y-6 bg-[#161616] text-[#f4f4f4]">
		<div
			class="flex flex-col justify-between gap-4 border-b border-[#393939] pb-4 sm:flex-row sm:items-center"
		>
			<div class="w-full">
				<div class="motion-skeleton h-8 w-56 bg-[#262626]"></div>
				<div class="motion-skeleton mt-2 h-3 w-80 bg-[#262626]"></div>
			</div>
			<div class="flex items-center gap-2">
				<div class="motion-skeleton h-8 w-28 bg-[#262626]"></div>
				<div class="motion-skeleton h-8 w-32 bg-[#262626]"></div>
			</div>
		</div>
		<div class="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
			{#each [0, 1, 2, 3] as _ (_)}
				<div
					class="flex h-[140px] flex-col justify-between rounded-none border border-[#393939] bg-[#262626] p-6 select-none"
				>
					<div>
						<div class="flex items-center justify-between">
							<div class="motion-skeleton h-3 w-24 bg-[#393939]"></div>
							<div class="motion-skeleton h-4 w-4 bg-[#393939]"></div>
						</div>
						<div class="motion-skeleton mt-3 h-8 w-20 bg-[#393939]"></div>
					</div>
					<div class="mt-3 flex items-center justify-between border-t border-[#393939] pt-3">
						<div class="motion-skeleton h-3 w-20 bg-[#393939]"></div>
						<div class="motion-skeleton h-3 w-12 bg-[#393939]"></div>
					</div>
				</div>
			{/each}
		</div>
	</div>
{:else if loadError}
	<div class="p-6">
		<CarbonErrorState
			title="Failed to load dashboard"
			message={loadError}
			onretry={refreshDashboard}
		/>
	</div>
{:else}
	<div class="space-y-6 bg-[#161616] text-[#f4f4f4]" data-testid="showcase-dashboard-content">
		<CarbonPageHeader
			title="Cluster Overview"
			description="Hardware utilization, game servers status, and network routing topology"
		>
			{#snippet tag()}
				<CarbonTag type="blue" size="sm">Carbon Panel</CarbonTag>
			{/snippet}
			{#snippet actions()}
				<CarbonButton
					kind="secondary"
					size="md"
					class="justify-center gap-2"
					onclick={refreshDashboard}
					disabled={isRefreshing}
				>
					<RefreshCw class="h-4 w-4 {isRefreshing ? 'animate-spin' : ''}" />
					<span>Refresh</span>
				</CarbonButton>
				<CarbonButton
					kind="primary"
					size="md"
					class="justify-center gap-2"
					href={resolve('/servers/new')}
				>
					<Plus class="h-4 w-4" />
					<span>Create Server</span>
				</CarbonButton>
			{/snippet}
		</CarbonPageHeader>

		<!-- Carbon KPI Tiles -->
		<div
			class="motion-stagger grid gap-4 md:grid-cols-2 lg:grid-cols-4 [&>*]:min-w-0"
			data-testid="showcase-kpi-tiles"
		>
			<!-- Tile 1: Total Servers -->
			<div
				class="flex flex-col justify-between rounded-none border border-[#393939] bg-[#262626] p-6 transition-colors select-none hover:border-[#525252]"
			>
				<div>
					<div
						class="flex items-center justify-between font-sans text-xs font-semibold tracking-wider text-[#a8a8a8] uppercase"
					>
						<span>Total Servers</span>
						<Server class="h-4 w-4 text-[#8d8d8d]" />
					</div>
					<div
						class="mt-3 font-sans text-[32px] leading-none font-semibold tracking-tight text-[#f4f4f4] tabular-nums"
					>
						<span use:countUp={{ value: stats.total, decimals: 0 }}></span>
					</div>
				</div>
				<div
					class="mt-3 flex items-center justify-between gap-2 border-t border-[#393939] pt-3 font-sans text-[11px] text-[#8d8d8d]"
				>
					<div class="flex items-center gap-3">
						<div class="flex items-center gap-1.5 text-[#24a148]">
							<span class="h-1.5 w-1.5 bg-[#24a148]"></span>
							<span>{stats.running} ACTIVE</span>
						</div>
						{#if stats.error > 0}
							<div class="flex items-center gap-1.5 text-[#da1e28]">
								<span class="h-1.5 w-1.5 bg-[#da1e28]"></span>
								<span>{stats.error} ISSUES</span>
							</div>
						{:else if stats.warning > 0}
							<div class="flex items-center gap-1.5 text-[#f1c21b]">
								<span class="h-1.5 w-1.5 bg-[#f1c21b]"></span>
								<span>{stats.warning} DEGRADED</span>
							</div>
						{:else}
							<span class="text-[#8d8d8d]">{stats.stopped} STOPPED</span>
						{/if}
					</div>
					<span class="text-[#8d8d8d]">RATIO {stats.activeRatio}%</span>
				</div>
			</div>

			<!-- Tile 2: Active Players -->
			<div
				class="flex flex-col justify-between rounded-none border border-[#393939] bg-[#262626] p-6 transition-colors select-none hover:border-[#525252]"
			>
				<div>
					<div
						class="flex items-center justify-between font-sans text-xs font-semibold tracking-wider text-[#a8a8a8] uppercase"
					>
						<span>Active Players</span>
						<Users class="h-4 w-4 text-[#8d8d8d]" />
					</div>
					<div
						class="mt-3 font-sans text-[32px] leading-none font-semibold tracking-tight text-[#f4f4f4] tabular-nums"
					>
						<span use:countUp={{ value: stats.totalPlayers, decimals: 0 }}></span>
					</div>
				</div>
				<div
					class="mt-3 flex items-center justify-between border-t border-[#393939] pt-3 font-sans text-[11px] text-[#8d8d8d]"
				>
					{#if stats.totalPlayers > 0}
						<div class="flex items-center gap-1.5 text-[#24a148]">
							<span class="h-1.5 w-1.5 bg-[#24a148]"></span>
							<span
								>{stats.totalPlayers === 1 ? '1 PLAYER' : `${stats.totalPlayers} PLAYERS`} ONLINE</span
							>
						</div>
					{:else}
						<span class="text-[#8d8d8d]">0 PLAYERS ONLINE</span>
					{/if}
					{#if stats.totalMaxPlayers > 0}
						<span class="text-[#8d8d8d]">MAX: {stats.totalMaxPlayers}</span>
					{/if}
				</div>
			</div>

			<!-- Tile 3: Memory Usage -->
			<div
				class="flex flex-col justify-between rounded-none border border-[#393939] bg-[#262626] p-6 transition-colors select-none hover:border-[#525252]"
			>
				<div>
					<div
						class="flex items-center justify-between font-sans text-xs font-semibold tracking-wider text-[#a8a8a8] uppercase"
					>
						<span>Memory Allocation</span>
						<MemoryStick class="h-4 w-4 text-[#8d8d8d]" />
					</div>
					<div class="mt-3 flex items-baseline gap-1.5">
						<span
							class="font-sans text-[32px] leading-none font-semibold tracking-tight text-[#f4f4f4] tabular-nums"
							use:countUp={{
								value: stats.totalMemory > 0 ? stats.usedMemory / 1024 : 0,
								decimals: 1
							}}
						></span>
						<span class="font-sans text-sm text-[#8d8d8d]">
							/ {stats.totalMemory > 0 ? (stats.totalMemory / 1024).toFixed(1) : '0.0'} GB
						</span>
					</div>
					<!-- Sharp Carbon Progress Bar -->
					<div class="mt-3 h-1.5 w-full overflow-hidden rounded-none bg-[#393939]">
						<div
							class="h-full bg-[#0f62fe] transition-[width] duration-[var(--motion-slow)] ease-[var(--ease-out-quart)]"
							style="width: {stats.memUsagePercent}%"
						></div>
					</div>
				</div>
				<div
					class="mt-3 flex items-center justify-between border-t border-[#393939] pt-3 font-sans text-[11px] text-[#8d8d8d]"
				>
					<div
						class="flex items-center gap-1.5 {stats.memUsagePercent > 85
							? 'text-[#da1e28]'
							: stats.memUsagePercent > 70
								? 'text-[#f1c21b]'
								: 'text-[#24a148]'}"
					>
						<span
							class="h-1.5 w-1.5 {stats.memUsagePercent > 85
								? 'bg-[#da1e28]'
								: stats.memUsagePercent > 70
									? 'bg-[#f1c21b]'
									: 'bg-[#24a148]'}"
						></span>
						<span>{stats.memUsagePercent}% UTILIZED</span>
					</div>
					<span class="text-[#8d8d8d]">RAM POOL</span>
				</div>
			</div>

			<!-- Tile 4: Cluster Performance -->
			<div
				class="flex flex-col justify-between rounded-none border border-[#393939] bg-[#262626] p-6 transition-colors select-none hover:border-[#525252]"
			>
				<div>
					<div
						class="flex items-center justify-between font-sans text-xs font-semibold tracking-wider text-[#a8a8a8] uppercase"
					>
						<span>Cluster Performance</span>
						<Gauge class="h-4 w-4 text-[#8d8d8d]" />
					</div>
					<div class="mt-3 flex items-baseline gap-2">
						{#if stats.avgTps > 0}
							<span
								class="font-sans text-[32px] leading-none font-semibold tracking-tight tabular-nums {stats.avgTps >=
								18
									? 'text-[#24a148]'
									: 'text-[#da1e28]'}"
								use:countUp={{ value: stats.avgTps, decimals: 1 }}
							></span>
						{:else}
							<span
								class="font-sans text-[32px] leading-none font-semibold tracking-tight text-[#8d8d8d] tabular-nums"
								>—</span
							>
						{/if}
						<span class="font-sans text-sm text-[#8d8d8d]">AVG TPS</span>
					</div>
				</div>
				<div
					class="mt-3 flex items-center justify-between border-t border-[#393939] pt-3 font-sans text-[11px] text-[#8d8d8d]"
				>
					{#if stats.running === 0}
						<div class="flex items-center gap-1.5 text-[#8d8d8d]">
							<span class="h-1.5 w-1.5 bg-[#8d8d8d]"></span>
							<span>IDLE</span>
						</div>
					{:else if stats.avgTps <= 0}
						<div class="flex items-center gap-1.5 text-[#8d8d8d]">
							<span class="h-1.5 w-1.5 bg-[#8d8d8d]"></span>
							<span>AWAITING SAMPLE</span>
						</div>
					{:else}
						<div
							class="flex items-center gap-1.5 {stats.avgTps >= 18
								? 'text-[#24a148]'
								: 'text-[#da1e28]'}"
						>
							<span class="h-1.5 w-1.5 {stats.avgTps >= 18 ? 'bg-[#24a148]' : 'bg-[#da1e28]'}"
							></span>
							<span>{stats.avgTps >= 18 ? 'HEALTHY' : 'DEGRADED'}</span>
						</div>
					{/if}
					<span class="text-[#8d8d8d]">
						AVG CPU {stats.avgCpu > 0 ? `${stats.avgCpu.toFixed(0)}%` : '—'}
					</span>
				</div>
			</div>
		</div>

		<!-- Carbon Inline Notification (when servers need attention) -->
		{#if serversByStatus.critical.length > 0 || serversByStatus.warning.length > 0}
			<div
				class="rounded-none border-l-4 {serversByStatus.critical.length > 0
					? 'border-l-[#da1e28]'
					: 'border-l-[#f1c21b]'} flex flex-col justify-between gap-3 border-t border-r border-b border-[#393939] bg-[#262626] p-4 text-sm sm:flex-row sm:items-center"
			>
				<div class="flex items-center gap-3">
					<AlertTriangle
						class="h-5 w-5 {serversByStatus.critical.length > 0
							? 'text-[#da1e28]'
							: 'text-[#f1c21b]'} shrink-0"
					/>
					<div class="font-sans text-xs text-[#f4f4f4]">
						{#if serversByStatus.critical.length > 0}
							<span class="font-mono font-bold text-[#da1e28]"
								>{serversByStatus.critical.length} SERVER{serversByStatus.critical.length > 1
									? 'S'
									: ''} CRITICAL</span
							>: require operator intervention.
						{/if}
						{#if serversByStatus.warning.length > 0}
							<span
								class="font-mono font-bold text-[#f1c21b] {serversByStatus.critical.length > 0
									? 'ml-2'
									: ''}"
								>{serversByStatus.warning.length} SERVER{serversByStatus.warning.length > 1
									? 'S'
									: ''} RUNNING SLOW</span
							>: sub-optimal TPS detected.
						{/if}
					</div>
				</div>
				<a
					href={resolve('/servers')}
					class="mono-label-accent flex shrink-0 items-center gap-1 hover:text-white"
				>
					<span>Inspect Servers</span>
					<ChevronRight class="h-3.5 w-3.5" />
				</a>
			</div>
		{/if}

		<!-- Server list & Recent activity Grid -->
		<div class="grid gap-6 lg:grid-cols-7 [&>*]:min-w-0">
			<!-- Carbon DataTable: Server Overview (4 cols on lg) -->
			<div
				class="flex flex-col justify-between rounded-none border border-[#393939] bg-[#262626] lg:col-span-4"
			>
				<div>
					<!-- Table Toolbar / Header -->
					<div class="flex items-center justify-between border-b border-[#393939] bg-[#262626] p-4">
						<div>
							<h2 class="font-sans text-sm font-semibold tracking-wider text-[#f4f4f4] uppercase">
								Server Overview
							</h2>
							<p class="mt-0.5 font-sans text-xs text-[#8d8d8d]">
								Instance telemetry & state across cluster
							</p>
						</div>
						<a
							href={resolve('/servers')}
							class="mono-label-accent inline-flex h-8 items-center gap-1 px-3 transition-colors hover:bg-[#353535] hover:text-white"
						>
							<span>View All ({dashboardServers.length})</span>
							<ChevronRight class="h-3.5 w-3.5" />
						</a>
					</div>

					<!-- Table Content -->
					{#if dashboardServers.length === 0}
						<div class="px-4 py-12 text-center">
							<div
								class="mx-auto mb-3 flex h-10 w-10 items-center justify-center border border-[#393939] bg-[#161616]"
							>
								<Server class="h-5 w-5 text-[#8d8d8d]" />
							</div>
							<h3 class="text-sm font-semibold text-[#f4f4f4]">No servers configured</h3>
							<p class="mt-1 mb-4 font-mono text-xs text-[#8d8d8d]">
								Create your first Minecraft server instance
							</p>
							<CarbonButton
								kind="primary"
								size="sm"
								href={resolve('/servers/new')}
								class="inline-flex justify-center gap-2"
							>
								<Plus class="h-3.5 w-3.5" />
								<span>Create Server</span>
							</CarbonButton>
						</div>
					{:else}
						<div class="overflow-x-auto">
							<table class="w-full border-collapse text-left font-sans text-sm">
								<thead class="border-b border-[#525252] bg-[#393939] text-[#f4f4f4]">
									<tr>
										<th
											scope="col"
											class="px-4 py-2.5 text-xs font-semibold tracking-wider text-[#f4f4f4] uppercase"
											>Server</th
										>
										<th
											scope="col"
											class="px-4 py-2.5 text-xs font-semibold tracking-wider text-[#f4f4f4] uppercase"
											>Status</th
										>
										<th
											scope="col"
											class="hidden px-4 py-2.5 text-xs font-semibold tracking-wider text-[#f4f4f4] uppercase sm:table-cell"
											>Version</th
										>
										<th
											scope="col"
											class="px-4 py-2.5 text-xs font-semibold tracking-wider text-[#f4f4f4] uppercase"
											>Metrics</th
										>
										<th
											scope="col"
											class="px-4 py-2.5 text-right text-xs font-semibold tracking-wider text-[#f4f4f4] uppercase"
											>Actions</th
										>
									</tr>
								</thead>
								<tbody class="motion-stagger divide-y divide-[#393939] bg-[#262626] text-[#f4f4f4]">
									{#each sortServersByActivity( [...dashboardServers] ).slice(0, 5) as server (server.id)}
										<tr class="group transition-colors hover:bg-[#353535]">
											<td class="px-4 py-3 align-middle">
												<a
													href={resolve(`/servers/${server.id}`)}
													class="block transition-colors hover:text-[#78a9ff]"
												>
													<div class="text-sm font-medium text-[#f4f4f4]">{server.name}</div>
													<div class="font-mono text-xs text-[#8d8d8d]">
														{server.id.slice(0, 8)}
													</div>
												</a>
											</td>
											<td class="px-4 py-3 align-middle">
												<CarbonTag
													type={getStatusTagType(server.status)}
													size="sm"
													pulse={server.status !== ServerStatus.RUNNING &&
														server.status !== ServerStatus.STOPPED &&
														server.status !== ServerStatus.PAUSED &&
														server.status !== ServerStatus.DEEPSLEEP}
												>
													{serverStatusLabel(server.status)}
												</CarbonTag>
											</td>
											<td
												class="hidden px-4 py-3 align-middle font-mono text-xs text-[#c6c6c6] sm:table-cell"
											>
												{server.mcVersion || '—'}
											</td>
											<td class="px-4 py-3 align-middle">
												{#if server.status === ServerStatus.RUNNING}
													<div class="flex items-center gap-3 font-mono text-xs">
														<span class="flex items-center gap-1 text-[#c6c6c6]">
															<Users class="h-3.5 w-3.5 text-[#8d8d8d]" />
															{#if joinIsPlayable(server)}
																{server.playersOnline || 0}/{server.maxPlayers}
															{:else}
																{joinKindLabel(server.joinObservationKind)}
															{/if}
														</span>
														{#if server.tps}
															<span class="flex items-center gap-1 {getTpsColor(server.tps)}">
																<Zap class="h-3.5 w-3.5" />
																{server.tps.toFixed(1)}
															</span>
														{/if}
													</div>
												{:else if server.status === ServerStatus.DEEPSLEEP}
													<span class="font-mono text-xs text-[#a56eff]">DEEP SLEEP</span>
												{:else if server.status === ServerStatus.PAUSED}
													<span class="font-mono text-xs text-[#78a9ff]">PAUSED</span>
												{:else}
													<span class="font-mono text-xs text-[#8d8d8d]">OFFLINE</span>
												{/if}
											</td>
											<td class="px-4 py-3 text-right align-middle">
												<div class="flex items-center justify-end gap-1">
													{#if server.status === ServerStatus.STOPPED || server.status === ServerStatus.DEEPSLEEP}
														<button
															type="button"
															onclick={() => handleServerAction('start', server)}
															class="flex h-7 w-7 cursor-pointer items-center justify-center text-[#24a148] transition-colors hover:bg-[#393939] hover:text-[#42be65]"
															title={server.status === ServerStatus.DEEPSLEEP
																? 'Wake Server'
																: 'Start Server'}
															aria-label={server.status === ServerStatus.DEEPSLEEP
																? 'Wake Server'
																: 'Start Server'}
														>
															<PlayCircle class="h-4 w-4" />
														</button>
														{#if server.status === ServerStatus.DEEPSLEEP}
															<button
																type="button"
																onclick={() => handleServerAction('stop', server)}
																class="flex h-7 w-7 cursor-pointer items-center justify-center text-[#da1e28] transition-colors hover:bg-[#393939] hover:text-[#ff8389]"
																title="Stop Server"
																aria-label="Stop Server"
															>
																<StopCircle class="h-4 w-4" />
															</button>
														{/if}
													{:else if server.status === ServerStatus.PAUSED}
														<button
															type="button"
															onclick={() => handleServerAction('start', server)}
															class="flex h-7 w-7 cursor-pointer items-center justify-center text-[#4589ff] transition-colors hover:bg-[#393939] hover:text-[#78a9ff]"
															title="Resume Server"
															aria-label="Resume Server"
														>
															<PlayCircle class="h-4 w-4" />
														</button>
														<button
															type="button"
															onclick={() => handleServerAction('stop', server)}
															class="flex h-7 w-7 cursor-pointer items-center justify-center text-[#da1e28] transition-colors hover:bg-[#393939] hover:text-[#ff8389]"
															title="Stop Server"
															aria-label="Stop Server"
														>
															<StopCircle class="h-4 w-4" />
														</button>
													{:else if server.status === ServerStatus.RUNNING}
														<button
															type="button"
															onclick={() => handleServerAction('stop', server)}
															class="flex h-7 w-7 cursor-pointer items-center justify-center text-[#da1e28] transition-colors hover:bg-[#393939] hover:text-[#ff8389]"
															title="Stop Server"
															aria-label="Stop Server"
														>
															<StopCircle class="h-4 w-4" />
														</button>
													{/if}
													<a
														href={resolve(`/servers/${server.id}`)}
														class="mono-label-accent inline-flex h-7 items-center px-2.5 transition-colors hover:bg-[#393939] hover:text-white"
													>
														Manage
													</a>
												</div>
											</td>
										</tr>
									{/each}
								</tbody>
							</table>
						</div>
					{/if}
				</div>
			</div>

			<!-- Carbon Structured List: Recent Activity (3 cols on lg) -->
			<div class="flex flex-col rounded-none border border-[#393939] bg-[#262626] lg:col-span-3">
				<!-- Header -->
				<div class="flex items-center justify-between border-b border-[#393939] bg-[#262626] p-4">
					<div>
						<h2 class="font-sans text-sm font-semibold tracking-wider text-[#f4f4f4] uppercase">
							Recent Activity
						</h2>
						<p class="mt-0.5 font-sans text-xs text-[#8d8d8d]">
							Latest lifecycle and cluster events
						</p>
					</div>
					<Clock class="h-4 w-4 text-[#8d8d8d]" />
				</div>

				<!-- Content -->
				{#if recentActivity.length === 0}
					<div
						class="flex flex-1 flex-col items-center justify-center p-8 text-center text-[#8d8d8d]"
					>
						<Clock class="mb-2 h-8 w-8 text-[#525252]" />
						<p class="mono-label">No recent events recorded</p>
					</div>
				{:else}
					<!-- Structured List Header -->
					<div
						class="flex items-center justify-between border-b border-[#525252] bg-[#393939] px-4 py-2.5 text-xs font-semibold tracking-wider text-[#f4f4f4] uppercase"
					>
						<span>Event / Instance</span>
						<span>Timestamp</span>
					</div>
					<!-- Structured List Rows -->
					<div class="motion-stagger flex-1 divide-y divide-[#393939] bg-[#262626]">
						{#each recentActivity as activity (activity.server + (activity.time?.seconds ?? ''))}
							<div
								class="flex items-center justify-between px-4 py-3 transition-colors hover:bg-[#353535]"
							>
								<div class="flex min-w-0 items-center gap-3">
									<!-- Sharp status pip -->
									<div
										role="img"
										aria-label={serverStatusLabel(activity.status)}
										title={serverStatusLabel(activity.status)}
										class="h-2 w-2 shrink-0 {statusPipColor[
											getStatusTagType(activity.status)
										]} {activity.status === ServerStatus.ERROR ||
										activity.status === ServerStatus.UNHEALTHY
											? 'motion-pulse'
											: ''}"
									></div>
									<div class="min-w-0">
										<p class="truncate text-sm font-medium text-[#f4f4f4]">{activity.server}</p>
										<p class="font-mono text-xs text-[#8d8d8d]">{activity.action}</p>
									</div>
								</div>
								<div class="shrink-0 pl-3 font-mono text-xs text-[#8d8d8d]">
									{formatUptime(activity.time)} ago
								</div>
							</div>
						{/each}
					</div>
				{/if}
			</div>
		</div>

		{#if dashboardNodes.length > 0}
			<div class="rounded-none border border-[#393939] bg-[#262626]" data-testid="node-section">
				<div
					class="flex flex-col justify-between gap-3 border-b border-[#393939] p-4 sm:flex-row sm:items-center"
				>
					<div>
						<h2 class="font-sans text-sm font-semibold tracking-wider text-[#f4f4f4] uppercase">
							Nodes
						</h2>
						<p class="mt-0.5 font-sans text-xs text-[#8d8d8d]">Docker hosts backing this cluster</p>
					</div>
					<CarbonTag type={nodeStats.offline > 0 ? 'yellow' : 'green'} size="sm">
						{nodeStats.online}/{nodeStats.total} ONLINE
					</CarbonTag>
				</div>
				<div class="divide-y divide-[#393939]">
					{#each dashboardNodes as node (node.id)}
						<div
							class="flex flex-col gap-2 px-4 py-3 sm:flex-row sm:items-center sm:justify-between"
							data-testid="node-row"
						>
							<div class="flex min-w-0 items-center gap-2">
								<span class="truncate text-sm font-medium text-[#f4f4f4]">{node.name}</span>
								{#if node.isLocal}
									<CarbonTag type="blue" size="sm">LOCAL</CarbonTag>
								{/if}
								<CarbonTag type={getNodeTagType(node.status)} size="sm">
									{describeNodeStatusLabel(node.status)}
								</CarbonTag>
							</div>
							<div class="flex items-center gap-4 font-sans text-[11px] text-[#8d8d8d]">
								<span class="tabular-nums">
									{node.runningCount ?? 0}/{node.serverCount ?? 0} SERVERS
								</span>
								<span class="tabular-nums">{formatNodeMemory(node)}</span>
							</div>
						</div>
					{/each}
				</div>
			</div>
		{/if}

		<!-- Carbon System Health Strip -->
		<div class="rounded-none border border-[#393939] bg-[#262626]">
			<div
				class="flex flex-col justify-between gap-3 border-b border-[#393939] p-4 sm:flex-row sm:items-center"
			>
				<div>
					<h2 class="font-sans text-sm font-semibold tracking-wider text-[#f4f4f4] uppercase">
						System Health
					</h2>
					<p class="mt-0.5 font-sans text-xs text-[#8d8d8d]">Underlying daemon and network state</p>
				</div>
				<CarbonTag
					type={serversByStatus.critical.length > 0
						? 'red'
						: serversByStatus.warning.length > 0
							? 'yellow'
							: 'green'}
					size="sm"
				>
					{serversByStatus.critical.length > 0
						? 'ATTENTION REQUIRED'
						: serversByStatus.warning.length > 0
							? 'DEGRADED'
							: 'OPERATIONAL'}
				</CarbonTag>
			</div>
			<div
				class="grid grid-cols-1 divide-y divide-[#393939] sm:grid-cols-2 sm:divide-y-0 lg:grid-cols-4"
			>
				<div class="flex items-center justify-between gap-3 px-4 py-3">
					<span class="mono-label"> RPC Telemetry </span>
					<CarbonTag type="green" size="sm">OPERATIONAL</CarbonTag>
				</div>
				<div class="flex items-center justify-between gap-3 px-4 py-3">
					<span class="mono-label"> Network Proxy </span>
					<CarbonTag type={stats.running > 0 ? 'green' : 'gray'} size="sm">
						{stats.running > 0 ? 'CONNECTED' : 'IDLE'}
					</CarbonTag>
				</div>
				<div class="flex items-center justify-between gap-3 px-4 py-3">
					<span class="mono-label"> Storage Allocation </span>
					<span class="font-mono text-xs text-[#f4f4f4]">
						{stats.totalDiskUsage > 0
							? `${formatBytes(stats.totalDiskUsage)}${stats.totalDiskSize}`
							: 'LOCAL VOLUMES'}
					</span>
				</div>
				<div class="flex items-center justify-between gap-3 px-4 py-3" data-testid="health-nodes">
					<span class="mono-label">Nodes</span>
					{#if nodeStats.total > 0}
						<CarbonTag type={nodeStats.offline > 0 ? 'yellow' : 'green'} size="sm">
							{nodeStats.online}/{nodeStats.total} ONLINE
						</CarbonTag>
					{:else}
						<CarbonTag type="gray" size="sm">NO DATA</CarbonTag>
					{/if}
				</div>
			</div>
		</div>
	</div>
{/if}
