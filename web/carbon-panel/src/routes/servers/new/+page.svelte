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
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { rpcClient } from '$lib/api/rpc-client';
	import { beginLifecycleRequest } from '$lib/lifecycle-request';
	import { toast } from 'svelte-sonner';
	import DatapackSearch from '$lib/components/datapack-search.svelte';
	import {
		ArrowLeft,
		Package,
		Settings,
		Server as ServerIcon,
		Activity,
		ChevronRight,
		ChevronLeft,
		Check,
		Plus,
		X
	} from '@lucide/svelte';
	import { create } from '@bufbuild/protobuf';
	import type { CreateServerRequest } from '$lib/proto/carbonpanel/v1/server_pb';
	import { CreateServerRequestSchema } from '$lib/proto/carbonpanel/v1/server_pb';
	import { ModLoader, type ProxyListener } from '$lib/proto/carbonpanel/v1/common_pb';
	import type { ServerBlueprint } from '$lib/proto/carbonpanel/v1/blueprint_pb';
	import { resolveCreateBlueprint } from '$lib/utils/server-blueprint';
	import type { ModLoaderInfo, DockerImage } from '$lib/proto/carbonpanel/v1/minecraft_pb';
	import type { IndexedModpack, Version } from '$lib/proto/carbonpanel/v1/modpack_pb';
	import type { Node } from '$lib/proto/carbonpanel/v1/node_pb';
	import { NodeStatus } from '$lib/proto/carbonpanel/v1/node_pb';
	import {
		CarbonButton,
		CarbonInlineLoading,
		CarbonModal,
		CarbonPageHeader,
		CarbonTag,
		CarbonTextInput
	} from '$lib/components/carbon';
	import AdditionalPortsEditor from '$lib/components/additional-ports-editor.svelte';
	import DockerOverridesEditor from '$lib/components/docker-overrides-editor.svelte';
	import { getUniqueDockerImages, getDockerImageDisplayName } from '$lib/utils';
	import * as _ from 'lodash-es';

	let loading = $state(false);
	let loadingVersions = $state(true);
	let minecraftVersions = $state<string[]>([]);
	let modLoaders = $state<ModLoaderInfo[]>([]);
	let dockerImages = $state<DockerImage[]>([]);
	let latestVersion = $state('');
	let proxyEnabled = $state(false);
	let proxyBaseURL = $state('');
	let proxyListeners = $state<ProxyListener[]>([]);
	let usedPorts = $state<Record<number, boolean>>({});
	let portError = $state('');
	let useProxyMode = $state(false);

	// Wizard step tracking
	let currentStep = $state(0);
	const steps = [
		{ id: 0, title: 'Basic Information', subtitle: 'Name & preset' },
		{ id: 1, title: 'Game Engine', subtitle: 'Version & loader' },
		{ id: 2, title: 'Resources & Network', subtitle: 'Port & RAM' },
		{ id: 3, title: 'Cluster & Runtime', subtitle: 'Placement & flags' },
		{ id: 4, title: 'Datapacks', subtitle: 'Optional world packs' }
	];

	// Datapack selection — installed into the world after the server is created
	let selectedDatapacks = $state<Array<{ id: string; name: string; indexer: string }>>([]);
	let catalogBlueprints = $state<ServerBlueprint[]>([]);
	let selectedBlueprintId = $state('');

	function toggleDatapack(id: string, name: string, indexer: string) {
		if (selectedDatapacks.some((p) => p.id === id)) {
			selectedDatapacks = selectedDatapacks.filter((p) => p.id !== id);
		} else {
			selectedDatapacks = [...selectedDatapacks, { id, name, indexer }];
		}
	}

	function removeDatapack(id: string) {
		selectedDatapacks = selectedDatapacks.filter((p) => p.id !== id);
	}

	// Cluster node placement state
	let nodes = $state<Node[]>([]);
	let loadingNodes = $state(true);
	let nodePlacementMode = $state<'auto' | 'manual'>('auto');
	let selectedNodeId = $state<string>('');
	let placementStrategy = $state<string>('least_memory');

	// Modpack selection
	let showModpackDialog = $state(false);
	let selectedModpack = $state<IndexedModpack | null>(null);
	let favoriteModpacks = $state<IndexedModpack[]>([]);
	let modpackVersions = $state<Version[]>([]);
	let selectedVersionId = $state<string>('');
	// Tracks an in-flight version fetch so the select can show a loading state
	// instead of briefly presenting an empty list.
	let loadingModpackVersions = $state(false);

	let formData = $state<CreateServerRequest>(
		create(CreateServerRequestSchema, {
			name: '',
			description: '',
			modLoader: ModLoader.VANILLA,
			mcVersion: '',
			loaderVersion: '',
			port: 25565,
			maxPlayers: 20,
			memory: 2048,
			dockerImage: '',
			autoStart: false,
			detached: false,
			startImmediately: false,
			autoHibernate: false,
			autoDeepSleep: false,
			proxyHostname: '',
			proxyListenerId: '',
			useBaseUrl: false,
			additionalPorts: [],
			dockerOverrides: undefined,
			modpackId: '',
			modpackVersionId: ''
		})
	);

	// Exact loader builds for the current loader + Minecraft version. Empty
	// when the selected loader does not take a version at all.
	let loaderVersions = $state<string[]>([]);
	let loadingLoaderVersions = $state(false);

	const loaderTakesVersion = $derived(
		[ModLoader.NEOFORGE, ModLoader.FORGE, ModLoader.FABRIC, ModLoader.QUILT].includes(
			formData.modLoader
		)
	);

	$effect(() => {
		const loader = formData.modLoader;
		const mcVersion = formData.mcVersion;
		if (!loaderTakesVersion || !mcVersion) {
			loaderVersions = [];
			formData.loaderVersion = '';
			return;
		}
		let cancelled = false;
		loadingLoaderVersions = true;
		rpcClient.minecraft
			.getLoaderVersions({ loader: loader.toString(), mcVersion })
			.then((response) => {
				if (cancelled) return;
				loaderVersions = response.versions;
				// Keep a pin that still exists; otherwise fall back to latest rather
				// than silently sending a build this Minecraft version has no image for.
				if (formData.loaderVersion && !response.versions.includes(formData.loaderVersion)) {
					formData.loaderVersion = '';
				}
			})
			.catch(() => {
				if (!cancelled) loaderVersions = [];
			})
			.finally(() => {
				if (!cancelled) loadingLoaderVersions = false;
			});
		return () => {
			cancelled = true;
		};
	});

	onMount(async () => {
		try {
			const [
				versionsData,
				loadersData,
				imagesData,
				proxyStatus,
				portData,
				listeners,
				nodesData,
				blueprintsData
			] = await Promise.allSettled([
				rpcClient.minecraft.getMinecraftVersions({}),
				rpcClient.minecraft.getModLoaders({}),
				rpcClient.minecraft.getDockerImages({}),
				rpcClient.proxy.getProxyStatus({}),
				rpcClient.server.getNextAvailablePort({}),
				rpcClient.proxy.getProxyListeners({}),
				rpcClient.node.listNodes({}),
				rpcClient.blueprint.listBlueprints({})
			]);

			if (versionsData.status === 'fulfilled') {
				minecraftVersions = versionsData.value.versions.map((v) => v.id);
				latestVersion = versionsData.value.latest;
				if (!formData.mcVersion && latestVersion) {
					formData.mcVersion = latestVersion;
				}
			}

			if (loadersData.status === 'fulfilled') {
				modLoaders = loadersData.value.modloaders;
			}

			if (imagesData.status === 'fulfilled') {
				dockerImages = imagesData.value.images;
			}

			if (proxyStatus.status === 'fulfilled') {
				proxyEnabled = proxyStatus.value.enabled;
				proxyBaseURL = proxyStatus.value.baseUrl || '';
			}

			if (listeners.status === 'fulfilled') {
				proxyListeners = listeners.value.listeners
					.map((l) => l.listener)
					.filter((l): l is ProxyListener => l !== undefined && l.enabled);

				const defaultListener = proxyListeners.find((l) => l?.isDefault);
				if (defaultListener) {
					formData.proxyListenerId = defaultListener.id;
				} else if (proxyListeners.length > 0) {
					formData.proxyListenerId = proxyListeners[0]?.id || '';
				}
			}

			if (portData.status === 'fulfilled') {
				formData.port = portData.value.port;
				usedPorts = Object.fromEntries(
					portData.value.usedPorts?.map((p) => [p.port, p.inUse]) || []
				);
			}

			if (blueprintsData.status === 'fulfilled') {
				catalogBlueprints = blueprintsData.value.blueprints || [];
			}

			if (nodesData.status === 'fulfilled') {
				nodes = nodesData.value.nodes || [];
				if (nodes.length > 0) {
					selectedNodeId = nodes[0].id;
				}
				const activeNodes = nodes.filter((n) => n.enabled && n.status === NodeStatus.ONLINE);
				if (activeNodes.length === 0) {
					toast.error(
						'No active Docker nodes available — register a node in Settings → Docker Nodes first.'
					);
				}
			}
		} catch (error) {
			console.error('Initialization error:', error);
		} finally {
			loadingVersions = false;
			loadingNodes = false;
		}

		loadFavoriteModpacks();

		const modpackParam = page.url.searchParams.get('modpack');
		if (modpackParam) {
			try {
				const packRes = await rpcClient.modpack.getModpack({ id: modpackParam });
				if (packRes.modpack) {
					await selectModpack(packRes.modpack);
				}
			} catch (err) {
				console.error('Failed to pre-select modpack from URL:', err);
			}
		}
	});

	async function loadFavoriteModpacks() {
		try {
			const result = await rpcClient.modpack.listFavorites({});
			favoriteModpacks = result.modpacks;
		} catch (error) {
			console.error('Failed to load favorite modpacks:', error);
		}
	}

	async function loadModpackVersions(modpackId: string) {
		loadingModpackVersions = true;
		modpackVersions = [];
		selectedVersionId = '';

		try {
			const data = await rpcClient.modpack.getModpackVersions({
				id: modpackId,
				gameVersion: '',
				modLoader: ''
			});
			modpackVersions = data.versions || [];
			if (modpackVersions.length > 0 && !selectedVersionId) {
				selectedVersionId = modpackVersions[0].id;
			}
		} catch (error) {
			console.error('Failed to load modpack versions:', error);
			modpackVersions = [];
		} finally {
			loadingModpackVersions = false;
		}
	}

	function applyBlueprint(seed: ServerBlueprint) {
		const spec = resolveCreateBlueprint(seed);
		if (!spec.qualified) {
			return;
		}
		selectedBlueprintId = seed.id;
		selectedModpack = null;
		formData.modLoader = spec.modLoader;
		formData.dockerImage = spec.storedImage;
		if (spec.memoryMb > 0) {
			formData.memory = spec.memoryMb;
		}
		if (!formData.mcVersion || formData.mcVersion.toUpperCase() === 'LATEST') {
			formData.mcVersion = latestVersion || formData.mcVersion;
		}
	}

	async function selectModpack(modpack: IndexedModpack) {
		selectedModpack = modpack;
		selectedBlueprintId = '';
		showModpackDialog = false;

		try {
			const cfg = await rpcClient.modpack.getModpackConfig({ id: modpack.id });
			formData.name = modpack.name || '';
			formData.description = modpack.summary || '';
			formData.modLoader = _.get(cfg, 'mod_loader', ModLoader.VANILLA);
			formData.mcVersion = modpack.mcVersion || '';
			formData.memory = _.get(cfg, 'memory', modpack.recommendedRam || 2048);
			formData.dockerImage = modpack.dockerImage || '';
			await loadModpackVersions(modpack.id);
		} catch (error) {
			toast.error('Failed to load modpack configuration');
			console.error(error);
			selectedModpack = null;
		}
	}

	function clearBlueprint() {
		selectedBlueprintId = '';
		formData.dockerImage = '';
	}

	function removeModpack() {
		selectedModpack = null;
		modpackVersions = [];
		selectedVersionId = '';
		formData.modLoader = ModLoader.VANILLA;
		formData.mcVersion = latestVersion || '';
		formData.dockerImage = '';
		formData.memory = 2048;
	}

	function parseJsonArray(jsonStr: string): string[] {
		try {
			return JSON.parse(jsonStr);
		} catch {
			return [];
		}
	}

	function validatePort(port: number) {
		portError = '';

		if (port < 1 || port > 65535) {
			portError = 'Port must be between 1 and 65535';
			return false;
		}

		if (usedPorts[port]) {
			portError = 'This port is already in use';
			return false;
		}

		return true;
	}

	async function refreshAvailablePort() {
		try {
			const portData = await rpcClient.server.getNextAvailablePort({});
			formData.port = portData.port;
			usedPorts = Object.fromEntries(portData.usedPorts?.map((p) => [p.port, p.inUse]) || []);
			portError = '';
		} catch (error) {
			console.error('Failed to get available port:', error);
		}
	}

	function canProceed(step: number): boolean {
		if (step === 0) {
			if (!formData.name.trim()) {
				toast.error('Server name is required');
				return false;
			}
		}
		if (step === 2) {
			if (!useProxyMode && !validatePort(formData.port)) {
				toast.error('Please enter a valid, available port');
				return false;
			}
		}
		return true;
	}

	function goToStep(stepIndex: number) {
		if (stepIndex > currentStep) {
			for (let i = currentStep; i < stepIndex; i++) {
				if (!canProceed(i)) return;
			}
		}
		currentStep = stepIndex;
	}

	function nextStep() {
		if (canProceed(currentStep)) {
			if (currentStep < steps.length - 1) {
				currentStep++;
			}
		}
	}

	function prevStep() {
		if (currentStep > 0) {
			currentStep--;
		}
	}

	async function handleSubmit(e?: Event) {
		if (e) e.preventDefault();

		if (!formData.name.trim()) {
			toast.error('Server name is required');
			currentStep = 0;
			return;
		}

		if (selectedBlueprintId) {
			const seed = catalogBlueprints.find((b) => b.id === selectedBlueprintId);
			if (seed && !resolveCreateBlueprint(seed).qualified) {
				toast.error('That catalog seed is not a qualified create path.');
				currentStep = 0;
				return;
			}
		}

		if (!useProxyMode && !validatePort(formData.port)) {
			toast.error('Please select a valid port');
			currentStep = 2;
			return;
		}

		if (nodePlacementMode === 'manual' && !selectedNodeId) {
			toast.error('No Docker node selected — register a node in Settings → Docker Nodes first.');
			currentStep = 3;
			return;
		}

		loading = true;
		const attempt = beginLifecycleRequest(formData.name.trim() || 'new-server', 'create');
		try {
			const selectedVersion = modpackVersions.find((v) => v.id === selectedVersionId);
			const versionToSend =
				selectedModpack?.indexer === 'modrinth' && selectedVersion?.versionNumber
					? selectedVersion.versionNumber
					: selectedVersionId;

			const createRequest = {
				...formData,
				nodeId: nodePlacementMode === 'manual' ? selectedNodeId : '',
				placementStrategy: nodePlacementMode === 'auto' ? placementStrategy : '',
				modpackId: selectedModpack?.id || '',
				modpackVersionId: versionToSend || '',
				port: useProxyMode ? 0 : formData.port,
				proxyHostname: useProxyMode && proxyEnabled ? formData.proxyHostname : '',
				proxyListenerId: useProxyMode && proxyEnabled ? formData.proxyListenerId : '',
				requestId: attempt.requestId
			};

			const response = await rpcClient.server.createServer(createRequest);
			const serverId = response.server?.id ?? '';

			// Install the datapacks selected in the wizard (best-effort: the server
			// is usable even if a download fails).
			for (const dp of selectedDatapacks) {
				try {
					await rpcClient.datapack.installDatapack({
						serverId,
						datapackId: dp.id,
						gameVersion: formData.mcVersion,
						indexer: dp.indexer
					});
					toast.success(`Installed datapack ${dp.name}`);
				} catch (error) {
					toast.error(
						`Datapack ${dp.name} failed to install: ${error instanceof Error ? error.message : 'Unknown error'}`
					);
				}
			}

			toast.success(`Server "${response.server?.name}" created successfully!`);
			goto(resolve(`/servers/${serverId}`));
		} catch (error) {
			toast.error(
				`Failed to create server: ${error instanceof Error ? error.message : 'Unknown error'}`
			);
		} finally {
			attempt.finish();
			loading = false;
		}
	}
