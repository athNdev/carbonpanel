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
	import { formatBytes } from '$lib/utils';
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
	import { summarizeNodes } from '$lib/utils/node-status';
	import { formatTimestamp } from '$lib/utils/format-time';
	import { summarizeDashboardTelemetry } from '$lib/features/servers/dashboard-telemetry';
	import { joinIsPlayable, joinKindLabel } from '$lib/server-join';
	import {
		CarbonButton,
		CarbonTag,
		CarbonErrorState,
		CarbonPageHeader
	} from '$lib/components/carbon';

	// Dashboard data
	let dashboardServers = $derived($serversStore);
	let dashboardLoadGeneration = 0;
	let mounted = false;
	let dashboardNodes: NodeType[] = $state([]);
	let isLoading = $state(true);
	let loadError = $state<string | null>(null);
	let isRefreshing = $state(false);

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
			const outcome = await serverActions.perform(server.id, action);
			if (outcome.kind === 'superseded') return;
			toast.success(`${action === 'start' ? 'Starting' : 'Stopping'} ${server.name}…`);
		} catch (error) {
			toast.error(
				`Failed to ${action} server: ${error instanceof Error ? error.message : 'Unknown error'}`
			);
		}
	}

	let telemetry = $derived(summarizeDashboardTelemetry(dashboardServers));
	let memoryUsageMiB = $derived(telemetry.memory.usageMiB);
	let stats = $derived({
		total: dashboardServers.length,
		running: telemetry.running,
		stopped: dashboardServers.filter((s) => s.status === ServerStatus.STOPPED).length,
		error: dashboardServers.filter((s) => s.status === ServerStatus.ERROR).length,
		warning: dashboardServers.filter((s) => s.status === ServerStatus.UNHEALTHY).length,
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
		activeRatio: dashboardServers.length
			? Math.round((telemetry.running / dashboardServers.length) * 100)
			: 0
	});

	let recentStarts = $derived(
		dashboardServers
			.filter((server) => server.lastStarted && server.lastStarted.seconds > 0n)
			.sort(
				(a, b) =>
					Number(b.lastStarted!.seconds - a.lastStarted!.seconds) ||
					b.lastStarted!.nanos - a.lastStarted!.nanos
			)
			.slice(0, 5)
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

	let nodeStats = $derived(summarizeNodes(dashboardNodes));

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

		return () => {
			mounted = false;
			++dashboardLoadGeneration;
		};
	});

	// Status pip colours derive from getStatusTagType so a transient state
	// (starting, sleeping, paused) is never shown as an error red. Only
	// ERROR is red; UNHEALTHY is a yellow degraded state, and deep sleep and
	// other transients are purple.
	const statusPipColor: Record<string, string> = {
		green: 'bg-success',
		red: 'bg-destructive',
		yellow: 'bg-warning',
		purple: 'bg-[var(--chart-4)]',
		blue: 'bg-primary',
		gray: 'bg-muted-foreground',
		'warm-gray': 'bg-muted-foreground'
	};

	const getTpsColor = (tps: number | undefined) => {
		if (!tps) return 'text-muted-foreground';
		if (tps >= 19) return 'text-success-text';
		if (tps >= 17) return 'text-warning-text';
		return 'text-destructive-text';
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
	<div class="space-y-6 bg-background text-foreground" role="status" aria-label="Loading dashboard">
		<div
			class="flex flex-col justify-between gap-4 border-b border-border pb-4 sm:flex-row sm:items-center"
		>
			<div class="w-full">
				<div class="motion-skeleton h-8 w-56 bg-card"></div>
				<div class="motion-skeleton mt-2 h-3 w-80 max-w-full bg-card"></div>
			</div>
			<div class="flex items-center gap-2">
				<div class="motion-skeleton h-8 w-28 bg-card"></div>
				<div class="motion-skeleton h-8 w-32 bg-card"></div>
			</div>
		</div>
		<div class="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
			{#each [0, 1, 2, 3] as _ (_)}
				<div
					class="flex h-[140px] flex-col justify-between rounded-none border border-border bg-card p-6 select-none"
				>
					<div>
						<div class="flex items-center justify-between">
							<div class="motion-skeleton h-3 w-24 bg-secondary"></div>
							<div class="motion-skeleton h-4 w-4 bg-secondary"></div>
						</div>
						<div class="motion-skeleton mt-3 h-8 w-20 bg-secondary"></div>
					</div>
					<div class="mt-3 flex items-center justify-between border-t border-border pt-3">
						<div class="motion-skeleton h-3 w-20 bg-secondary"></div>
						<div class="motion-skeleton h-3 w-12 bg-secondary"></div>
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
	<div class="space-y-6 bg-background text-foreground" data-testid="showcase-dashboard-content">
		<CarbonPageHeader
			title="Cluster Overview"
			description="Hardware utilization, server status, and routing topology"
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
					<span>Create server</span>
				</CarbonButton>
			{/snippet}
		</CarbonPageHeader>

		<!-- Carbon KPI Tiles -->
		<div
			class="motion-stagger-fade grid gap-4 md:grid-cols-2 xl:grid-cols-4 [&>*]:min-w-0"
			data-testid="showcase-kpi-tiles"
		>
			<!-- Tile 1: Total Servers -->
			<div
				class="flex flex-col justify-between rounded-none border border-border bg-card p-6 transition-colors select-none hover:border-foreground/30"
			>
				<div>
					<div
						class="flex items-center justify-between font-sans text-xs font-semibold tracking-wider text-muted-foreground uppercase"
					>
						<span>Total Servers</span>
						<Server class="h-4 w-4 text-muted-foreground" />
					</div>
					<div
						class="mt-3 font-sans text-[32px] leading-none font-semibold tracking-tight text-foreground tabular-nums"
					>
						<span use:countUp={{ value: stats.total, decimals: 0 }}></span>
					</div>
				</div>
				<div
					class="mt-3 flex flex-wrap items-start justify-between gap-x-3 gap-y-1 border-t border-border pt-3 font-sans text-[11px] text-muted-foreground"
				>
					<div class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
						<div class="flex items-center gap-1.5 text-success-text">
							<span class="h-1.5 w-1.5 bg-success"></span>
							<span>{stats.running} ACTIVE</span>
						</div>
						{#if stats.error > 0}
							<div class="flex items-center gap-1.5 text-destructive-text">
								<span class="h-1.5 w-1.5 bg-destructive"></span>
								<span>{stats.error} ISSUES</span>
							</div>
						{:else if stats.warning > 0}
							<div class="flex items-center gap-1.5 text-warning-text">
								<span class="h-1.5 w-1.5 bg-warning"></span>
								<span>{stats.warning} DEGRADED</span>
							</div>
						{:else}
							<span class="text-muted-foreground">{stats.stopped} STOPPED</span>
						{/if}
					</div>
					<span class="text-muted-foreground">RATIO {stats.activeRatio}%</span>
				</div>
			</div>

			<!-- Tile 2: Active Players -->
			<div
				class="flex flex-col justify-between rounded-none border border-border bg-card p-6 transition-colors select-none hover:border-foreground/30"
			>
				<div>
					<div
						class="flex items-center justify-between font-sans text-xs font-semibold tracking-wider text-muted-foreground uppercase"
					>
						<span>Active Players</span>
						<Users class="h-4 w-4 text-muted-foreground" />
					</div>
					<div
						class="mt-3 font-sans text-[32px] leading-none font-semibold tracking-tight text-foreground tabular-nums"
					>
						{#if telemetry.players.online === null}
							<span class="text-muted-foreground">—</span>
						{:else}
							<span use:countUp={{ value: telemetry.players.online, decimals: 0 }}></span>
						{/if}
					</div>
				</div>
				<div
					class="mt-3 flex flex-wrap items-start justify-between gap-x-3 gap-y-1 border-t border-border pt-3 font-sans text-[11px] text-muted-foreground"
				>
					{#if telemetry.running === 0}
						<span>No running servers</span>
					{:else if telemetry.players.online === null}
						<span>Player count unavailable</span>
					{:else if telemetry.players.reporting < telemetry.running}
						<span>{telemetry.players.reporting} of {telemetry.running} servers reporting</span>
					{:else}
						<span>All running servers reporting</span>
					{/if}
					{#if telemetry.players.capacity > 0}
						<span>{telemetry.players.capacity} slots configured</span>
					{/if}
				</div>
			</div>

			<!-- Tile 3: Memory Usage -->
			<div
				class="flex flex-col justify-between rounded-none border border-border bg-card p-6 transition-colors select-none hover:border-foreground/30"
			>
				<div>
					<div
						class="flex items-center justify-between font-sans text-xs font-semibold tracking-wider text-muted-foreground uppercase"
					>
						<span>Memory Usage</span>
						<MemoryStick class="h-4 w-4 text-muted-foreground" />
					</div>
					<div class="mt-3 flex items-baseline gap-1.5">
						{#if memoryUsageMiB === null}
							<span class="font-sans text-[32px] leading-none font-semibold text-muted-foreground"
								>—</span
							>
						{:else}
							{#key memoryUsageMiB >= 1024}
								<span
									class="font-sans text-[32px] leading-none font-semibold tracking-tight text-foreground tabular-nums"
									use:countUp={{
										value: memoryUsageMiB >= 1024 ? memoryUsageMiB / 1024 : memoryUsageMiB,
										decimals: memoryUsageMiB >= 1024 ? 1 : 0
									}}
								></span>
								<span class="font-sans text-sm text-muted-foreground"
									>{memoryUsageMiB >= 1024 ? 'GiB' : 'MiB'}</span
								>
							{/key}
						{/if}
					</div>
					{#if telemetry.memory.reportedUtilization !== null}
						<div class="telemetry-track mt-3" aria-hidden="true">
							<div
								class="h-full bg-accent-text transition-[width] duration-[var(--motion-slow)] ease-[var(--ease-out-quart)]"
								style="width: {Math.min(telemetry.memory.reportedUtilization, 100)}%"
							></div>
						</div>
					{/if}
				</div>
				<div
					class="mt-3 flex flex-wrap items-start justify-between gap-x-3 gap-y-1 border-t border-border pt-3 font-sans text-[11px] text-muted-foreground"
				>
					{#if telemetry.memory.reportedUtilization !== null}
						<span
							class={telemetry.memory.reportedUtilization > 85
								? 'text-destructive-text'
								: telemetry.memory.reportedUtilization > 70
									? 'text-warning-text'
									: 'text-success-text'}
							>{telemetry.memory.reportedUtilization}% of reporting limits</span
						>
					{:else if telemetry.memory.usageMiB === null}
						<span>{telemetry.running ? 'Usage unavailable' : 'No running servers'}</span>
					{:else}
						<span>Reporting limits unavailable</span>
					{/if}
					{#if telemetry.memory.reporting > 0 && telemetry.memory.reporting < telemetry.running}
						<span>{telemetry.memory.reporting} of {telemetry.running} servers reporting</span>
					{/if}
					{#if telemetry.memory.allocatedMiB > 0}
						<span>
							{telemetry.memory.allocatedMiB >= 1024
								? `${(telemetry.memory.allocatedMiB / 1024).toFixed(1)} GiB`
								: `${telemetry.memory.allocatedMiB} MiB`} allocated
						</span>
					{/if}
				</div>
			</div>

			<!-- Tile 4: Cluster Performance -->
			<div
				class="flex flex-col justify-between rounded-none border border-border bg-card p-6 transition-colors select-none hover:border-foreground/30"
			>
				<div>
					<div
						class="flex items-center justify-between font-sans text-xs font-semibold tracking-wider text-muted-foreground uppercase"
					>
						<span>Cluster Performance</span>
						<Gauge class="h-4 w-4 text-muted-foreground" />
					</div>
					<div class="mt-3 flex items-baseline gap-2">
						{#if stats.avgTps > 0}
							<span
								class="font-sans text-[32px] leading-none font-semibold tracking-tight tabular-nums {stats.avgTps >=
								18
									? 'text-success-text'
									: 'text-destructive-text'}"
								use:countUp={{ value: stats.avgTps, decimals: 1 }}
							></span>
						{:else}
							<span
								class="font-sans text-[32px] leading-none font-semibold tracking-tight text-muted-foreground tabular-nums"
								>—</span
							>
						{/if}
						<span class="font-sans text-sm text-muted-foreground">AVG TPS</span>
					</div>
				</div>
				<div
					class="mt-3 flex flex-wrap items-start justify-between gap-x-3 gap-y-1 border-t border-border pt-3 font-sans text-[11px] text-muted-foreground"
				>
					{#if stats.running === 0}
						<div class="flex items-center gap-1.5 text-muted-foreground">
							<span class="h-1.5 w-1.5 bg-muted-foreground"></span>
							<span>IDLE</span>
						</div>
					{:else if stats.avgTps <= 0}
						<div class="flex items-center gap-1.5 text-muted-foreground">
							<span class="h-1.5 w-1.5 bg-muted-foreground"></span>
							<span>AWAITING SAMPLE</span>
						</div>
					{:else}
						<div
							class="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5 {stats.avgTps >= 18
								? 'text-success-text'
								: 'text-destructive-text'}"
						>
							<span class="h-1.5 w-1.5 {stats.avgTps >= 18 ? 'bg-success' : 'bg-destructive'}"
							></span>
							<span>{stats.avgTps >= 18 ? 'HEALTHY' : 'DEGRADED'}</span>
						</div>
					{/if}
					<span class="text-muted-foreground">
						AVG CPU {stats.avgCpu > 0 ? `${stats.avgCpu.toFixed(0)}%` : '—'}
					</span>
				</div>
			</div>
		</div>

		<!-- Carbon Inline Notification (when servers need attention) -->
		{#if serversByStatus.critical.length > 0 || serversByStatus.warning.length > 0}
			<div
				class="rounded-none border-l-4 {serversByStatus.critical.length > 0
					? 'border-l-destructive'
					: 'border-l-warning'} flex flex-col justify-between gap-3 border-t border-r border-b border-border bg-card p-4 text-sm sm:flex-row sm:items-center"
			>
				<div class="flex items-center gap-3">
					<AlertTriangle
						class="h-5 w-5 {serversByStatus.critical.length > 0
							? 'text-destructive-text'
							: 'text-warning-text'} shrink-0"
					/>
					<div class="space-y-1 text-sm leading-snug">
						{#if serversByStatus.critical.length > 0}
							<p class="font-semibold text-destructive-text">
								{serversByStatus.critical.length}
								{serversByStatus.critical.length === 1 ? 'server needs' : 'servers need'} attention
							</p>
							<p class="text-muted-foreground">Check the affected servers for errors or low TPS.</p>
						{/if}
						{#if serversByStatus.warning.length > 0}
							<p class="font-medium text-warning-text">
								{serversByStatus.warning.length}
								{serversByStatus.warning.length === 1 ? 'server is' : 'servers are'} reporting degraded
								performance
							</p>
						{/if}
					</div>
				</div>
				<a
					href={resolve('/servers')}
					class="focus-ring flex shrink-0 items-center gap-1 text-sm font-medium text-accent-text hover:underline"
				>
					<span>Inspect Servers</span>
					<ChevronRight class="h-3.5 w-3.5" />
				</a>
			</div>
		{/if}

		<!-- Server list & Recent activity Grid -->
		<div class="grid gap-6 2xl:grid-cols-7 [&>*]:min-w-0">
			<!-- Carbon DataTable: Server Overview (4 cols on lg) -->
			<div
				class="flex flex-col justify-between rounded-none border border-border bg-card 2xl:col-span-4"
			>
				<div>
					<!-- Table Toolbar / Header -->
					<div class="flex items-center justify-between border-b border-border bg-card p-4">
						<div>
							<h2 class="font-sans text-base font-semibold text-foreground">Server Overview</h2>
							<p class="mt-0.5 font-sans text-xs text-muted-foreground">
								Server telemetry and state across the cluster
							</p>
						</div>
						<a
							href={resolve('/servers')}
							class="mono-label-accent inline-flex h-8 items-center gap-1 px-3 transition-colors hover:bg-accent hover:text-foreground"
						>
							<span>View All ({dashboardServers.length})</span>
							<ChevronRight class="h-3.5 w-3.5" />
						</a>
					</div>

					<!-- Table Content -->
					{#if dashboardServers.length === 0}
						<div class="px-4 py-12 text-center">
							<div
								class="mx-auto mb-3 flex h-10 w-10 items-center justify-center border border-border bg-background"
							>
								<Server class="h-5 w-5 text-muted-foreground" />
							</div>
							<h3 class="text-sm font-semibold text-foreground">No servers configured</h3>
							<p class="mt-1 mb-4 font-mono text-xs text-muted-foreground">
								Create your first Minecraft server
							</p>
							<CarbonButton
								kind="primary"
								size="sm"
								href={resolve('/servers/new')}
								class="inline-flex justify-center gap-2"
							>
								<Plus class="h-3.5 w-3.5" />
								<span>Create server</span>
							</CarbonButton>
						</div>
					{:else}
						<div
							class="fleet-table-scroll focus-ring"
							role="region"
							aria-label="Server overview table"
							tabindex="0"
						>
							<table class="w-full border-collapse text-left font-sans text-sm">
								<thead class="border-b border-border bg-secondary text-foreground">
									<tr>
										<th
											scope="col"
											class="px-4 py-2.5 text-xs font-semibold tracking-wider text-foreground uppercase"
											>Server</th
										>
										<th
											scope="col"
											class="px-4 py-2.5 text-xs font-semibold tracking-wider text-foreground uppercase"
											>Status</th
										>
										<th
											scope="col"
											class="hidden px-4 py-2.5 text-xs font-semibold tracking-wider text-foreground uppercase sm:table-cell"
											>Version</th
										>
										<th
											scope="col"
											class="px-4 py-2.5 text-xs font-semibold tracking-wider text-foreground uppercase"
											>Metrics</th
										>
										<th
											scope="col"
											class="px-4 py-2.5 text-right text-xs font-semibold tracking-wider text-foreground uppercase"
											>Actions</th
										>
									</tr>
								</thead>
								<tbody class="motion-stagger divide-y divide-border bg-card text-foreground">
									{#each sortServersByActivity( [...dashboardServers] ).slice(0, 5) as server (server.id)}
										<tr class="group transition-colors hover:bg-accent">
											<td class="px-4 py-3 align-middle">
												<a
													href={resolve('/servers/[id]', { id: server.id })}
													class="block transition-colors hover:text-accent-text"
												>
													<div class="text-sm font-medium text-foreground">{server.name}</div>
													<div class="font-mono text-xs text-muted-foreground">
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
												class="hidden px-4 py-3 align-middle font-mono text-xs text-muted-foreground sm:table-cell"
											>
												{server.mcVersion || '—'}
											</td>
											<td class="px-4 py-3 align-middle">
												{#if server.status === ServerStatus.RUNNING}
													<div class="flex items-center gap-3 font-mono text-xs">
														<span class="flex items-center gap-1 text-muted-foreground">
															<Users class="h-3.5 w-3.5 text-muted-foreground" />
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
													<span class="font-mono text-xs text-[var(--chart-4)]">DEEP SLEEP</span>
												{:else if server.status === ServerStatus.PAUSED}
													<span class="font-mono text-xs text-accent-text">PAUSED</span>
												{:else}
													<span class="font-mono text-xs text-muted-foreground">OFFLINE</span>
												{/if}
											</td>
											<td class="px-4 py-3 text-right align-middle">
												<div class="flex items-center justify-end gap-1">
													{#if server.status === ServerStatus.STOPPED || server.status === ServerStatus.DEEPSLEEP}
														<button
															type="button"
															onclick={() => handleServerAction('start', server)}
															class="flex h-7 w-7 cursor-pointer items-center justify-center text-success-text transition-colors hover:bg-secondary hover:text-success-text"
															title={server.status === ServerStatus.DEEPSLEEP ? 'Wake' : 'Start'}
															aria-label={(server.status === ServerStatus.DEEPSLEEP
																? 'Wake '
																: 'Start ') + server.name}
														>
															<PlayCircle class="h-4 w-4" />
														</button>
														{#if server.status === ServerStatus.DEEPSLEEP}
															<button
																type="button"
																onclick={() => handleServerAction('stop', server)}
																class="flex h-7 w-7 cursor-pointer items-center justify-center text-destructive-text transition-colors hover:bg-secondary hover:text-destructive-text"
																title="Stop"
																aria-label={'Stop ' + server.name}
															>
																<StopCircle class="h-4 w-4" />
															</button>
														{/if}
													{:else if server.status === ServerStatus.PAUSED}
														<button
															type="button"
															onclick={() => handleServerAction('start', server)}
															class="flex h-7 w-7 cursor-pointer items-center justify-center text-accent-text transition-colors hover:bg-secondary hover:text-accent-text"
															title="Resume"
															aria-label={'Resume ' + server.name}
														>
															<PlayCircle class="h-4 w-4" />
														</button>
														<button
															type="button"
															onclick={() => handleServerAction('stop', server)}
															class="flex h-7 w-7 cursor-pointer items-center justify-center text-destructive-text transition-colors hover:bg-secondary hover:text-destructive-text"
															title="Stop"
															aria-label={'Stop ' + server.name}
														>
															<StopCircle class="h-4 w-4" />
														</button>
													{:else if server.status === ServerStatus.RUNNING}
														<button
															type="button"
															onclick={() => handleServerAction('stop', server)}
															class="flex h-7 w-7 cursor-pointer items-center justify-center text-destructive-text transition-colors hover:bg-secondary hover:text-destructive-text"
															title="Stop"
															aria-label={'Stop ' + server.name}
														>
															<StopCircle class="h-4 w-4" />
														</button>
													{/if}
													<a
														href={resolve('/servers/[id]', { id: server.id })}
														aria-label={'Manage ' + server.name}
														class="mono-label-accent inline-flex h-7 items-center px-2.5 transition-colors hover:bg-secondary hover:text-foreground"
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

			<!-- Recent server starts -->
			<div class="flex flex-col rounded-none border border-border bg-card 2xl:col-span-3">
				<!-- Header -->
				<div class="flex items-center justify-between border-b border-border bg-card p-4">
					<div>
						<h2 class="font-sans text-base font-semibold text-foreground">Recent server starts</h2>
					</div>
					<Clock class="h-4 w-4 text-muted-foreground" />
				</div>

				<!-- Content -->
				{#if recentStarts.length === 0}
					<div
						class="flex flex-1 flex-col items-center justify-center p-8 text-center text-muted-foreground"
					>
						<Clock class="mb-2 h-8 w-8 text-muted-foreground" />
						<p class="text-sm">No server starts recorded</p>
					</div>
				{:else}
					<!-- Structured List Header -->
					<div
						class="flex items-center justify-between border-b border-border bg-secondary px-4 py-2.5 text-xs font-semibold tracking-wider text-foreground uppercase"
					>
						<span>Server</span>
						<span>Last started</span>
					</div>
					<!-- Structured List Rows -->
					<div class="motion-stagger flex-1 divide-y divide-border bg-card">
						{#each recentStarts as server (server.id)}
							<div
								class="flex items-center justify-between px-4 py-3 transition-colors hover:bg-accent"
							>
								<div class="flex min-w-0 items-center gap-3">
									<!-- Sharp status pip -->
									<div
										role="img"
										aria-label={serverStatusLabel(server.status)}
										title={serverStatusLabel(server.status)}
										class="h-2 w-2 shrink-0 {statusPipColor[
											getStatusTagType(server.status)
										]} {server.status === ServerStatus.ERROR ||
										server.status === ServerStatus.UNHEALTHY
											? 'motion-pulse'
											: ''}"
									></div>
									<div class="min-w-0">
										<p class="truncate text-sm font-medium text-foreground">{server.name}</p>
									</div>
								</div>
								<div class="shrink-0 pl-3 font-mono text-xs text-muted-foreground">
									{formatTimestamp(server.lastStarted)}
								</div>
							</div>
						{/each}
					</div>
				{/if}
			</div>
		</div>

		{#if dashboardNodes.length > 0}
			<div class="rounded-none border border-border bg-card" data-testid="node-section">
				<div
					class="flex flex-col justify-between gap-3 border-b border-border p-4 sm:flex-row sm:items-center"
				>
					<div>
						<h2 class="font-sans text-base font-semibold text-foreground">Nodes</h2>
						<p class="mt-0.5 font-sans text-xs text-muted-foreground">
							Docker hosts backing this cluster
						</p>
					</div>
					<CarbonTag
						type={nodeStats.offline > 0 ? 'yellow' : 'green'}
						size="sm"
						class="self-start sm:self-auto"
					>
						{nodeStats.online}/{nodeStats.total} ONLINE
					</CarbonTag>
				</div>
				<div class="divide-y divide-border">
					{#each dashboardNodes as node (node.id)}
						<div
							class="flex flex-col gap-2 px-4 py-3 sm:flex-row sm:items-center sm:justify-between"
							data-testid="node-row"
						>
							<div class="flex min-w-0 items-center gap-2">
								<span class="truncate text-sm font-medium text-foreground">{node.name}</span>
								{#if node.isLocal}
									<CarbonTag type="blue" size="sm">LOCAL</CarbonTag>
								{/if}
								<CarbonTag type={getNodeTagType(node.status)} size="sm">
									{describeNodeStatusLabel(node.status)}
								</CarbonTag>
							</div>
							<div class="flex items-center gap-4 font-sans text-[11px] text-muted-foreground">
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
		<div class="rounded-none border border-border bg-card">
			<div
				class="flex flex-col justify-between gap-3 border-b border-border p-4 sm:flex-row sm:items-center"
			>
				<div>
					<h2 class="font-sans text-base font-semibold text-foreground">System Health</h2>
					<p class="mt-0.5 font-sans text-xs text-muted-foreground">
						Underlying daemon and network state
					</p>
				</div>
				<CarbonTag
					type={serversByStatus.critical.length > 0
						? 'red'
						: serversByStatus.warning.length > 0
							? 'yellow'
							: 'green'}
					size="sm"
					class="self-start sm:self-auto"
				>
					{serversByStatus.critical.length > 0
						? 'ATTENTION REQUIRED'
						: serversByStatus.warning.length > 0
							? 'DEGRADED'
							: 'OPERATIONAL'}
				</CarbonTag>
			</div>
			<div class="grid grid-cols-[repeat(auto-fit,minmax(min(100%,15rem),1fr))] gap-px bg-border">
				<div class="flex min-w-0 items-center justify-between gap-3 bg-card px-4 py-3">
					<span class="text-sm text-muted-foreground">RPC telemetry</span>
					<CarbonTag type="gray" size="sm">NOT MEASURED</CarbonTag>
				</div>
				<div class="flex min-w-0 items-center justify-between gap-3 bg-card px-4 py-3">
					<span class="text-sm text-muted-foreground">Network proxy</span>
					<CarbonTag type={stats.running > 0 ? 'green' : 'gray'} size="sm">
						{stats.running > 0 ? 'CONNECTED' : 'IDLE'}
					</CarbonTag>
				</div>
				<div class="flex min-w-0 items-center justify-between gap-3 bg-card px-4 py-3">
					<span class="text-sm text-muted-foreground">Storage allocation</span>
					<span class="text-right text-sm font-medium text-foreground">
						{stats.totalDiskUsage > 0
							? `${formatBytes(stats.totalDiskUsage)}${stats.totalDiskSize}`
							: 'Local volumes'}
					</span>
				</div>
				<div
					class="flex min-w-0 items-center justify-between gap-3 bg-card px-4 py-3"
					data-testid="health-nodes"
				>
					<span class="text-sm text-muted-foreground">Nodes</span>
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
