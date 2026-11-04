<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { onMount } from 'svelte';
	import { rpcClient } from '$lib/api/rpc-client';
	import { apiFetch } from '$lib/api/fetch';
	import { NodeStatus, type Node } from '$lib/proto/carbonpanel/v1/node_pb';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Button } from '$lib/components/ui/button';
	import { Switch } from '$lib/components/ui/switch';
	import { toast } from 'svelte-sonner';
	import {
		CarbonButton,
		CarbonConfirm,
		CarbonInlineLoading,
		CarbonTag,
		CarbonTile
	} from '$lib/components/carbon';
	import {
		Server,
		Plus,
		Trash2,
		Loader2,
		Activity,
		CheckCircle2,
		RefreshCw,
		Layers,
		Edit,
		Radio
	} from '@lucide/svelte';
	import {
		Dialog,
		DialogContent,
		DialogDescription,
		DialogFooter,
		DialogHeader,
		DialogTitle
	} from '$lib/components/ui/dialog';
	import { Textarea } from '$lib/components/ui/textarea';

	let loading = $state(true);
	let nodes = $state<Node[]>([]);
	let pingingNodeId = $state<string | null>(null);
	let pingLatencies = $state<
		Record<string, { latency: number; status: NodeStatus; message?: string }>
	>({});
	let scanning = $state(false);

	// Detected-daemons dialog state (auto-detect results pending registration).
	type DetectedDaemon = {
		host: string;
		latencyMs: number;
		source: string;
		alreadyRegistered: boolean;
		name: string;
		advertisedIp: string;
	};
	let showDetectedDialog = $state(false);
	let detectedDaemons = $state<DetectedDaemon[]>([]);
	let addingDaemonHost = $state<string | null>(null);

	// Dialog state
	let showAddDialog = $state(false);
	let showEditDialog = $state(false);
	let isSaving = $state(false);

	let newNode = $state({
		name: '',
		host: 'tcp://',
		advertisedIp: '',
		tlsEnabled: false,
		tlsSkipVerify: false,
		tlsCaCert: '',
		tlsCert: '',
		tlsKey: '',
		maxMemoryMb: 0,
		maxServers: 0,
		enabled: true
	});

	let editingNode = $state<{
		id: string;
		name: string;
		host: string;
		advertisedIp: string;
		tlsEnabled: boolean;
		tlsSkipVerify: boolean;
		tlsCaCert: string;
		tlsCert: string;
		tlsKey: string;
		maxMemoryMb: number;
		maxServers: number;
		enabled: boolean;
	} | null>(null);

	onMount(() => {
		loadNodes();
	});

	async function loadNodes() {
		loading = true;
		try {
			const res = await rpcClient.node.listNodes({});
			nodes = res.nodes;
		} catch (error) {
			console.error('Failed to load nodes:', error);
			toast.error('Failed to load Docker nodes');
		} finally {
			loading = false;
		}
	}

	// Normalize a candidate host for comparison against registered node hosts
	// (unix socket paths and tcp addresses can both exist for one daemon).
	function normalizeHost(host: string): string {
		return host.trim().toLowerCase().replace(/\/+$/, '');
	}

	async function handleAutoDetect() {
		scanning = true;
		try {
			const res = await apiFetch('/api/v1/nodes/scan', { method: 'GET' });
			if (!res.ok) {
				throw new Error(`Scan endpoint returned ${res.status}`);
			}
			const data = await res.json();
			const candidates: Array<{
				host: string;
				reachable: boolean;
				latencyMs: number;
				source?: string;
			}> = data.candidates || [];

			await loadNodes();

			const registeredHosts = new Set(nodes.map((n) => n.host).map(normalizeHost));
			const reachable = candidates.filter((c) => c.reachable);
			// Prioritize the local socket (same-host daemon) first, then latency.
			detectedDaemons = reachable.map((c) => ({
				host: c.host,
				latencyMs: c.latencyMs,
				source: c.source || 'scan',
				alreadyRegistered: registeredHosts.has(normalizeHost(c.host)),
				name: suggestDaemonName(c.host, c.source || 'scan'),
				advertisedIp: guessAdvertisedIp(c.host)
			}));
			// Drop already-registered ones unless nothing unregistered remains —
			// then fall back to showing all reachable for visibility.
			const newOnes = detectedDaemons.filter((d) => !d.alreadyRegistered);
			if (newOnes.length > 0) {
				detectedDaemons = newOnes;
			}
			if (reachable.length === 0) {
				toast.info(
					`Auto-detect scanned ${candidates.length} daemon endpoint(s) across local interfaces — none reachable. Add a node manually if its daemon needs TLS or a custom port.`
				);
			} else if (newOnes.length > 0) {
				// Per your workflow: found daemons should offer a prompt to add.
				showDetectedDialog = true;
			} else {
				toast.success(`Auto-detect found ${reachable.length} daemon(s) — all already registered.`);
			}
			// Re-probe registered nodes via the existing health endpoint.
			for (const node of nodes) {
				try {
					await rpcClient.node.pingNode({ id: node.id });
				} catch {
					// Per-node ping failures already surface via loadNodes statuses.
				}
			}
			await loadNodes();
		} catch (error: unknown) {
			toast.error(
				`Auto-detect failed: ${error instanceof Error ? error.message : 'Unknown error'}`
			);
		} finally {
			scanning = false;
		}
	}

	function suggestDaemonName(host: string, source: string): string {
		const m = /tcp:\/\/([^:]+):/.exec(host);
		if (m) {
			return `node-${m[1]}`;
		}
		if (host.startsWith('unix://')) {
			return source === 'local-socket' ? 'local-docker' : host.replace('unix://', '');
		}
		return `docker-${source}`;
	}

	function guessAdvertisedIp(host: string): string {
		const m = /tcp:\/\/(\d+\.\d+\.\d+\.\d+):/.exec(host);
		if (m) return m[1];
		// Local unix socket daemon — the panel host's external IP isn't known
		// client-side; leave empty and let the operator fill it in.
		return '';
	}

	async function addDetectedDaemon(d: DetectedDaemon) {
		addingDaemonHost = d.host;
		try {
			await rpcClient.node.createNode({
				name: d.name.trim() || suggestDaemonName(d.host, d.source),
				host: d.host,
				advertisedIp: d.advertisedIp.trim(),
				tlsEnabled: false,
				tlsSkipVerify: false,
				tlsCaCert: '',
				tlsCert: '',
				tlsKey: '',
				maxMemoryMb: BigInt(0),
				maxServers: 0,
				enabled: true
			});
			d.alreadyRegistered = true;
			toast.success(`Node "${d.name}" added from auto-detect (${d.host})`);
			await loadNodes();
			// Close the prompt once every found daemon has been registered or skipped.
			if (detectedDaemons.every((x) => x.alreadyRegistered)) {
				showDetectedDialog = false;
			}
		} catch (error: unknown) {
			toast.error(
				`Failed to add node: ${error instanceof Error ? error.message : 'Unknown error'}`
			);
		} finally {
			addingDaemonHost = null;
		}
	}

	async function handlePingNode(nodeId: string) {
		pingingNodeId = nodeId;
		try {
			const res = await rpcClient.node.pingNode({ id: nodeId });
			pingLatencies[nodeId] = {
				latency: Number(res.latencyMs),
				status: res.status,
				message: res.message
			};
			if (res.success) {
				toast.success(`Node responded in ${res.latencyMs}ms`);
			} else {
				toast.error(`Node ping failed: ${res.message}`);
			}
			await loadNodes();
		} catch (error: unknown) {
			toast.error(`Ping failed: ${error instanceof Error ? error.message : 'Unknown error'}`);
		} finally {
			pingingNodeId = null;
		}
	}

	async function handleCreateNode() {
		if (!newNode.name.trim()) {
			toast.error('Node name is required');
			return;
		}
		if (!newNode.host.trim()) {
			toast.error('Docker host URL is required');
			return;
		}

		isSaving = true;
		try {
			await rpcClient.node.createNode({
				name: newNode.name.trim(),
				host: newNode.host.trim(),
				advertisedIp: newNode.advertisedIp.trim(),
				tlsEnabled: newNode.tlsEnabled,
				tlsSkipVerify: newNode.tlsSkipVerify,
				tlsCaCert: newNode.tlsCaCert,
				tlsCert: newNode.tlsCert,
				tlsKey: newNode.tlsKey,
				maxMemoryMb: BigInt(newNode.maxMemoryMb || 0),
				maxServers: newNode.maxServers || 0,
				enabled: newNode.enabled
			});

			toast.success(`Node "${newNode.name}" added successfully`);
			showAddDialog = false;
			// Reset form
			newNode = {
				name: '',
				host: 'tcp://',
				advertisedIp: '',
				tlsEnabled: false,
				tlsSkipVerify: false,
				tlsCaCert: '',
				tlsCert: '',
				tlsKey: '',
				maxMemoryMb: 0,
				maxServers: 0,
				enabled: true
			};
			await loadNodes();
		} catch (error: unknown) {
			toast.error(
				`Failed to add node: ${error instanceof Error ? error.message : 'Unknown error'}`
			);
		} finally {
			isSaving = false;
		}
	}

	function openEditNode(node: Node) {
		editingNode = {
			id: node.id,
			name: node.name,
			host: node.host,
			advertisedIp: node.advertisedIp,
			tlsEnabled: node.tlsEnabled,
			tlsSkipVerify: node.tlsSkipVerify,
			tlsCaCert: node.tlsCaCert || '',
			tlsCert: node.tlsCert || '',
			tlsKey: node.tlsKey || '',
			maxMemoryMb: Number(node.maxMemoryMb),
			maxServers: node.maxServers,
			enabled: node.enabled
		};
		showEditDialog = true;
	}

	async function handleUpdateNode() {
		if (!editingNode) return;
		if (!editingNode.name.trim()) {
			toast.error('Node name is required');
			return;
		}
		if (!editingNode.host.trim()) {
			toast.error('Docker host URL is required');
			return;
		}

		isSaving = true;
		try {
			await rpcClient.node.updateNode({
				id: editingNode.id,
				name: editingNode.name.trim(),
				host: editingNode.host.trim(),
				advertisedIp: editingNode.advertisedIp.trim(),
				tlsEnabled: editingNode.tlsEnabled,
				tlsSkipVerify: editingNode.tlsSkipVerify,
				tlsCaCert: editingNode.tlsCaCert,
				tlsCert: editingNode.tlsCert,
				tlsKey: editingNode.tlsKey,
				maxMemoryMb: BigInt(editingNode.maxMemoryMb || 0),
				maxServers: editingNode.maxServers || 0,
				enabled: editingNode.enabled
			});

			toast.success(`Node "${editingNode.name}" updated successfully`);
			showEditDialog = false;
			editingNode = null;
			await loadNodes();
		} catch (error: unknown) {
			toast.error(
				`Failed to update node: ${error instanceof Error ? error.message : 'Unknown error'}`
			);
		} finally {
			isSaving = false;
		}
	}

	let pendingDelete = $state<Node | null>(null);
	let confirmOpen = $state(false);

	async function handleDeleteNode(node: Node) {
		if (node.isLocal) {
			toast.error('Local Docker daemon cannot be deleted');
			return;
		}

		if (node.serverCount > 0) {
			toast.error(`Cannot delete node: ${node.serverCount} servers are assigned to it`);
			return;
		}

		pendingDelete = node;
		confirmOpen = true;
	}

	async function confirmDeleteNode() {
		const node = pendingDelete;
		if (!node) return;
		try {
			await rpcClient.node.deleteNode({ id: node.id });
			toast.success(`Node "${node.name}" deleted`);
			await loadNodes();
		} catch (error: unknown) {
			toast.error(
				`Failed to delete node: ${error instanceof Error ? error.message : 'Unknown error'}`
			);
		} finally {
			pendingDelete = null;
		}
	}

	type NodeStatusTag = { label: string; type: 'green' | 'red' | 'gray' };

	function getNodeStatusTag(status: NodeStatus): NodeStatusTag {
		switch (status) {
			case NodeStatus.ONLINE:
				return { label: 'Online', type: 'green' };
			case NodeStatus.ERROR:
				return { label: 'Error', type: 'red' };
			case NodeStatus.OFFLINE:
				return { label: 'Offline', type: 'gray' };
			default:
				return { label: 'Unknown', type: 'gray' };
		}
	}

	function formatMemory(mb: number | bigint): string {
		const val = Number(mb);
		if (val <= 0) return '0 MB';
		if (val >= 1024) {
			return `${(val / 1024).toFixed(1)} GB`;
		}
		return `${val} MB`;
	}

	// Effective memory ceiling for a node: an explicit max wins, otherwise the
	// daemon-reported host total is the best available bound.
	function memoryCap(node: Node): number {
		const max = Number(node.maxMemoryMb);
		return max > 0 ? max : Number(node.totalMemoryMb);
	}

	function memoryPercent(node: Node): number {
		const cap = memoryCap(node);
		if (cap <= 0) return 0;
		return Math.min(100, Math.round((Number(node.allocatedMemoryMb) / cap) * 100));
	}

	function getAvailableMemory(node: Node): { text: string; state: 'ok' | 'full' | 'unknown' } {
		const available = Number(node.availableMemoryMb);
		const max = Number(node.maxMemoryMb);
		const total = Number(node.totalMemoryMb);
		const allocated = Number(node.allocatedMemoryMb);

		if (available > 0) return { text: formatMemory(available), state: 'ok' };
		if (max > 0 && max <= allocated) return { text: '0 MB', state: 'full' };
		if (total > 0 && total <= allocated) return { text: '0 MB', state: 'full' };
		if (max > 0) return { text: formatMemory(max - allocated), state: 'ok' };
		return { text: 'Dynamic', state: 'unknown' };
	}

	const nodeSummary = $derived.by(() => {
		let online = 0;
		let runningServers = 0;
		let totalServers = 0;
		let allocatedMemoryMb = 0;

		for (const node of nodes) {
			if (node.status === NodeStatus.ONLINE) online += 1;
			runningServers += node.runningCount;
			totalServers += node.serverCount;
			allocatedMemoryMb += Number(node.allocatedMemoryMb);
		}

		return { total: nodes.length, online, runningServers, totalServers, allocatedMemoryMb };
	});