</script>

<div class="bg-[#161616] font-sans text-[#f4f4f4]">
	<div class="space-y-6">
		<!-- Carbon Page Header -->
		<CarbonPageHeader
			title="Create New Server"
			description="Configure instance runtime, container engine, resource requests, and network proxy"
		>
			{#snippet tag()}
				<span
					class="border border-[#0f62fe]/40 bg-[#0f62fe]/20 px-2 py-0.5 font-mono text-xs text-[#78a9ff]"
				>
					WIZARD
				</span>
			{/snippet}
			{#snippet actions()}
				<CarbonButton
					href={resolve('/servers')}
					kind="tertiary"
					size="md"
					iconOnly
					aria-label="Back to Servers"
					title="Back to Servers"
				>
					<ArrowLeft class="h-4 w-4" aria-hidden="true" />
				</CarbonButton>
			{/snippet}
		</CarbonPageHeader>

		<!-- Carbon Progress Indicator / Steps (Requirement 3) -->
		<div class="w-full rounded-none border border-[#393939] bg-[#262626] p-4">
			<div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
				{#each steps as step (step.id)}
					<button
						type="button"
						aria-current={currentStep === step.id ? 'step' : undefined}
						onclick={() => goToStep(step.id)}
						class="focus-ring relative flex cursor-pointer items-center gap-3 rounded-none border p-3 text-left transition-colors select-none {currentStep ===
						step.id
							? 'border-[#0f62fe] bg-[#353535] shadow-[inset_3px_0_0_#0f62fe]'
							: currentStep > step.id
								? 'border-[#393939] bg-[#161616] hover:bg-[#262626]'
								: 'border-[#393939] bg-[#161616] opacity-70 hover:opacity-100'}"
					>
						<!-- Indicator Badge -->
						<div
							class="flex h-7 w-7 shrink-0 items-center justify-center rounded-none font-mono text-xs font-bold {currentStep ===
							step.id
								? 'bg-[#0f62fe] text-white'
								: currentStep > step.id
									? 'bg-[#24a148] text-white'
									: 'bg-[#393939] text-[#8d8d8d]'}"
						>
							{#if currentStep > step.id}
								<Check class="h-4 w-4" />
							{:else}
								{step.id + 1}
							{/if}
						</div>

						<div class="min-w-0 flex-1">
							<p
								class="text-[10px] leading-tight font-semibold tracking-wide break-words uppercase {currentStep ===
								step.id
									? 'text-[#f4f4f4]'
									: currentStep > step.id
										? 'text-[#c6c6c6]'
										: 'text-[#8d8d8d]'}"
							>
								{step.title}
							</p>
							<p class="mt-0.5 font-sans text-xs break-words text-[#8d8d8d]">
								{step.subtitle}
							</p>
						</div>
					</button>
				{/each}
			</div>
		</div>

		<!-- Wizard Form Container -->
		<form onsubmit={handleSubmit} class="space-y-6">
			<!-- Step 0: Basic Information & Preset -->
			{#if currentStep === 0}
				<div class="space-y-6 rounded-none border border-[#393939] bg-[#262626] p-6">
					<div>
						<h2 class="text-lg font-light text-[#f4f4f4]">Step 1: Basic Information & Preset</h2>
						<p class="mt-1 font-sans text-xs text-[#a8a8a8]">
							Provide server identification and select whether to build manual config or load from a
							curated modpack.
						</p>
					</div>

					<!-- Preset Selection (Carbon Tiles) -->
					<div class="space-y-2">
						<span
							id="config-method-label"
							class="block font-mono text-xs font-semibold tracking-wider text-[#c6c6c6] uppercase"
						>
							Configuration Method
						</span>
						<div
							role="radiogroup"
							aria-labelledby="config-method-label"
							class="grid grid-cols-1 gap-3 sm:grid-cols-2"
						>
							<!-- Manual Tile -->
							<button
								type="button"
								role="radio"
								aria-checked={selectedModpack === null}
								onclick={() => (selectedModpack = null)}
								class="focus-ring relative cursor-pointer rounded-none border p-4 text-left transition-colors {selectedModpack ===
								null
									? 'border-[#0f62fe] bg-[#353535] shadow-[inset_3px_0_0_#0f62fe]'
									: 'border-[#393939] bg-[#161616] hover:bg-[#262626]'}"
							>
								<div class="flex items-center gap-2">
									<Settings class="h-4 w-4 text-accent-text" />
									<h4 class="text-sm font-semibold text-[#f4f4f4]">Manual Configuration</h4>
								</div>
								<p class="mt-1 text-xs text-[#a8a8a8]">
									Start with clean Minecraft server settings and custom modloaders.
								</p>
							</button>

							<!-- Modpack Tile -->
							<button
								type="button"
								role="radio"
								aria-checked={selectedModpack !== null}
								onclick={() => (showModpackDialog = true)}
								class="focus-ring relative cursor-pointer rounded-none border p-4 text-left transition-colors {selectedModpack !==
								null
									? 'border-[#0f62fe] bg-[#353535] shadow-[inset_3px_0_0_#0f62fe]'
									: 'border-[#393939] bg-[#161616] hover:bg-[#262626]'}"
							>
								<div class="flex items-center gap-2">
									<Package class="h-4 w-4 text-[#78a9ff]" />
									<h4 class="text-sm font-semibold text-[#f4f4f4]">
										{selectedModpack ? selectedModpack.name : 'From Modpack Preset'}
									</h4>
								</div>
								<p class="mt-1 text-xs text-[#a8a8a8]">
									{selectedModpack
										? `${selectedModpack.indexer} • MC ${selectedModpack.mcVersion}`
										: favoriteModpacks.length === 0
											? 'Browse favorites or modpack repo'
											: 'Select from favorite saved modpacks'}
								</p>
							</button>
						</div>

						{#if selectedModpack}
							<div
								class="mt-3 flex items-start justify-between gap-4 rounded-none border border-[#0f62fe] bg-[#161616] p-4"
							>
								<div class="flex min-w-0 items-start gap-3">
									{#if selectedModpack.logoUrl}
										<img
											src={selectedModpack.logoUrl}
											alt={selectedModpack.name}
											class="h-12 w-12 shrink-0 rounded-none border border-[#393939] object-cover"
										/>
									{/if}
									<div class="min-w-0">
										<div class="flex items-center gap-2">
											<h4 class="text-sm font-semibold text-[#f4f4f4]">{selectedModpack.name}</h4>
											<CarbonTag type="blue" size="sm">{selectedModpack.indexer}</CarbonTag>
										</div>
										<p class="mt-1 line-clamp-2 font-sans text-xs text-[#a8a8a8]">
											{selectedModpack.summary}
										</p>
										{#if modpackVersions.length > 0 || loadingModpackVersions}
											<div class="mt-2 flex items-center gap-2">
												<span class="font-mono text-xs text-[#c6c6c6]">Version:</span>
												<select
													bind:value={selectedVersionId}
													disabled={loadingModpackVersions}
													class="h-8 rounded-none border border-[#525252] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4] focus:border-[#0f62fe] focus:outline-none disabled:opacity-50"
												>
													<option value="">
														{loadingModpackVersions ? 'Loading versions…' : 'Latest Release'}
													</option>
													{#each modpackVersions as version (version.id)}
														<option value={version.id}>
															{version.displayName}
															{version.releaseType ? `(${version.releaseType})` : ''}
														</option>
													{/each}
												</select>
											</div>
										{/if}
									</div>
								</div>
								<button
									type="button"
									onclick={removeModpack}
									class="shrink-0 cursor-pointer rounded-none bg-[#393939] px-3 py-1.5 font-mono text-xs text-white uppercase transition-colors hover:bg-[#da1e28]"
								>
									Remove
								</button>
							</div>
						{/if}
					</div>

					<!-- Server Name (Carbon TextInput) -->
					<CarbonTextInput
						id="name"
						label="Server Name *"
						placeholder="survival-smp-01"
						bind:value={formData.name}
						required
					/>

					<!-- Server Description -->
					<div class="space-y-1.5">
						<label
							for="description"
							class="block font-mono text-xs font-semibold tracking-wider text-[#c6c6c6] uppercase"
						>
							Description <span class="text-xs font-normal text-[#8d8d8d] lowercase"
								>(optional)</span
							>
						</label>
						<textarea
							id="description"
							placeholder="Primary survival server instance for cluster..."
							bind:value={formData.description}
							rows={3}
							class="focus-ring w-full resize-none rounded-none border border-[#393939] bg-[#161616] p-3 text-sm text-[#f4f4f4] transition-colors placeholder:text-[#8d8d8d] focus:bg-[#353535]"
						></textarea>
					</div>

					{#if catalogBlueprints.length > 0}
						<div class="space-y-2">
							<div class="flex items-center justify-between gap-2">
								<p class="font-mono text-xs font-semibold tracking-wider text-[#c6c6c6] uppercase">
									Catalog seed
								</p>
								{#if selectedBlueprintId}
									<button
										type="button"
										onclick={clearBlueprint}
										class="font-mono text-xs text-[#78a9ff] uppercase hover:underline"
									>
										Clear seed
									</button>
								{/if}
							</div>
							<p class="font-sans text-xs text-[#8d8d8d]">
								A qualified seed fills Java image, loader TYPE and protocol. Unsupported rows are
								not a native Bedrock edition path.
							</p>
							<div class="grid gap-2 sm:grid-cols-2">
								{#each catalogBlueprints as seed (seed.id)}
									{@const spec = resolveCreateBlueprint(seed)}
									<button
										type="button"
										disabled={!spec.qualified || loading}
										onclick={() => applyBlueprint(seed)}
										class="rounded-none border p-3 text-left transition-colors {selectedBlueprintId ===
										seed.id
											? 'border-[#0f62fe] bg-[#161616]'
											: 'border-[#393939] bg-[#161616]'} {spec.qualified
											? 'hover:border-[#78a9ff]'
											: 'cursor-not-allowed opacity-80'}"
									>
										<div class="flex items-center justify-between gap-2">
											<span class="text-sm font-semibold text-[#f4f4f4]">{seed.name}</span>
											{#if spec.qualified}
												<CarbonTag type="green" size="sm">Java</CarbonTag>
											{:else}
												<CarbonTag type="yellow" size="sm">Unsupported</CarbonTag>
											{/if}
										</div>
										<p class="mt-1 font-sans text-xs text-[#a8a8a8]">{seed.description}</p>
										{#if !spec.qualified}
											<p class="mt-2 font-sans text-xs text-[#f1c21b]">{spec.limitation}</p>
										{/if}
									</button>
								{/each}
							</div>
						</div>
					{/if}
				</div>

				<!-- Step 1: Game Engine & Version -->
			{:else if currentStep === 1}
				<div class="space-y-6 rounded-none border border-[#393939] bg-[#262626] p-6">
					<div>
						<h2 class="text-lg font-light text-[#f4f4f4]">Step 2: Engine & Version</h2>
						<p class="mt-1 font-sans text-xs text-[#a8a8a8]">
							Select the Minecraft server release and runtime loader.
						</p>
					</div>

					<!-- Minecraft Version (Carbon Select) -->
					<div class="space-y-1.5">
						<label
							for="mcVersion"
							class="block font-mono text-xs font-semibold tracking-wider text-[#c6c6c6] uppercase"
						>
							Minecraft Version
						</label>
						{#if loadingVersions}
							<div class="flex items-center gap-2 border border-[#393939] bg-[#161616] p-3">
								<CarbonInlineLoading description="Loading supported versions catalog..." />
							</div>
						{:else}
							<div class="relative">
								<select
									id="mcVersion"
									bind:value={formData.mcVersion}
									disabled={loading}
									class="focus-ring h-10 w-full cursor-pointer appearance-none rounded-none border border-[#393939] bg-[#161616] px-4 pr-10 text-sm text-[#f4f4f4] transition-colors focus:bg-[#353535]"
								>
									{#each minecraftVersions as version (version)}
										<option value={version}>
											{version}
											{version === latestVersion ? '(Latest Official)' : ''}
										</option>
									{/each}
								</select>
								<div
									class="pointer-events-none absolute inset-y-0 right-0 flex items-center px-3 text-[#c6c6c6]"
								>
									<svg class="h-4 w-4" fill="currentColor" viewBox="0 0 20 20">
										<path
											fill-rule="evenodd"
											d="M5.293 7.293a1 1 0 011.414 0L10 10.586l3.293-3.293a1 1 0 111.414 1.414l-4 4a1 1 0 01-1.414 0l-4-4a1 1 0 010-1.414z"
											clip-rule="evenodd"
										/>
									</svg>
								</div>
							</div>
						{/if}
					</div>

					<!-- Mod Loader (Carbon Select) -->
					<div class="space-y-1.5">
						<label
							for="modLoader"
							class="block font-mono text-xs font-semibold tracking-wider text-[#c6c6c6] uppercase"
						>
							Mod Loader / Server Software
						</label>
						<div class="relative">
							<select
								id="modLoader"
								value={formData.modLoader.toString()}
								onchange={(e) => {
									const val = Number(e.currentTarget.value);
									formData.modLoader = val as ModLoader;
								}}
								disabled={loading || !!selectedModpack}
								class="focus-ring h-10 w-full cursor-pointer appearance-none rounded-none border border-[#393939] bg-[#161616] px-4 pr-10 text-sm text-[#f4f4f4] transition-colors focus:bg-[#353535] disabled:opacity-50"
							>
								{#each modLoaders as loader (loader.name)}
									{@const enumVal =
										ModLoader[loader.name.toUpperCase() as keyof typeof ModLoader] ?? 0}
									<option value={enumVal.toString()}>
										{loader.displayName}
									</option>
								{/each}
							</select>
							<div
								class="pointer-events-none absolute inset-y-0 right-0 flex items-center px-3 text-[#c6c6c6]"
							>
								<svg class="h-4 w-4" fill="currentColor" viewBox="0 0 20 20">
									<path
										fill-rule="evenodd"
										d="M5.293 7.293a1 1 0 011.414 0L10 10.586l3.293-3.293a1 1 0 111.414 1.414l-4 4a1 1 0 01-1.414 0l-4-4a1 1 0 010-1.414z"
										clip-rule="evenodd"
									/>
								</svg>
							</div>
						</div>

						{#if selectedModpack}
							<p class="mt-1 font-mono text-xs text-[#78a9ff]">
								Mod loader auto-determined by preset configuration.
							</p>
						{:else if formData.modLoader === ModLoader.VANILLA}
							<p class="mt-1 font-sans text-xs text-[#8d8d8d]">
								Vanilla Minecraft server binary with no mod dependencies.
							</p>
						{:else}
							<p class="mt-1 font-sans text-xs text-[#6fdc8c]">
								Supports runtime mod jar injection and server extensions.
							</p>
						{/if}
					</div>

					{#if loaderTakesVersion && !selectedModpack}
						<!-- Loader Version (Carbon Select) -->
						<div class="space-y-1.5">
							<label
								for="loaderVersion"
								class="block font-mono text-xs font-semibold tracking-wider text-[#c6c6c6] uppercase"
							>
								{formData.modLoader === ModLoader.NEOFORGE ? 'NeoForge' : 'Loader'} Version
							</label>
							<div class="relative">
								<select
									id="loaderVersion"
									value={formData.loaderVersion}
									onchange={(e) => (formData.loaderVersion = e.currentTarget.value)}
									disabled={loading || loadingLoaderVersions || !formData.mcVersion}
									class="h-10 w-full cursor-pointer appearance-none rounded-none border-b border-[#8d8d8d] bg-[#161616] px-4 pr-10 text-sm text-[#f4f4f4] focus:border-b-2 focus:border-[#0f62fe] focus:bg-[#262626] focus:outline-none disabled:opacity-50"
								>
									<option value="">Latest (recommended)</option>
									{#each loaderVersions as version (version)}
										{#if version !== 'latest'}
											<option value={version}>{version}</option>
										{/if}
									{/each}
								</select>
								<div
									class="pointer-events-none absolute inset-y-0 right-0 flex items-center px-3 text-[#c6c6c6]"
								>
									<svg class="h-4 w-4" fill="currentColor" viewBox="0 0 20 20">
										<path
											fill-rule="evenodd"
											d="M5.293 7.293a1 1 0 011.414 0L10 10.586l3.293-3.293a1 1 0 111.414 1.414l-4 4a1 1 0 01-1.414 0l-4-4a1 1 0 010-1.414z"
											clip-rule="evenodd"
										/>
									</svg>
								</div>
							</div>
							<p class="mt-1 font-sans text-xs text-[#8d8d8d]">
								{loadingLoaderVersions
									? 'Looking up published builds…'
									: 'Pin an exact build, or leave it on Latest to follow the loader.'}
							</p>
						</div>
					{/if}
				</div>

				<!-- Step 2: Resources & Network -->
			{:else if currentStep === 2}
				<div class="space-y-6 rounded-none border border-[#393939] bg-[#262626] p-6">
					<div>
						<h2 class="text-lg font-light text-[#f4f4f4]">Step 3: Resources & Network</h2>
						<p class="mt-1 font-sans text-xs text-[#a8a8a8]">
							Configure port routing, reverse proxy domain, and a memory request (not a hard cap
							until the engine reports it as enforced).
						</p>
					</div>

					<!-- Connection Method (Carbon Tiles) -->
					{#if proxyEnabled}
						<div class="space-y-2">
							<span
								id="ingress-method-label"
								class="block font-mono text-xs font-semibold tracking-wider text-[#c6c6c6] uppercase"
							>
								Connection Ingress Method
							</span>
							<div
								role="radiogroup"
								aria-labelledby="ingress-method-label"
								class="grid grid-cols-1 gap-3 sm:grid-cols-2"
							>
								<button
									type="button"
									role="radio"
									aria-checked={!useProxyMode}
									onclick={() => {
										useProxyMode = false;
										formData.proxyHostname = '';
										portError = '';
									}}
									class="focus-ring relative cursor-pointer rounded-none border p-4 text-left transition-colors {!useProxyMode
										? 'border-[#0f62fe] bg-[#353535] shadow-[inset_3px_0_0_#0f62fe]'
										: 'border-[#393939] bg-[#161616] hover:bg-[#262626]'}"
								>
									<h4 class="text-sm font-semibold text-[#f4f4f4]">Direct Port</h4>
									<p class="mt-1 text-xs text-[#a8a8a8]">
										Bind directly to container host port (e.g. :25565)
									</p>
								</button>
								<button
									type="button"
									role="radio"
									aria-checked={useProxyMode}
									onclick={() => {
										useProxyMode = true;
										if (!formData.proxyHostname) {
											formData.proxyHostname =
												formData.name.toLowerCase().replace(/\s+/g, '-') || 'minecraft-server';
										}
										const currentListener =
											proxyListeners.find((l) => l.id === formData.proxyListenerId) ||
											proxyListeners[0];
										if (currentListener) {
											formData.port = currentListener.port;
										}
										portError = '';
									}}
									class="focus-ring relative cursor-pointer rounded-none border p-4 text-left transition-colors {useProxyMode
										? 'border-[#0f62fe] bg-[#353535] shadow-[inset_3px_0_0_#0f62fe]'
										: 'border-[#393939] bg-[#161616] hover:bg-[#262626]'}"
								>
									<h4 class="text-sm font-semibold text-[#f4f4f4]">SNI / Custom Hostname</h4>
									<p class="mt-1 text-xs text-[#a8a8a8]">
										Route traffic through proxy using virtual domain hostname
									</p>
								</button>
							</div>
						</div>
					{/if}

					<!-- Port or Proxy Hostname inputs -->
					{#if useProxyMode && proxyEnabled}
						<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
							{#if proxyListeners.length > 0}
								<div class="space-y-1.5">
									<label
										for="proxy_listener"
										class="block font-mono text-xs font-semibold tracking-wider text-[#c6c6c6] uppercase"
									>
										Proxy Listener
									</label>
									<div class="relative">
										<select
											id="proxy_listener"
											bind:value={formData.proxyListenerId}
											class="focus-ring h-10 w-full cursor-pointer appearance-none rounded-none border border-[#393939] bg-[#161616] px-4 pr-10 text-sm text-[#f4f4f4] transition-colors focus:bg-[#353535]"
										>
											{#each proxyListeners as listener (listener.id)}
												<option value={listener.id}>
													{listener.name} (Port {listener.port}) {listener.isDefault
														? '[Default]'
														: ''}
												</option>
											{/each}
										</select>
										<div
											class="pointer-events-none absolute inset-y-0 right-0 flex items-center px-3 text-[#c6c6c6]"
										>
											<svg class="h-4 w-4" fill="currentColor" viewBox="0 0 20 20">
												<path
													fill-rule="evenodd"
													d="M5.293 7.293a1 1 0 011.414 0L10 10.586l3.293-3.293a1 1 0 111.414 1.414l-4 4a1 1 0 01-1.414 0l-4-4a1 1 0 010-1.414z"
													clip-rule="evenodd"
												/>
											</svg>
										</div>
									</div>
								</div>
							{/if}

							<CarbonTextInput
								id="proxy_hostname"
								label="Server Hostname"
								placeholder={proxyBaseURL ? 'survival' : 'survival.example.com'}
								bind:value={formData.proxyHostname}
							/>
						</div>
					{:else}
						<div class="space-y-1.5">
							<div class="flex items-center justify-between">
								<label
									for="port"
									class="font-mono text-xs font-semibold tracking-wider text-[#c6c6c6] uppercase"
								>
									Server Port
								</label>
								<button
									type="button"
									onclick={refreshAvailablePort}
									class="focus-ring cursor-pointer font-mono text-xs text-[#78a9ff] hover:underline"
								>
									Auto-Assign Next Port
								</button>
							</div>
							<input
								id="port"
								type="number"
								min="1"
								max="65535"
								bind:value={formData.port}
								oninput={(e) => validatePort(Number(e.currentTarget.value))}
								aria-invalid={portError ? 'true' : undefined}
								aria-describedby={portError ? 'port-error' : undefined}
								class="focus-ring h-10 w-full rounded-none border border-[#393939] bg-[#161616] px-4 font-mono text-sm text-[#f4f4f4] transition-colors focus:bg-[#353535] {portError
									? '!border-[#da1e28]'
									: ''}"
							/>
							{#if portError}
								<p id="port-error" class="mt-1 font-mono text-xs text-[#ff8389]">{portError}</p>
							{/if}
						</div>
					{/if}

					<!-- Memory request and max players -->
					<div class="grid grid-cols-1 gap-4 pt-2 sm:grid-cols-2">
						<div class="space-y-1.5">
							<label
								for="memory"
								class="block font-mono text-xs font-semibold tracking-wider text-[#c6c6c6] uppercase"
							>
								Memory request (MB)
							</label>
							<input
								id="memory"
								type="number"
								min="512"
								step="256"
								bind:value={formData.memory}
								class="focus-ring h-10 w-full rounded-none border border-[#393939] bg-[#161616] px-4 font-mono text-sm text-[#f4f4f4] transition-colors focus:bg-[#353535]"
							/>
							<p class="font-mono text-xs text-[#8d8d8d]">
								Equals {(formData.memory / 1024).toFixed(1)} GB · not a hard cap if the engine reports
								memory_limit_unsupported
								{#if selectedModpack && selectedModpack.recommendedRam > 0}
									· Recommended for this pack: {selectedModpack.recommendedRam} MB
								{/if}
							</p>
						</div>

						<div class="space-y-1.5">
							<label
								for="max_players"
								class="block font-mono text-xs font-semibold tracking-wider text-[#c6c6c6] uppercase"
							>
								Max Players
							</label>
							<input
								id="max_players"
								type="number"
								min="1"
								max="1000"
								bind:value={formData.maxPlayers}
								class="focus-ring h-10 w-full rounded-none border border-[#393939] bg-[#161616] px-4 font-mono text-sm text-[#f4f4f4] transition-colors focus:bg-[#353535]"
							/>
						</div>
					</div>

					<!-- Additional Ports Editor -->
					<div class="border-t border-[#393939] pt-2">
						<AdditionalPortsEditor
							bind:ports={formData.additionalPorts}
							disabled={loading}
							{usedPorts}
							onchange={(ports) => (formData.additionalPorts = ports)}
						/>
					</div>
				</div>

				<!-- Step 3: Cluster Placement & Runtime -->
			{:else if currentStep === 3}
				<div class="space-y-6 rounded-none border border-[#393939] bg-[#262626] p-6">
					<div>
						<h2 class="text-lg font-light text-[#f4f4f4]">Step 4: Cluster Placement & Runtime</h2>
						<p class="mt-1 font-sans text-xs text-[#a8a8a8]">
							Designate physical cluster host and configure container startup options.
						</p>
					</div>

					<!-- Cluster Placement Mode (Carbon Tiles) -->
					<div class="space-y-3">
						<span
							id="node-placement-label"
							class="block font-mono text-xs font-semibold tracking-wider text-[#c6c6c6] uppercase"
						>
							Node Placement Mode
						</span>
						<div
							role="radiogroup"
							aria-labelledby="node-placement-label"
							class="grid grid-cols-1 gap-3 sm:grid-cols-2"
						>
							<button
								type="button"
								role="radio"
								aria-checked={nodePlacementMode === 'auto'}
								onclick={() => (nodePlacementMode = 'auto')}
								class="focus-ring relative cursor-pointer rounded-none border p-4 text-left transition-colors {nodePlacementMode ===
								'auto'
									? 'border-[#0f62fe] bg-[#353535] shadow-[inset_3px_0_0_#0f62fe]'
									: 'border-[#393939] bg-[#161616] hover:bg-[#262626]'}"
							>
								<div class="flex items-center gap-2">
									<Activity class="h-4 w-4 text-accent-text" />
									<h4 class="text-sm font-semibold text-[#f4f4f4]">
										Auto-Placement (Load Balanced)
									</h4>
								</div>
								<p class="mt-1 text-xs text-[#a8a8a8]">
									Cluster scheduler selects optimal worker node based on available memory and active
									workload.
								</p>
							</button>

							<button
								type="button"
								role="radio"
								aria-checked={nodePlacementMode === 'manual'}
								onclick={() => (nodePlacementMode = 'manual')}
								class="focus-ring relative cursor-pointer rounded-none border p-4 text-left transition-colors {nodePlacementMode ===
								'manual'
									? 'border-[#0f62fe] bg-[#353535] shadow-[inset_3px_0_0_#0f62fe]'
									: 'border-[#393939] bg-[#161616] hover:bg-[#262626]'}"
							>
								<div class="flex items-center gap-2">
									<ServerIcon class="h-4 w-4 text-[#78a9ff]" />
									<h4 class="text-sm font-semibold text-[#f4f4f4]">Target Specific Node</h4>
								</div>
								<p class="mt-1 text-xs text-[#a8a8a8]">
									Pin this Minecraft container instance to a designated Docker host machine.
								</p>
							</button>
						</div>

						{#if nodePlacementMode === 'auto'}
							<div class="space-y-2 rounded-none border border-[#393939] bg-[#161616] p-4">
								<label
									for="placement_strategy"
									class="block font-mono text-xs text-[#c6c6c6] uppercase"
								>
									Placement Strategy
								</label>
								<select
									id="placement_strategy"
									bind:value={placementStrategy}
									class="focus-ring h-10 w-full rounded-none border border-[#525252] bg-[#262626] px-3 font-mono text-xs text-[#f4f4f4] transition-colors focus:bg-[#353535]"
								>
									<option value="least_memory">Least Memory Allocated (Recommended)</option>
									<option value="least_servers">Least Active Server Count</option>
									<option value="round_robin">Round Robin Distribution</option>
								</select>
							</div>
						{:else}
							<div class="space-y-2">
								{#if loadingNodes}
									<div
										class="flex items-center justify-center border border-[#393939] bg-[#161616] p-6"
									>
										<CarbonInlineLoading description="Scanning cluster topology..." />
									</div>
								{:else if nodes.length === 0}
									<div class="border border-[#393939] bg-[#161616] p-4 text-xs text-[#a8a8a8]">
										No worker nodes available. Add and enable a Docker node in Settings → Docker
										Nodes before creating servers.
									</div>
								{:else}
									<div
										role="radiogroup"
										aria-label="Target Docker node"
										class="grid grid-cols-1 gap-3 sm:grid-cols-2"
									>
										{#each nodes as node (node.id)}
											<button
												type="button"
												role="radio"
												aria-checked={selectedNodeId === node.id}
												aria-disabled={!node.enabled}
												onclick={() => {
													if (node.enabled) selectedNodeId = node.id;
												}}
												class="focus-ring relative cursor-pointer rounded-none border p-3.5 text-left transition-colors {selectedNodeId ===
												node.id
													? 'border-[#0f62fe] bg-[#353535] shadow-[inset_3px_0_0_#0f62fe]'
													: 'border-[#393939] bg-[#161616] hover:bg-[#262626]'} {!node.enabled
													? 'cursor-not-allowed opacity-40'
													: ''}"
											>
												<div class="flex items-center justify-between">
													<div class="flex items-center gap-2">
														<span class="text-sm font-medium text-[#f4f4f4]">{node.name}</span>
														{#if node.isLocal}
															<span
																class="py-0.2 bg-[#393939] px-1.5 font-mono text-xs text-[#c6c6c6]"
																>Local</span
															>
														{/if}
													</div>
													<CarbonTag
														type={node.status === NodeStatus.ONLINE ? 'green' : 'red'}
														size="sm"
													>
														{node.status === NodeStatus.ONLINE ? 'ONLINE' : 'OFFLINE'}
													</CarbonTag>
												</div>
												<p class="mt-1 truncate font-mono text-xs text-[#8d8d8d]">
													{node.host || node.advertisedIp || 'local socket'}
												</p>
												<div
													class="mt-1 flex items-center gap-1.5 font-mono text-xs text-[#8d8d8d]"
												>
													{#if Number(node.availableMemoryMb) > 0}
														<span class="text-emerald-400"
															>{(Number(node.availableMemoryMb) / 1024).toFixed(1)} GB free</span
														>
													{:else if Number(node.maxMemoryMb) > 0}
														<span
															class={Number(node.maxMemoryMb) > Number(node.allocatedMemoryMb)
																? 'text-emerald-400'
																: 'text-rose-400'}
														>
															{(
																Math.max(
																	0,
																	Number(node.maxMemoryMb) - Number(node.allocatedMemoryMb)
																) / 1024
															).toFixed(1)} GB free
														</span>
													{/if}
													<span>•</span>
													<span
														>{(Number(node.allocatedMemoryMb) / 1024).toFixed(1)} GB allocated</span
													>
												</div>
											</button>
										{/each}
									</div>
								{/if}
							</div>
						{/if}
					</div>

					<!-- Advanced Docker Image -->
					<div class="space-y-1.5 border-t border-[#393939] pt-2">
						<label
							for="docker_image"
							class="block font-mono text-xs font-semibold tracking-wider text-[#c6c6c6] uppercase"
						>
							Docker Image <span class="text-xs font-normal text-[#8d8d8d] lowercase"
								>(Advanced)</span
							>
						</label>
						<div class="relative">
							<select
								id="docker_image"
								bind:value={formData.dockerImage}
								class="focus-ring h-10 w-full cursor-pointer appearance-none rounded-none border border-[#393939] bg-[#161616] px-4 pr-10 font-mono text-sm text-[#f4f4f4] transition-colors focus:bg-[#353535]"
							>
								<option value="">Auto-select base image (Recommended)</option>
								{#each getUniqueDockerImages(dockerImages) as image (image.tag)}
									<option value={image.tag}>
										{getDockerImageDisplayName(image)}
									</option>
								{/each}
							</select>
							<div
								class="pointer-events-none absolute inset-y-0 right-0 flex items-center px-3 text-[#c6c6c6]"
							>
								<svg class="h-4 w-4" fill="currentColor" viewBox="0 0 20 20">
									<path
										fill-rule="evenodd"
										d="M5.293 7.293a1 1 0 011.414 0L10 10.586l3.293-3.293a1 1 0 111.414 1.414l-4 4a1 1 0 01-1.414 0l-4-4a1 1 0 010-1.414z"
										clip-rule="evenodd"
									/>
								</svg>
							</div>
						</div>
					</div>

					<!-- Lifecycle Toggles -->
					<div class="space-y-3 border-t border-[#393939] pt-2">
						<h3 class="font-mono text-xs font-semibold tracking-wider text-[#c6c6c6] uppercase">
							Lifecycle & Autostart
						</h3>
						<div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
							<!-- Start Immediately -->
							<label
								class="flex cursor-pointer items-center gap-3 rounded-none border border-[#393939] bg-[#161616] p-3 select-none hover:bg-[#262626]"
							>
								<input
									type="checkbox"
									bind:checked={formData.startImmediately}
									class="h-4 w-4 rounded-none accent-[#0f62fe]"
								/>
								<div>
									<span class="block text-xs font-medium text-[#f4f4f4]">Start Immediately</span>
									<span class="block font-sans text-xs text-[#8d8d8d]"
										>Boot container upon creation</span
									>
								</div>
							</label>

							<!-- Detached Mode -->
							<label
								class="flex cursor-pointer items-center gap-3 rounded-none border border-[#393939] bg-[#161616] p-3 select-none hover:bg-[#262626] {useProxyMode
									? 'cursor-not-allowed opacity-50'
									: ''}"
							>
								<input
									type="checkbox"
									bind:checked={formData.detached}
									disabled={useProxyMode}
									class="h-4 w-4 rounded-none accent-[#0f62fe]"
								/>
								<div>
									<span class="block text-xs font-medium text-[#f4f4f4]">Detached Mode</span>
									<span class="block font-sans text-xs text-[#8d8d8d]"
										>Persists past daemon stop</span
									>
								</div>
							</label>

							<!-- Auto Start -->
							<label
								class="flex cursor-pointer items-center gap-3 rounded-none border border-[#393939] bg-[#161616] p-3 select-none hover:bg-[#262626] {formData.detached
									? 'cursor-not-allowed opacity-50'
									: ''}"
							>
								<input
									type="checkbox"
									bind:checked={formData.autoStart}
									disabled={formData.detached}
									class="h-4 w-4 rounded-none accent-[#0f62fe]"
								/>
								<div>
									<span class="block text-xs font-medium text-[#f4f4f4]">Auto Start</span>
									<span class="block font-sans text-xs text-[#8d8d8d]"
										>Start when host daemon starts</span
									>
								</div>
							</label>

							<!-- Freeze on Idle -->
							<label
								class="flex cursor-pointer items-center gap-3 rounded-none border border-[#393939] bg-[#161616] p-3 select-none hover:bg-[#262626]"
							>
								<input
									type="checkbox"
									bind:checked={formData.autoHibernate}
									class="h-4 w-4 rounded-none accent-[#0f62fe]"
								/>
								<div>
									<span class="block text-xs font-medium text-[#f4f4f4]">Freeze on Idle</span>
									<span class="block font-sans text-xs text-[#8d8d8d]"
										>Pause container when empty</span
									>
								</div>
							</label>

							<!-- Deep Sleep on Idle -->
							<label
								class="flex cursor-pointer items-center gap-3 rounded-none border border-[#393939] bg-[#161616] p-3 select-none hover:bg-[#262626]"
							>
								<input
									type="checkbox"
									bind:checked={formData.autoDeepSleep}
									class="h-4 w-4 rounded-none accent-[#0f62fe]"
								/>
								<div>
									<span class="block text-xs font-medium text-[#f4f4f4]">Deep Sleep on Idle</span>
									<span class="block font-sans text-xs text-[#8d8d8d]"
										>Stop container when empty</span
									>
								</div>
							</label>
						</div>
					</div>

					<!-- Docker Overrides -->
					<div class="border-t border-[#393939] pt-2">
						<DockerOverridesEditor
							bind:overrides={formData.dockerOverrides}
							disabled={loading}
							onchange={(overrides) => (formData.dockerOverrides = overrides)}
						/>
					</div>
				</div>
			{:else if currentStep === 4}
				<div class="space-y-5">
					<div>
						<h2 class="text-lg font-light text-[#f4f4f4]">Step 5: Datapacks (Optional)</h2>
						<p class="mt-1 text-xs text-[#a8a8a8]">
							Search and select datapacks to install into the world after the server is created.
							Datapacks run on Vanilla servers with no mod loader — skip this step and add them
							later from the server's Datapacks tab if you prefer.
						</p>
					</div>

					{#if selectedDatapacks.length > 0}
						<div class="border border-[#393939] bg-[#262626]">
							<div
								class="border-b border-[#393939] px-4 py-2.5 text-xs font-semibold text-[#f4f4f4] uppercase"
							>
								Selected for install ({selectedDatapacks.length})
							</div>
							<ul class="divide-y divide-[#393939]">
								{#each selectedDatapacks as dp (dp.id)}
									<li class="flex items-center justify-between px-4 py-2.5 text-sm text-[#f4f4f4]">
										<span class="flex items-center gap-2">
											<Package class="h-4 w-4 text-[#8d8d8d]" />
											{dp.name}
										</span>
										<button
											type="button"
											onclick={() => removeDatapack(dp.id)}
											aria-label={`Remove datapack ${dp.name}`}
											class="flex h-8 w-8 cursor-pointer items-center justify-center text-[#8d8d8d] transition-colors hover:bg-[#353535] hover:text-[#ff8389]"
										>
											<X class="h-4 w-4" />
										</button>
									</li>
								{/each}
							</ul>
						</div>
					{/if}

					<DatapackSearch
						gameVersion={formData.mcVersion}
						emptyHint="Search for datapacks, or skip this step and add them later."
					>
						{#snippet actions(d)}
							{@const added = selectedDatapacks.some((p) => p.id === d.id)}
							<button
								type="button"
								aria-pressed={added}
								onclick={() => toggleDatapack(d.id, d.name, d.indexer)}
								class="focus-ring flex h-9 cursor-pointer items-center gap-1.5 rounded-none px-3 font-sans text-xs font-medium transition-colors {added
									? 'bg-[#393939] text-[#c6c6c6] hover:bg-[#525252]'
									: 'bg-[#0f62fe] text-white hover:bg-[#0353e9]'}"
							>
								{#if added}<Check class="h-3.5 w-3.5" /> Added{:else}<Plus class="h-3.5 w-3.5" /> Add{/if}
							</button>
						{/snippet}
					</DatapackSearch>
				</div>
			{/if}

			<!-- Carbon Wizard Navigation Bar -->
			<div class="flex items-center justify-between gap-3 border-t border-[#393939] pt-4">
				<CarbonButton
					href={resolve('/servers')}
					kind="tertiary"
					size="md"
					class="whitespace-nowrap"
				>
					Cancel
				</CarbonButton>

				<div class="flex items-center gap-2">
					{#if currentStep > 0}
						<CarbonButton
							kind="secondary"
							size="md"
							class="gap-2 whitespace-nowrap"
							onclick={prevStep}
						>
							<ChevronLeft class="h-4 w-4" aria-hidden="true" />
							<span>Previous</span>
						</CarbonButton>
					{/if}

					{#if currentStep < steps.length - 1}
						<CarbonButton
							kind="primary"
							size="md"
							class="gap-2 whitespace-nowrap"
							onclick={nextStep}
						>
							<span>Next Step</span>
							<ChevronRight class="h-4 w-4" aria-hidden="true" />
						</CarbonButton>
					{:else}
						<CarbonButton
							type="submit"
							kind="primary"
							size="md"
							class="gap-2 whitespace-nowrap"
							disabled={loading || loadingVersions}
							{loading}
						>
							{#if !loading}
								<Check class="h-4 w-4" aria-hidden="true" />
							{/if}
							<span>{loading ? 'Creating Server...' : 'Create Server'}</span>
						</CarbonButton>
					{/if}
				</div>
			</div>
		</form>
	</div>
</div>

<!-- Modpack Selection Modal (Sharp Carbon Modal) -->
<CarbonModal
	bind:open={showModpackDialog}
	title="Select Favorite Modpack"
	description="Choose a pre-configured modpack from your saved favorites list."
	size="2xl"
	hasFooter={false}
>
	<div class="max-h-[60vh] space-y-3 overflow-y-auto">
		{#if favoriteModpacks.length === 0}
			<div class="rounded-none border border-[#393939] bg-[#262626] p-8 text-center">
				<Package class="mx-auto mb-2 h-8 w-8 text-[#8d8d8d]" />
				<p class="text-sm font-semibold text-[#f4f4f4]">No favorite modpacks yet</p>
				<p class="mt-1 text-xs text-[#a8a8a8]">
					Visit the Modpacks directory to search and favorite packs to use as presets.
				</p>
			</div>
		{:else}
			{#each favoriteModpacks as modpack (modpack.id)}
				<button
					type="button"
					onclick={() => selectModpack(modpack)}
					class="flex w-full cursor-pointer items-start gap-3 rounded-none border border-[#393939] bg-[#262626] p-3.5 text-left transition-colors hover:bg-[#353535]"
				>
					{#if modpack.logoUrl}
						<img
							src={modpack.logoUrl}
							alt={modpack.name}
							class="h-12 w-12 shrink-0 rounded-none border border-[#393939] object-cover"
						/>
					{/if}
					<div class="min-w-0 flex-1">
						<div class="flex items-center gap-2">
							<h4 class="text-sm font-semibold text-[#f4f4f4]">{modpack.name}</h4>
							<CarbonTag type="blue" size="sm">{modpack.indexer}</CarbonTag>
						</div>
						<p class="mt-1 line-clamp-2 text-xs text-[#a8a8a8]">
							{modpack.summary}
						</p>
						<div class="mt-2 flex items-center gap-2 font-mono text-xs text-[#78a9ff]">
							{#if parseJsonArray(modpack.gameVersions).length > 0}
								<span>MC {parseJsonArray(modpack.gameVersions)[0]}</span>
							{/if}
							{#if parseJsonArray(modpack.modLoaders).length > 0}
								<span>• {parseJsonArray(modpack.modLoaders)[0]}</span>
							{/if}
						</div>
					</div>
				</button>
			{/each}
		{/if}
	</div>
</CarbonModal>