</script>

<div class="space-y-4">
	<!-- Header panel: section title, node actions, and cluster summary -->
	<CarbonTile class="overflow-hidden rounded-none !border-[#393939] !bg-[#262626] !p-0">
		<div class="flex flex-col gap-4 p-5 sm:flex-row sm:items-start sm:justify-between">
			<div class="min-w-0">
				<div class="flex items-center gap-2">
					<Layers class="h-4 w-4 shrink-0 text-[#78a9ff]" />
					<h2 class="truncate font-sans text-base font-semibold text-[#f4f4f4]">
						Docker Daemons & Nodes
					</h2>
				</div>
				<p class="mt-1 max-w-2xl font-sans text-xs text-[#8d8d8d]">
					Configure multiple remote Docker daemons for distributed container execution and load
					balancing.
				</p>
			</div>
			<div class="flex flex-wrap items-center gap-2">
				<CarbonButton
					kind="secondary"
					size="md"
					class="justify-center gap-2"
					onclick={handleAutoDetect}
					disabled={loading || scanning}
					title="Scan local network interfaces for active Docker daemons"
				>
					<Radio class="h-4 w-4 {scanning ? 'animate-pulse' : ''}" />
					<span>{scanning ? 'Scanning...' : 'Auto-detect'}</span>
				</CarbonButton>
				<CarbonButton
					kind="secondary"
					size="md"
					class="justify-center gap-2"
					onclick={loadNodes}
					disabled={loading}
				>
					<RefreshCw class="h-4 w-4 {loading ? 'animate-spin' : ''}" />
					<span>Refresh</span>
				</CarbonButton>
				<CarbonButton
					kind="primary"
					size="md"
					class="justify-center gap-2"
					onclick={() => (showAddDialog = true)}
				>
					<Plus class="h-4 w-4" />
					<span>Add Docker Node</span>
				</CarbonButton>
			</div>
		</div>

		{#if !loading}
			<!-- One-line cluster summary; 1px gaps paint the dividers -->
			<div class="grid grid-cols-2 gap-px border-t border-[#393939] bg-[#393939] sm:grid-cols-4">
				<div class="bg-[#262626] px-5 py-3">
					<p class="font-mono text-[11px] leading-4 tracking-wider text-[#8d8d8d] uppercase">
						Nodes
					</p>
					<p class="mt-0.5 font-mono text-base font-semibold text-[#f4f4f4]">
						{nodeSummary.total}
					</p>
				</div>
				<div class="bg-[#262626] px-5 py-3">
					<p class="font-mono text-[11px] leading-4 tracking-wider text-[#8d8d8d] uppercase">
						Online
					</p>
					<p class="mt-0.5 font-mono text-base font-semibold text-[#24a148]">
						{nodeSummary.online}
						<span class="text-sm text-[#8d8d8d]">/ {nodeSummary.total}</span>
					</p>
				</div>
				<div class="bg-[#262626] px-5 py-3">
					<p class="font-mono text-[11px] leading-4 tracking-wider text-[#8d8d8d] uppercase">
						Running Servers
					</p>
					<p class="mt-0.5 font-mono text-base font-semibold text-[#f4f4f4]">
						{nodeSummary.runningServers}
						<span class="text-sm text-[#8d8d8d]">/ {nodeSummary.totalServers}</span>
					</p>
				</div>
				<div class="bg-[#262626] px-5 py-3">
					<p class="font-mono text-[11px] leading-4 tracking-wider text-[#8d8d8d] uppercase">
						Allocated RAM
					</p>
					<p class="mt-0.5 font-mono text-base font-semibold text-[#f4f4f4]">
						{formatMemory(nodeSummary.allocatedMemoryMb)}
					</p>
				</div>
			</div>
		{/if}
	</CarbonTile>

	<!-- Nodes List -->
	{#if loading}
		<CarbonTile class="rounded-none !border-[#393939] !bg-[#262626] !p-0">
			<div class="flex items-center justify-center px-5 py-16">
				<CarbonInlineLoading description="Loading Docker nodes..." />
			</div>
		</CarbonTile>
	{:else if nodes.length === 0}
		<CarbonTile class="rounded-none !border-[#393939] !bg-[#262626] !p-0">
			<div class="flex flex-col items-center justify-center gap-4 px-5 py-16 text-center">
				<div
					class="flex h-12 w-12 items-center justify-center border border-[#393939] bg-[#161616]"
				>
					<Server class="h-6 w-6 text-[#8d8d8d]" />
				</div>
				<div class="space-y-1">
					<p class="font-mono text-[11px] tracking-wider text-[#8d8d8d] uppercase">
						No nodes registered
					</p>
					<h3 class="font-sans text-base font-semibold text-[#f4f4f4]">No Docker nodes found</h3>
					<p class="mx-auto max-w-md font-sans text-xs text-[#a8a8a8]">
						Add a remote Docker host to scale server instances across multiple physical machines, or
						auto-detect daemons listening on your local network.
					</p>
				</div>
				<div class="flex flex-wrap items-center justify-center gap-2">
					<CarbonButton
						kind="tertiary"
						size="md"
						class="justify-center gap-2"
						onclick={handleAutoDetect}
						disabled={scanning}
					>
						<Radio class="h-4 w-4 {scanning ? 'animate-pulse' : ''}" />
						<span>{scanning ? 'Scanning...' : 'Auto-detect Daemons'}</span>
					</CarbonButton>
					<CarbonButton
						kind="primary"
						size="md"
						class="justify-center gap-2"
						onclick={() => (showAddDialog = true)}
					>
						<Plus class="h-4 w-4" />
						<span>Add Docker Node</span>
					</CarbonButton>
				</div>
			</div>
		</CarbonTile>
	{:else}
		<div class="motion-stagger grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
			{#each nodes as node (node.id)}
				{@const statusTag = getNodeStatusTag(node.status)}
				{@const pingInfo = pingLatencies[node.id]}
				{@const available = getAvailableMemory(node)}
				{@const capMb = memoryCap(node)}
				{@const usedPercent = memoryPercent(node)}
				{@const pingFailed =
					pingInfo?.status === NodeStatus.ERROR || pingInfo?.status === NodeStatus.OFFLINE}

				<CarbonTile
					class="flex h-full flex-col rounded-none !border-[#393939] !bg-[#262626] !p-0 transition-colors hover:!border-[#525252]"
				>
					<!-- Identity -->
					<div class="px-5 pt-4">
						<div class="flex items-start justify-between gap-3">
							<h3
								class="min-w-0 flex-1 truncate font-sans text-sm font-semibold text-[#f4f4f4]"
								title={node.name}
							>
								{node.name}
							</h3>
							<CarbonTag type={statusTag.type} size="sm">{statusTag.label}</CarbonTag>
						</div>
						<div class="mt-1 flex items-center justify-between gap-2">
							<p
								class="min-w-0 flex-1 truncate font-mono text-[11px] text-[#8d8d8d]"
								title={node.host}
							>
								{node.host}
							</p>
							<div class="flex shrink-0 items-center gap-1">
								{#if node.isLocal}
									<CarbonTag type="gray" size="sm">Local</CarbonTag>
								{/if}
								{#if !node.enabled}
									<CarbonTag type="warm-gray" size="sm">Disabled</CarbonTag>
								{/if}
							</div>
						</div>
					</div>

					<!-- Metrics -->
					<div class="flex flex-1 flex-col px-5 pt-4">
						<div class="grid grid-cols-1 gap-x-4 gap-y-3 min-[380px]:grid-cols-2">
							<div class="min-w-0">
								<p class="font-mono text-[11px] leading-4 tracking-wider text-[#8d8d8d] uppercase">
									Servers
								</p>
								<p
									class="mt-0.5 flex flex-wrap items-baseline gap-x-1.5 font-mono text-sm text-[#f4f4f4]"
								>
									<span class="whitespace-nowrap">{node.runningCount} / {node.serverCount}</span>
									{#if node.maxServers > 0}
										<span class="text-[11px] whitespace-nowrap text-[#8d8d8d]">
											MAX {node.maxServers}
										</span>
									{/if}
								</p>
							</div>

							<div class="min-w-0">
								<p class="font-mono text-[11px] leading-4 tracking-wider text-[#8d8d8d] uppercase">
									Allocated RAM
								</p>
								<p
									class="mt-0.5 flex flex-wrap items-baseline gap-x-1.5 font-mono text-sm text-[#f4f4f4]"
								>
									<span class="whitespace-nowrap">{formatMemory(node.allocatedMemoryMb)}</span>
									{#if capMb > 0}
										<span class="text-[11px] whitespace-nowrap text-[#8d8d8d]">
											/ {formatMemory(capMb)}
										</span>
									{/if}
								</p>
							</div>

							<div class="min-w-0">
								<p class="font-mono text-[11px] leading-4 tracking-wider text-[#8d8d8d] uppercase">
									Available RAM
								</p>
								<p
									class="mt-0.5 font-mono text-sm whitespace-nowrap {available.state === 'full'
										? 'text-[#da1e28]'
										: available.state === 'ok'
											? 'text-[#24a148]'
											: 'text-[#8d8d8d]'}"
								>
									{available.text}{available.state === 'full' ? ' (full)' : ''}
								</p>
							</div>

							<div class="min-w-0">
								<p class="font-mono text-[11px] leading-4 tracking-wider text-[#8d8d8d] uppercase">
									TLS
								</p>
								<p class="mt-0.5 flex flex-wrap items-baseline gap-x-1.5 font-mono text-sm">
									<span
										class="whitespace-nowrap {node.tlsEnabled
											? 'text-[#24a148]'
											: 'text-[#8d8d8d]'}"
									>
										{node.tlsEnabled ? 'Enabled' : 'Off'}
									</span>
									{#if node.tlsEnabled && node.tlsSkipVerify}
										<span class="text-[11px] whitespace-nowrap text-[#f1c21b]">SKIP VERIFY</span>
									{/if}
								</p>
							</div>

							<div class="min-w-0 min-[380px]:col-span-2">
								<div class="flex items-baseline justify-between gap-3">
									<p
										class="shrink-0 font-mono text-[11px] leading-4 tracking-wider text-[#8d8d8d] uppercase"
									>
										Advertised IP
									</p>
									<p
										class="min-w-0 truncate font-mono text-[13px] {node.advertisedIp
											? 'text-[#f4f4f4]'
											: 'text-[#8d8d8d]'}"
										title={node.advertisedIp || 'Not set'}
									>
										{node.advertisedIp || 'Not set'}
									</p>
								</div>
							</div>
						</div>

						<!-- Sharp capacity bar for the memory ceiling -->
						{#if capMb > 0}
							<div
								class="mt-3 h-1 w-full overflow-hidden bg-[#393939]"
								role="progressbar"
								aria-label="{node.name} allocated memory"
								aria-valuemin="0"
								aria-valuemax="100"
								aria-valuenow={usedPercent}
							>
								<div
									class="h-full {usedPercent >= 100
										? 'bg-[#da1e28]'
										: 'bg-[#0f62fe]'} transition-[width] duration-[var(--motion-slow)] ease-[var(--ease-out-quart)]"
									style="width: {usedPercent}%"
								></div>
							</div>
						{/if}

						{#if pingInfo}
							<p
								class="mt-2.5 font-mono text-[11px] tracking-wider uppercase {pingFailed
									? 'text-[#da1e28]'
									: 'text-[#78a9ff]'}"
							>
								Last ping {pingInfo.latency} ms
							</p>
						{/if}
					</div>

					<!-- Footer actions, pinned to the bottom of the tile -->
					<div
						class="mt-auto flex items-center justify-between gap-2 border-t border-[#393939] px-4 py-1.5"
					>
						<CarbonButton
							kind="ghost"
							size="md"
							class="justify-center gap-2"
							onclick={() => handlePingNode(node.id)}
							disabled={pingingNodeId === node.id}
							title="Ping {node.name}"
						>
							{#if pingingNodeId === node.id}
								<Loader2 class="h-4 w-4 animate-spin" />
								<span>Pinging...</span>
							{:else}
								<Activity class="h-4 w-4" />
								<span>Ping</span>
							{/if}
						</CarbonButton>

						<div class="flex items-center gap-1">
							<CarbonButton
								kind="ghost"
								iconOnly
								onclick={() => openEditNode(node)}
								title="Edit node"
								aria-label="Edit {node.name}"
							>
								<Edit class="h-4 w-4" />
							</CarbonButton>
							{#if !node.isLocal}
								<CarbonButton
									kind="ghost"
									iconOnly
									class="hover:text-[#da1e28]"
									onclick={() => handleDeleteNode(node)}
									title="Delete node"
									aria-label="Delete {node.name}"
								>
									<Trash2 class="h-4 w-4" />
								</CarbonButton>
							{/if}
						</div>
					</div>
				</CarbonTile>
			{/each}
		</div>
	{/if}
</div>

<!-- Detected Daemons Dialog (auto-detect results pending registration) -->
<Dialog bind:open={showDetectedDialog}>
	<DialogContent class="sm:max-w-xl">
		<DialogHeader>
			<DialogTitle>Docker daemons detected</DialogTitle>
			<DialogDescription>
				Auto-detect found reachable Docker daemons on this host's network. Register the ones you
				want the placement engine to use — you can adjust names, IPs, and limits later.
			</DialogDescription>
		</DialogHeader>

		<div class="max-h-[50vh] space-y-3 overflow-y-auto py-2">
			{#each detectedDaemons as d (d.host)}
				<div class="space-y-2 rounded-lg border p-3">
					<div class="flex items-center justify-between gap-2">
						<div class="min-w-0">
							<div class="flex items-center gap-2">
								<CheckCircle2 class="h-4 w-4 shrink-0 text-green-500" />
								<span class="truncate font-mono text-xs text-foreground" title={d.host}
									>{d.host}</span
								>
							</div>
							<p class="mt-0.5 text-xs text-muted-foreground">
								{#if d.latencyMs > 0}responded in {d.latencyMs}ms ·
								{/if}{d.source}
							</p>
						</div>
						<Button
							size="sm"
							onclick={() => addDetectedDaemon(d)}
							disabled={d.alreadyRegistered || addingDaemonHost !== null}
						>
							{#if addingDaemonHost === d.host}
								<Loader2 class="mr-2 h-4 w-4 animate-spin" />
								Adding...
							{:else if d.alreadyRegistered}
								Added
							{:else}
								<Plus class="mr-2 h-4 w-4" />
								Add
							{/if}
						</Button>
					</div>
					{#if !d.alreadyRegistered}
						<div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
							<div class="space-y-1">
								<Label class="text-xs" for="det-name-{d.host}">Node name</Label>
								<Input
									id="det-name-{d.host}"
									class="h-8 text-xs"
									bind:value={d.name}
									disabled={addingDaemonHost !== null}
								/>
							</div>
							<div class="space-y-1">
								<Label class="text-xs" for="det-ip-{d.host}">Advertised IP</Label>
								<Input
									id="det-ip-{d.host}"
									class="h-8 text-xs"
									placeholder="e.g. 192.168.1.10"
									bind:value={d.advertisedIp}
									disabled={addingDaemonHost !== null}
								/>
							</div>
						</div>
					{/if}
				</div>
			{/each}
		</div>

		<DialogFooter>
			<Button variant="outline" onclick={() => (showDetectedDialog = false)}>
				{#if detectedDaemons.some((d) => !d.alreadyRegistered)}
					Skip for now
				{:else}
					Close
				{/if}
			</Button>
		</DialogFooter>
	</DialogContent>
</Dialog>

<!-- Add Node Dialog -->
<Dialog bind:open={showAddDialog}>
	<DialogContent class="max-h-[90vh] overflow-y-auto sm:max-w-lg">
		<DialogHeader>
			<DialogTitle>Add Docker Daemon Node</DialogTitle>
			<DialogDescription>
				Connect an external Docker daemon host via TCP socket or unix pipe.
			</DialogDescription>
		</DialogHeader>

		<div class="space-y-4 py-2">
			<div class="space-y-2">
				<Label for="node-name">Node Name *</Label>
				<Input
					id="node-name"
					placeholder="e.g. worker-1"
					bind:value={newNode.name}
					disabled={isSaving}
				/>
			</div>

			<div class="space-y-2">
				<Label for="node-host">Docker Host URL *</Label>
				<Input
					id="node-host"
					placeholder="tcp://192.168.0.102:2376 or unix:///var/run/docker.sock"
					bind:value={newNode.host}
					disabled={isSaving}
				/>
				<p class="text-xs text-muted-foreground">
					Ensure Docker daemon on the host is listening on this socket/address.
				</p>
			</div>

			<div class="space-y-2">
				<Label for="node-adv-ip">Advertised IP Address</Label>
				<Input
					id="node-adv-ip"
					placeholder="e.g. 192.168.0.102"
					bind:value={newNode.advertisedIp}
					disabled={isSaving}
				/>
				<p class="text-xs text-muted-foreground">
					External IP address that players connect to when servers run on this node.
				</p>
			</div>

			<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
				<div class="space-y-2">
					<Label for="node-max-mem">Max Memory Limit (MB)</Label>
					<Input
						id="node-max-mem"
						type="number"
						min="0"
						step="1024"
						placeholder="0 = Unlimited"
						bind:value={newNode.maxMemoryMb}
						disabled={isSaving}
					/>
				</div>

				<div class="space-y-2">
					<Label for="node-max-srv">Max Server Containers</Label>
					<Input
						id="node-max-srv"
						type="number"
						min="0"
						placeholder="0 = Unlimited"
						bind:value={newNode.maxServers}
						disabled={isSaving}
					/>
				</div>
			</div>

			<!-- Enabled Switch -->
			<div class="flex items-center justify-between rounded-lg border p-3">
				<div class="space-y-0.5">
					<Label for="node-enabled">Enable Node</Label>
					<p class="text-xs text-muted-foreground">
						Allow placement engine to assign servers to this Docker node.
					</p>
				</div>
				<Switch
					id="node-enabled"
					checked={newNode.enabled}
					onCheckedChange={(v) => (newNode.enabled = v)}
					disabled={isSaving}
				/>
			</div>

			<!-- TLS Configuration -->
			<div class="space-y-3 rounded-lg border p-3">
				<div class="flex items-center justify-between">
					<div class="space-y-0.5">
						<Label for="node-tls">TLS Authentication</Label>
						<p class="text-xs text-muted-foreground">
							Use client certificates for mutual TLS docker daemon auth.
						</p>
					</div>
					<Switch
						id="node-tls"
						checked={newNode.tlsEnabled}
						onCheckedChange={(v) => (newNode.tlsEnabled = v)}
						disabled={isSaving}
					/>
				</div>

				{#if newNode.tlsEnabled}
					<div class="space-y-3 border-t border-border/60 pt-2">
						<div class="flex items-center justify-between">
							<Label for="node-skip-verify" class="text-xs">Skip TLS Certificate Verification</Label
							>
							<Switch
								id="node-skip-verify"
								checked={newNode.tlsSkipVerify}
								onCheckedChange={(v) => (newNode.tlsSkipVerify = v)}
								disabled={isSaving}
							/>
						</div>

						<div class="space-y-1.5">
							<Label for="node-ca-cert" class="text-xs">CA Certificate (PEM)</Label>
							<Textarea
								id="node-ca-cert"
								placeholder="-----BEGIN CERTIFICATE-----\n..."
								bind:value={newNode.tlsCaCert}
								class="h-16 font-mono text-xs"
								disabled={isSaving}
							/>
						</div>

						<div class="space-y-1.5">
							<Label for="node-cert" class="text-xs">Client Certificate (PEM)</Label>
							<Textarea
								id="node-cert"
								placeholder="-----BEGIN CERTIFICATE-----\n..."
								bind:value={newNode.tlsCert}
								class="h-16 font-mono text-xs"
								disabled={isSaving}
							/>
						</div>

						<div class="space-y-1.5">
							<Label for="node-key" class="text-xs">Client Private Key (PEM)</Label>
							<Textarea
								id="node-key"
								placeholder="-----BEGIN RSA PRIVATE KEY-----\n..."
								bind:value={newNode.tlsKey}
								class="h-16 font-mono text-xs"
								disabled={isSaving}
							/>
						</div>
					</div>
				{/if}
			</div>
		</div>

		<DialogFooter>
			<Button variant="outline" onclick={() => (showAddDialog = false)} disabled={isSaving}>
				Cancel
			</Button>
			<Button onclick={handleCreateNode} disabled={isSaving}>
				{#if isSaving}
					<Loader2 class="mr-2 h-4 w-4 animate-spin" />
					Adding...
				{:else}
					Add Node
				{/if}
			</Button>
		</DialogFooter>
	</DialogContent>
</Dialog>

<!-- Edit Node Dialog -->
<Dialog bind:open={showEditDialog}>
	<DialogContent class="max-h-[90vh] overflow-y-auto sm:max-w-lg">
		<DialogHeader>
			<DialogTitle>Edit Docker Daemon Node</DialogTitle>
			<DialogDescription>
				Update host, capacity limits, or TLS configuration for this node.
			</DialogDescription>
		</DialogHeader>

		{#if editingNode}
			<div class="space-y-4 py-2">
				<div class="space-y-2">
					<Label for="edit-node-name">Node Name *</Label>
					<Input id="edit-node-name" bind:value={editingNode.name} disabled={isSaving} />
				</div>

				<div class="space-y-2">
					<Label for="edit-node-host">Docker Host URL *</Label>
					<Input id="edit-node-host" bind:value={editingNode.host} disabled={isSaving} />
				</div>

				<div class="space-y-2">
					<Label for="edit-node-adv-ip">Advertised IP Address</Label>
					<Input
						id="edit-node-adv-ip"
						placeholder="e.g. 192.168.0.102"
						bind:value={editingNode.advertisedIp}
						disabled={isSaving}
					/>
				</div>

				<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
					<div class="space-y-2">
						<Label for="edit-node-max-mem">Max Memory Limit (MB)</Label>
						<Input
							id="edit-node-max-mem"
							type="number"
							min="0"
							step="1024"
							placeholder="0 = Unlimited"
							bind:value={editingNode.maxMemoryMb}
							disabled={isSaving}
						/>
					</div>

					<div class="space-y-2">
						<Label for="edit-node-max-srv">Max Server Containers</Label>
						<Input
							id="edit-node-max-srv"
							type="number"
							min="0"
							placeholder="0 = Unlimited"
							bind:value={editingNode.maxServers}
							disabled={isSaving}
						/>
					</div>
				</div>

				<!-- Enabled Switch -->
				<div class="flex items-center justify-between rounded-lg border p-3">
					<div class="space-y-0.5">
						<Label for="edit-node-enabled">Enable Node</Label>
						<p class="text-xs text-muted-foreground">
							Allow placement engine to assign servers to this Docker node.
						</p>
					</div>
					<Switch
						id="edit-node-enabled"
						checked={editingNode.enabled}
						onCheckedChange={(v) => {
							if (editingNode) editingNode.enabled = v;
						}}
						disabled={isSaving}
					/>
				</div>

				<!-- TLS Configuration -->
				<div class="space-y-3 rounded-lg border p-3">
					<div class="flex items-center justify-between">
						<div class="space-y-0.5">
							<Label for="edit-node-tls">TLS Authentication</Label>
							<p class="text-xs text-muted-foreground">
								Use client certificates for mutual TLS auth.
							</p>
						</div>
						<Switch
							id="edit-node-tls"
							checked={editingNode.tlsEnabled}
							onCheckedChange={(v) => {
								if (editingNode) editingNode.tlsEnabled = v;
							}}
							disabled={isSaving}
						/>
					</div>

					{#if editingNode.tlsEnabled}
						<div class="space-y-3 border-t border-border/60 pt-2">
							<div class="flex items-center justify-between">
								<Label for="edit-node-skip-verify" class="text-xs"
									>Skip TLS Certificate Verification</Label
								>
								<Switch
									id="edit-node-skip-verify"
									checked={editingNode.tlsSkipVerify}
									onCheckedChange={(v) => {
										if (editingNode) editingNode.tlsSkipVerify = v;
									}}
									disabled={isSaving}
								/>
							</div>

							<div class="space-y-1.5">
								<Label for="edit-node-ca-cert" class="text-xs">CA Certificate (PEM)</Label>
								<Textarea
									id="edit-node-ca-cert"
									placeholder="-----BEGIN CERTIFICATE-----\n..."
									bind:value={editingNode.tlsCaCert}
									class="h-16 font-mono text-xs"
									disabled={isSaving}
								/>
							</div>

							<div class="space-y-1.5">
								<Label for="edit-node-cert" class="text-xs">Client Certificate (PEM)</Label>
								<Textarea
									id="edit-node-cert"
									placeholder="-----BEGIN CERTIFICATE-----\n..."
									bind:value={editingNode.tlsCert}
									class="h-16 font-mono text-xs"
									disabled={isSaving}
								/>
							</div>

							<div class="space-y-1.5">
								<Label for="edit-node-key" class="text-xs">Client Private Key (PEM)</Label>
								<Textarea
									id="edit-node-key"
									placeholder="-----BEGIN RSA PRIVATE KEY-----\n..."
									bind:value={editingNode.tlsKey}
									class="h-16 font-mono text-xs"
									disabled={isSaving}
								/>
							</div>
						</div>
					{/if}
				</div>
			</div>
		{/if}

		<DialogFooter>
			<Button variant="outline" onclick={() => (showEditDialog = false)} disabled={isSaving}>
				Cancel
			</Button>
			<Button onclick={handleUpdateNode} disabled={isSaving}>
				{#if isSaving}
					<Loader2 class="mr-2 h-4 w-4 animate-spin" />
					Saving...
				{:else}
					Save Changes
				{/if}
			</Button>
		</DialogFooter>
	</DialogContent>
</Dialog>
<CarbonConfirm
	bind:open={confirmOpen}
	title="Delete Docker node?"
	message="The node is removed from the panel. Containers on it are left untouched."
	confirmLabel="Delete Node"
	danger
	onconfirm={confirmDeleteNode}
	onclose={() => (pendingDelete = null)}
>
	{#snippet details()}
		{#if pendingDelete}{pendingDelete.name}{/if}
	{/snippet}
</CarbonConfirm>
