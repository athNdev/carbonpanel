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
	import { beforeNavigate, goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { Code, ConnectError } from '@connectrpc/connect';
	import { beginLifecycleRequest } from '$lib/lifecycle-request';
	import { toast } from 'svelte-sonner';
	import { uploadFile } from '$lib/utils/chunked-upload';
	import DatapackSearch from '$lib/components/datapack-search.svelte';
	import ModBrowserDialog from '$lib/components/mod-browser-dialog.svelte';
	import ModsCatalogResultCard from '$lib/mods/ModsCatalogResultCard.svelte';
	import {
		mergeWizardModFiles,
		supportsWizardMods,
		type QueuedServerMod
	} from '$lib/features/servers/create-content';
	import { loaderDisplayName } from '$lib/utils/mod-loader';
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
		Upload,
		Loader2,
		X,
		Archive
	} from '@lucide/svelte';
	import { create, toJsonString } from '@bufbuild/protobuf';
	import type {
		CreateServerRequest,
		PreviewServerArchiveResponse
	} from '$lib/proto/carbonpanel/v1/server_pb';
	import { CreateServerRequestSchema } from '$lib/proto/carbonpanel/v1/server_pb';
	import {
		ModLoader,
		ServerStatus,
		type ProxyListener,
		type Server
	} from '$lib/proto/carbonpanel/v1/common_pb';
	import {
		ModuleStatus,
		ModuleTemplateType,
		type ModuleTemplate
	} from '$lib/proto/carbonpanel/v1/module_pb';
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
	import {
		createArchiveImport,
		isArchivePreviewCurrent,
		type ArchiveImportState
	} from '$lib/features/servers/archive-import-session';
	import ArchivePreviewPanel from '$lib/features/servers/ArchivePreviewPanel.svelte';
	import {
		ARCHIVE_RPC_OPTIONS,
		describePreview,
		isSupportedArchiveFilename
	} from '$lib/features/servers/archive-presentation';

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
		{ id: 4, title: 'Add-ons', subtitle: 'Mods, datapacks & modules' }
	];
	const addonTabs = ['Datapacks', 'Mods', 'Modules'] as const;
	let addonTab = $state<(typeof addonTabs)[number]>('Datapacks');
	let showModBrowser = $state(false);
	let selectedMods = $state<QueuedServerMod[]>([]);
	let modSelectionTarget = $state('');
	let indexedDatapackTarget = $state('');
	let moduleTemplates = $state<ModuleTemplate[]>([]);
	let selectedModules = $state<ModuleTemplate[]>([]);
	let loadingModules = $state(false);
	let modulesLoaded = $state(false);
	let modulesError = $state('');
	const setupModuleIds = new Set([
		'builtin-geyser',
		'builtin-minecraft-exporter',
		'builtin-resource-pack-host'
	]);

	interface SetupTask {
		name: string;
		run: (serverId: string, retry: boolean) => Promise<void>;
		status: 'pending' | 'running' | 'done' | 'failed';
		error?: string;
	}
	let createdServer = $state<Server | null>(null);
	let setupTasks = $state<SetupTask[]>([]);
	let setupError = $state('');
	let preparingContainer = $state(false);
	let deferredStart = $state(false);
	let startError = $state('');
	let startRequestId = '';
	let pendingCreate: { fingerprint: string; requestId: string } | null = null;

	// Datapack selection — installed into the world after the server is created
	let selectedDatapacks = $state<Array<{ id: string; name: string; indexer: string }>>([]);
	let pendingDatapackUploads = $state<
		Array<{ sessionId: string; fileName: string; name: string; file: File }>
	>([]);
	let datapackUploadInput = $state<HTMLInputElement | null>(null);
	let uploadingWizardDatapack = $state(false);
	let wizardDatapackUploadPercent = $state(0);
	let catalogBlueprints = $state<ServerBlueprint[]>([]);
	let selectedBlueprintId = $state('');

	function toggleDatapack(id: string, name: string, indexer: string) {
		indexedDatapackTarget = datapackTarget;
		if (selectedDatapacks.some((p) => p.id === id)) {
			selectedDatapacks = selectedDatapacks.filter((p) => p.id !== id);
		} else {
			selectedDatapacks = [...selectedDatapacks, { id, name, indexer }];
		}
	}

	function removeDatapack(id: string) {
		selectedDatapacks = selectedDatapacks.filter((p) => p.id !== id);
	}

	async function handleWizardDatapackUpload(event: Event) {
		const input = event.target as HTMLInputElement;
		const file = input.files?.[0];
		input.value = '';
		if (!file) return;

		if (!file.name.toLowerCase().endsWith('.zip')) {
			toast.error('Datapacks must be .zip archives containing pack.mcmeta');
			return;
		}

		uploadingWizardDatapack = true;
		wizardDatapackUploadPercent = 0;
		try {
			const { sessionId } = await uploadFile(file, {
				onProgress: (p) => (wizardDatapackUploadPercent = Math.round(p.percentComplete))
			});
			pendingDatapackUploads = [
				...pendingDatapackUploads.filter(
					(dp) => dp.fileName.toLowerCase() !== file.name.toLowerCase()
				),
				{
					sessionId,
					fileName: file.name,
					name: file.name.replace(/\.zip$/i, ''),
					file
				}
			];
			toast.success(`Uploaded ${file.name} — it will be installed after the server is created`);
		} catch (e) {
			toast.error(e instanceof Error ? e.message : 'Datapack upload failed');
		} finally {
			uploadingWizardDatapack = false;
		}
	}

	function removePendingDatapackUpload(sessionId: string) {
		pendingDatapackUploads = pendingDatapackUploads.filter((u) => u.sessionId !== sessionId);
	}

	// Cluster node placement state
	let nodes = $state<Node[]>([]);
	let loadingNodes = $state(true);
	let nodePlacementMode = $state<'auto' | 'manual'>('auto');
	let selectedNodeId = $state<string>('');
	let placementStrategy = $state<string>('least_memory');

	// Create from archive (OSS-428). The upload stays owned by the wizard; the
	// session only fences preview responses and holds the stable create id.
	let archiveMode = $state(false);
	let archiveUploadInput = $state<HTMLInputElement | null>(null);
	let archiveUploading = $state(false);
	let archiveUploadPercent = $state(0);
	let archiveImportState = $state<ArchiveImportState>({
		uploadId: '',
		fileName: '',
		preview: null,
		previewUploadId: '',
		previewNodeId: '',
		destinations: [],
		preflightUploadId: '',
		preparing: false,
		error: null,
		requestId: ''
	});

	interface ArchiveSubmission {
		uploadId: string;
		nodeId: string;
		requestId: string;
		preview: PreviewServerArchiveResponse;
	}
	interface ArchivePlacementSnapshot {
		nodePlacementMode: 'auto' | 'manual';
		selectedNodeId: string;
		placementStrategy: string;
	}
	// The in-flight submission is captured, so completion and refusal handling
	// never read a newer mode, upload or destination.
	let archiveSubmission = $state<ArchiveSubmission | null>(null);
	let priorPlacement = $state<ArchivePlacementSnapshot | null>(null);

	beforeNavigate(({ cancel }) => {
		if (archiveSubmission) cancel();
	});

	const archiveImport = createArchiveImport(
		{
			previewServerArchive: (request) =>
				rpcClient.server.previewServerArchive(request, ARCHIVE_RPC_OPTIONS)
		},
		(state) => (archiveImportState = state)
	);

	const archivePreviewView = $derived(
		archiveImportState.preview ? describePreview(archiveImportState.preview) : null
	);
	// Archive imports always use a concrete destination so the preview checks
	// that node's storage and create submits exactly the node that was verified.
	// Non-archive creates keep the automatic placement path.
	const archiveNodeId = $derived(
		archiveMode ? selectedNodeId : nodePlacementMode === 'manual' ? selectedNodeId : ''
	);
	const archivePreviewReady = $derived(isArchivePreviewCurrent(archiveImportState, archiveNodeId));
	const firstEnabledNode = $derived(nodes.find((node) => node.enabled) ?? null);
	// A successful preflight for the current upload is authoritative for archive
	// placement: it lists the nodes enabled right now, even when the cached
	// ListNodes inventory is stale, and it is the only source when nodes/read is
	// denied. Cached inventory may only seed the choice before an upload.
	const archivePreflightSettled = $derived(
		archiveMode &&
			Boolean(archiveImportState.uploadId) &&
			archiveImportState.preflightUploadId === archiveImportState.uploadId
	);
	const archivePreflightWaiting = $derived(
		archiveMode &&
			Boolean(archiveImportState.uploadId) &&
			!archivePreflightSettled &&
			!archiveImportState.error
	);
	const destinationChoices = $derived.by(() => {
		if (!archiveMode || !archiveImportState.uploadId) {
			return nodes.filter((node) => node.enabled).map((node) => ({ id: node.id, name: node.name }));
		}
		if (archivePreflightSettled) {
			return archiveImportState.destinations.map((destination) => ({
				id: destination.id,
				name: destination.name
			}));
		}
		return [];
	});
	const selectedNodeEnabled = $derived.by(() => {
		// Until the preflight settles, keep the preliminary cached choice so the
		// first preview can still validate it. A settled preflight is the truth.
		if (archiveMode && archiveImportState.uploadId && !archivePreflightSettled) {
			return Boolean(selectedNodeId);
		}
		if (archivePreflightSettled) {
			return archiveImportState.destinations.some(
				(destination) => destination.id === selectedNodeId
			);
		}
		return nodes.some((node) => node.id === selectedNodeId && node.enabled);
	});
	const archiveCreateBlockedReason = $derived.by(() => {
		if (!archiveMode) return '';
		if (!archiveImportState.uploadId) return 'Upload a server archive before creating.';
		if (archiveUploading) return 'Wait for the replacement archive upload to finish.';
		if (!archiveNodeId) {
			if (archivePreflightSettled && archiveImportState.destinations.length === 0)
				return 'Enable a Docker node in Settings → Docker Nodes, then retry the archive preview.';
			return 'Choose an enabled destination node so its storage can be verified.';
		}
		if (archiveImportState.preparing) return 'Wait for the archive preview to finish.';
		if (archiveImportState.error)
			return 'Retry the archive preview for this destination before creating.';
		if (!archivePreviewReady)
			return 'Preview this upload for the selected destination before creating.';
		if (archivePreviewView?.isBlocked)
			return 'Resolve the blocking archive findings before creating.';
		if (!archiveImportState.requestId)
			return 'Archive submission is not ready. Re-upload the archive and try again.';
		return '';
	});
	const archiveCreateReady = $derived(!archiveMode || archiveCreateBlockedReason === '');

	// Bind a concrete enabled node as soon as one is available. Before an
	// upload, cached inventory seeds the choice; afterwards only a settled
	// preflight may bind, and a now-ineligible selection is replaced so a stale
	// cached node cannot stay selected after it was disabled.
	$effect(() => {
		if (!archiveMode || archiveSubmission) return;
		if (!archiveImportState.uploadId) {
			if (selectedNodeEnabled) return;
			if (firstEnabledNode) selectedNodeId = firstEnabledNode.id;
			return;
		}
		if (!archivePreflightSettled) return;
		if (archiveImportState.destinations.some((destination) => destination.id === selectedNodeId))
			return;
		selectedNodeId = archiveImportState.destinations[0]?.id ?? '';
	});

	// Keep the visible engine identity aligned with the verified archive. An
	// archive cannot be combined with a different loader or Minecraft version.
	$effect(() => {
		const preview = archiveImportState.preview;
		if (!archiveMode || !preview) return;
		formData.mcVersion = preview.mcVersion;
		formData.modLoader = preview.modLoader;
		formData.loaderVersion = preview.loaderVersion;
	});

	// Preview again whenever the destination changes. A plain variable (not
	// $state) holds the last key so the effect does not retrigger itself.
	let lastArchivePreviewKey = '';
	$effect(() => {
		const uploadId = archiveImportState.uploadId;
		const nodeKey = archiveNodeId;
		if (!archiveMode || !uploadId) {
			lastArchivePreviewKey = '';
			return;
		}
		// Without a node this is an inspection-only preview (format, runtime and
		// blockers). It cannot qualify create; a concrete preview follows once a
		// destination is chosen.
		const key = `${uploadId}:${nodeKey}`;
		if (key === lastArchivePreviewKey) return;
		lastArchivePreviewKey = key;
		void archiveImport.refresh(nodeKey);
	});

	function retryArchivePreview() {
		if (!archiveImportState.uploadId) return;
		void archiveImport.retry(archiveNodeId);
	}

	// When no destination list is known, retrying the previously selected node
	// would just refuse again. Ask for the inspection result so newly enabled
	// nodes can be discovered, then the effect previews the concrete choice.
	function retryArchiveDestinations() {
		if (!archiveImportState.uploadId) return;
		void archiveImport.retry('');
	}

	/** Pick a concrete destination for an archive while keeping the choice editable. */
	function ensureArchiveDestination() {
		nodePlacementMode = 'manual';
		if (archivePreflightSettled) {
			if (archiveImportState.destinations.some((destination) => destination.id === selectedNodeId))
				return;
			selectedNodeId = archiveImportState.destinations[0]?.id ?? '';
			return;
		}
		if (selectedNodeEnabled) return;
		if (firstEnabledNode) selectedNodeId = firstEnabledNode.id;
	}

	/** Standalone add-ons are post-create work; an archive already carries its files. */
	function clearStandaloneAddons() {
		selectedMods = [];
		modSelectionTarget = '';
		selectedDatapacks = [];
		pendingDatapackUploads = [];
		indexedDatapackTarget = '';
		selectedModules = [];
		showModBrowser = false;
	}

	function chooseArchiveMode() {
		if (archiveSubmission || archiveMode) return;
		priorPlacement = {
			nodePlacementMode,
			selectedNodeId,
			placementStrategy
		};
		archiveMode = true;
		if (selectedModpack || loadingModpackVersions) removeModpack();
		// A catalog seed belongs to the former create path; its image cannot be
		// combined with a portable archive even when the preview succeeds.
		clearBlueprint();
		clearStandaloneAddons();
		ensureArchiveDestination();
	}

	function leaveArchiveMode() {
		if (archiveSubmission) return;
		if (!archiveMode) return;
		archiveMode = false;
		if (priorPlacement) {
			nodePlacementMode = priorPlacement.nodePlacementMode;
			selectedNodeId = priorPlacement.selectedNodeId;
			placementStrategy = priorPlacement.placementStrategy;
			priorPlacement = null;
		}
	}

	// Every upload gets a generation. A replacement upload may finish after a
	// newer one; only the newest generation may change the submission state.
	let archiveUploadSequence = 0;

	async function handleArchiveUpload(event: Event) {
		const input = event.target as HTMLInputElement;
		const file = input.files?.[0];
		input.value = '';
		if (archiveSubmission) return;
		if (!file) return;
		if (!isSupportedArchiveFilename(file.name)) {
			toast.error('Choose a .zip or .tar.gz server archive.');
			return;
		}
		const generation = ++archiveUploadSequence;
		archiveUploading = true;
		archiveUploadPercent = 0;
		try {
			const { sessionId } = await uploadFile(file, {
				onProgress: (progress) => {
					if (generation === archiveUploadSequence) {
						archiveUploadPercent = Math.round(progress.percentComplete);
					}
				}
			});
			if (generation !== archiveUploadSequence) return;
			// A failed or refused preview leaves this upload in place for retry.
			archiveImport.setUpload(sessionId, file.name);
		} catch (error) {
			if (generation !== archiveUploadSequence) return;
			toast.error(error instanceof Error ? error.message : 'Archive upload failed');
		} finally {
			if (generation === archiveUploadSequence) archiveUploading = false;
		}
	}

	function removeArchive() {
		if (archiveSubmission) return;
		// Fence a replacement upload that is still in flight before clearing.
		archiveUploadSequence++;
		archiveUploading = false;
		archiveUploadPercent = 0;
		archiveImport.discard();
	}

	function applyArchiveDefaults() {
		const preview = archiveImportState.preview;
		if (!preview) return;
		formData.mcVersion = preview.mcVersion;
		formData.modLoader = preview.modLoader;
		formData.loaderVersion = preview.loaderVersion;
		formData.memory = preview.memory;
		formData.maxPlayers = preview.maxPlayers;
		toast.success('Archive runtime defaults applied. Review them before creating.');
	}

	// Modpack selection
	let showModpackDialog = $state(false);
	let selectedModpack = $state<IndexedModpack | null>(null);
	let favoriteModpacks = $state<IndexedModpack[]>([]);
	let libraryModpacks = $state<IndexedModpack[]>([]);
	let libraryModpackQuery = $state('');
	let libraryActiveQuery = $state('');
	let libraryPage = $state(1);
	let libraryTotal = $state(0);
	const libraryPageSize = 30;
	let libraryModpackError = $state('');
	let loadingLibraryModpacks = $state(false);
	let showFavoriteModpacks = $state(false);
	let modpackVersions = $state<Version[]>([]);
	let selectedVersionId = $state<string>('');
	// Tracks an in-flight version fetch so the select can show a loading state
	// instead of briefly presenting an empty list.
	let loadingModpackVersions = $state(false);
	let changingPack = 0;
	let searchingPack = 0;

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
	const modpackReleaseMinecraft = $derived(
		new Map(
			modpackVersions.map((version) => [
				version.id,
				version.gameVersions
					.filter((gameVersion) => /^\d+\.\d+(\.\d+)?$/.test(gameVersion))
					.sort((left, right) => right.localeCompare(left, 'en', { numeric: true }))[0] || ''
			])
		)
	);
	const datapackGameVersion = $derived(
		selectedVersionId
			? modpackReleaseMinecraft.get(selectedVersionId) || ''
			: modpackReleaseMinecraft.get(selectedModpack?.latestFileId || '') || formData.mcVersion
	);
	const runtimeOverridden = $derived(
		Boolean(
			formData.dockerOverrides?.environment.TYPE ||
			formData.dockerOverrides?.environment.VERSION ||
			formData.dockerOverrides?.entrypoint.length ||
			formData.dockerOverrides?.command.length
		)
	);
	const canBrowseMods = $derived(
		!selectedModpack &&
			!runtimeOverridden &&
			supportsWizardMods(formData.modLoader) &&
			Boolean(formData.mcVersion && formData.mcVersion.toLowerCase() !== 'latest')
	);
	const modTarget = $derived(
		`${formData.modLoader}:${formData.mcVersion}:${selectedModpack?.id ?? ''}:${runtimeOverridden}`
	);
	const datapackTarget = $derived(
		`${selectedModpack?.id ?? ''}:${selectedVersionId}:${datapackGameVersion}`
	);

	$effect(() => {
		if (modSelectionTarget && modSelectionTarget !== modTarget) {
			selectedMods = [];
			modSelectionTarget = '';
			toast.info('Mod selections cleared because the runtime changed.');
		}
		if (indexedDatapackTarget && indexedDatapackTarget !== datapackTarget) {
			selectedDatapacks = [];
			indexedDatapackTarget = '';
			toast.info(
				'Catalog datapack selections cleared because the game version or modpack changed.'
			);
		}
	});

	function queueMods(mods: QueuedServerMod[]) {
		modSelectionTarget = modTarget;
		selectedMods = mods;
	}

	function moduleRestriction(template: ModuleTemplate): string {
		if (template.id === 'builtin-geyser' && runtimeOverridden) {
			return 'Requires the selected game engine’s default entrypoint and version settings.';
		}
		if (
			template.id === 'builtin-geyser' &&
			(selectedModpack ||
				![ModLoader.PAPER, ModLoader.SPIGOT, ModLoader.PURPUR].includes(formData.modLoader))
		) {
			return 'Requires Paper, Spigot, or Purpur for the Floodgate plugin.';
		}
		return '';
	}

	async function loadModules() {
		loadingModules = true;
		modulesError = '';
		try {
			const response = await rpcClient.module.listModuleTemplates({
				type: ModuleTemplateType.BUILTIN
			});
			moduleTemplates = response.templates.filter((template) => setupModuleIds.has(template.id));
			modulesLoaded = true;
		} catch (error) {
			modulesError = error instanceof Error ? error.message : 'Could not load module templates.';
		} finally {
			loadingModules = false;
		}
	}

	function selectAddonTab(tab: (typeof addonTabs)[number]) {
		addonTab = tab;
		if (tab === 'Modules' && !modulesLoaded && !loadingModules) void loadModules();
	}

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

	async function searchLibraryModpacks(pageNumber = 1, query = libraryModpackQuery.trim()) {
		const request = ++searchingPack;
		showFavoriteModpacks = false;
		loadingLibraryModpacks = true;
		libraryModpackError = '';
		try {
			const response = await rpcClient.modpack.searchModpacks({
				query,
				page: pageNumber,
				pageSize: libraryPageSize
			});
			if (request === searchingPack) {
				libraryModpacks = response.modpacks;
				libraryActiveQuery = query;
				libraryPage = pageNumber;
				libraryTotal = response.total;
			}
		} catch (error) {
			if (request === searchingPack) {
				libraryModpacks = [];
				libraryModpackError =
					error instanceof Error ? error.message : 'Could not load the Modpacks Library.';
			}
		} finally {
			if (request === searchingPack) loadingLibraryModpacks = false;
		}
	}

	function applyBlueprint(seed: ServerBlueprint) {
		if (archiveSubmission) return;
		const spec = resolveCreateBlueprint(seed);
		if (!spec.qualified) {
			return;
		}
		leaveArchiveMode();
		selectedBlueprintId = seed.id;
		if (selectedModpack || loadingModpackVersions) removeModpack();
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
		if (archiveSubmission) return;
		const request = ++changingPack;
		selectedModpack = modpack;
		selectedBlueprintId = '';
		leaveArchiveMode();
		showModpackDialog = false;
		loadingModpackVersions = true;
		modpackVersions = [];
		selectedVersionId = '';

		try {
			const [cfg, versions] = await Promise.allSettled([
				rpcClient.modpack.getModpackConfig({ id: modpack.id }),
				rpcClient.modpack.getModpackVersions({ id: modpack.id })
			]);
			if (request !== changingPack) return;
			if (cfg.status === 'rejected') throw cfg.reason;
			const config = cfg.value.config;
			formData.name = modpack.name || '';
			formData.description = modpack.summary || '';
			formData.modLoader =
				modpack.indexer === 'modrinth' ? ModLoader.MODRINTH : ModLoader.AUTO_CURSEFORGE;
			formData.mcVersion = config?.mc_version || modpack.mcVersion || '';
			formData.memory = Number(config?.memory) || modpack.recommendedRam || 4096;
			formData.dockerImage = config?.docker_image || modpack.dockerImage || '';
			if (versions.status === 'fulfilled') modpackVersions = versions.value.versions;
			if (versions.status === 'rejected')
				toast.info('Could not list modpack versions. The library default will be used.');
		} catch (error) {
			if (request !== changingPack) return;
			toast.error('Failed to load modpack configuration');
			console.error(error);
			removeModpack();
		} finally {
			if (request === changingPack) loadingModpackVersions = false;
		}
	}

	function selectModpackRelease(versionId: string) {
		selectedVersionId = versionId;
		// A library image recommendation belongs to the default release. Let the
		// runtime choose Java for an explicitly selected Minecraft version.
		formData.dockerImage = selectedVersionId ? '' : selectedModpack?.dockerImage || '';
	}

	function clearBlueprint() {
		selectedBlueprintId = '';
		formData.dockerImage = '';
	}

	function removeModpack() {
		changingPack++;
		loadingModpackVersions = false;
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
			const value: unknown = JSON.parse(jsonStr);
			return Array.isArray(value)
				? value.filter((entry): entry is string => typeof entry === 'string')
				: [];
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
		if (loadingModpackVersions) {
			toast.info('Wait for the modpack settings to load.');
			return false;
		}
		if (step === 0) {
			if (!formData.name.trim()) {
				toast.error('Server name is required');
				return false;
			}
			if (archiveMode && archiveUploading) {
				toast.info('Wait for the replacement archive upload to finish.');
				return false;
			}
			if (archiveMode && !archiveImportState.uploadId) {
				toast.error('Upload a server archive to continue.');
				return false;
			}
		}
		if (step === 1 && !(datapackGameVersion || formData.mcVersion).trim()) {
			toast.error('Choose a Minecraft version.');
			return false;
		}
		if (step === 1 && selectedVersionId && !datapackGameVersion) {
			toast.error('Choose a modpack release with a known Minecraft version.');
			return false;
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
		if (loading || createdServer || archiveSubmission) return;
		if (stepIndex > currentStep) {
			for (let i = currentStep; i < stepIndex; i++) {
				if (!canProceed(i)) return;
			}
		}
		currentStep = stepIndex;
	}

	function nextStep() {
		if (loading || archiveSubmission) return;
		if (canProceed(currentStep)) {
			if (currentStep < steps.length - 1) {
				currentStep++;
			}
		}
	}

	function prevStep() {
		if (loading || archiveSubmission) return;
		if (currentStep > 0) {
			currentStep--;
		}
	}

	function buildSetupTasks(): SetupTask[] {
		// Archive content is restored from its manifest; standalone setup add-ons
		// are refused so a former selection cannot be applied to the import.
		if (archiveMode) return [];
		const tasks: SetupTask[] = [];
		const add = (name: string, run: SetupTask['run']) =>
			tasks.push({ name, run, status: 'pending' });
		for (const file of mergeWizardModFiles(selectedMods.flatMap((mod) => mod.files))) {
			add(file.fileName, async (serverId) => {
				const { name, fileName, downloadUrl } = file;
				const response = await rpcClient.modBrowse.installMods({
					serverId,
					items: [{ name, fileName, downloadUrl }]
				});
				if (!response.installed.includes(fileName) || response.errors.length > 0) {
					throw new Error(
						response.errors.join('; ') || response.message || 'The download did not finish.'
					);
				}
			});
		}
		const gameVersion = datapackGameVersion;
		for (const dp of selectedDatapacks) {
			add(dp.name, async (serverId) => {
				await rpcClient.datapack.installDatapack({
					serverId,
					datapackId: dp.id,
					gameVersion,
					indexer: dp.indexer
				});
			});
		}
		for (const dp of pendingDatapackUploads) {
			add(dp.fileName, async (serverId, retry) => {
				let uploadSessionId = dp.sessionId;
				if (retry) {
					const existing = await rpcClient.datapack.listDatapacks({ serverId });
					if (existing.datapacks.some((pack) => pack.fileName === dp.fileName)) return;
					({ sessionId: uploadSessionId } = await uploadFile(dp.file));
				}
				await rpcClient.datapack.uploadDatapack({
					serverId,
					uploadSessionId,
					fileName: dp.fileName
				});
			});
		}
		for (const template of selectedModules) {
			let moduleId = '';
			add(template.name, async (serverId, retry) => {
				if (retry && !moduleId) {
					const existing = await rpcClient.module.listModules({
						serverId,
						templateId: template.id
					});
					const matching = existing.modules.filter(
						(module) => module.serverId === serverId && module.templateId === template.id
					);
					if (matching.length > 1)
						throw new Error(
							'Several modules match this template. Open Modules to choose the intended one.'
						);
					moduleId = matching[0]?.id || '';
				}
				if (!moduleId) {
					const response = await rpcClient.module.createModule({
						serverId,
						templateId: template.id,
						name: template.name,
						envOverrides: template.defaultEnv,
						volumeOverrides: template.defaultVolumes,
						eventHooks: template.defaultHooks,
						autoStart: false,
						startImmediately: false,
						followServerLifecycle: true
					});
					moduleId = response.module?.id || '';
					if (!moduleId) throw new Error('The panel returned no module.');
					retry = false;
				}
				await waitForSetupModule(moduleId, retry);
			});
		}
		return tasks;
	}

	async function waitForSetupModule(id: string, retry: boolean) {
		const deadline = Date.now() + 45_000;
		while (Date.now() < deadline) {
			const { module } = await rpcClient.module.getModule(
				{ id },
				{ ...silentCallOptions, timeoutMs: 5_000 }
			);
			if (!module) throw new Error('The panel returned no module.');
			if (module.status === ModuleStatus.STOPPED && module.containerId) return;
			if (module.status === ModuleStatus.RUNNING || module.status === ModuleStatus.STARTING) {
				throw new Error('This module is already active. Open Modules to check its status.');
			}
			if (module.status === ModuleStatus.ERROR) {
				if (!retry || module.containerId)
					throw new Error(
						'The module container failed to initialize. Check the module from the server page.'
					);
				retry = false;
				await rpcClient.module.recreateModule({ id }, { ...silentCallOptions, timeoutMs: 40_000 });
				continue;
			}
			await new Promise((resolve) => setTimeout(resolve, 1_000));
		}
		throw new Error('The module container is still being prepared. Retry when it is ready.');
	}

	async function waitForPreparedServer(server: Server, starting = false) {
		const deadline = Date.now() + 45_000;
		while (Date.now() < deadline) {
			const { server: current } = await rpcClient.server.getServer(
				{ id: server.id },
				silentCallOptions
			);
			if (current?.status === ServerStatus.ERROR && starting && startRequestId) return current;
			if (!current || current.status === ServerStatus.ERROR)
				throw new Error(
					'The server container could not be created. Open the server to check its status.'
				);
			if (current.status === ServerStatus.RUNNING || current.status === ServerStatus.STARTING) {
				if (starting) return current;
				throw new Error('The server is running. Stop it before applying the remaining add-ons.');
			}
			if (current.status === ServerStatus.STOPPED && current.containerId) return current;
			await new Promise((resolve) => setTimeout(resolve, 1_000));
		}
		throw new Error('The container is still being prepared. Retry when it is ready.');
	}

	async function startPreparedServer(server: Server) {
		if (!deferredStart) return;
		const current = await waitForPreparedServer(server, true);
		if (current.status === ServerStatus.RUNNING || current.status === ServerStatus.STARTING) return;
		let permittedErrorRetry = false;
		if (startRequestId) {
			// A transport failure does not tell us whether Docker was called. Same
			// key is safe until a durable receipt says this attempt can be retried.
			const journal = await rpcClient.operation
				.listOperations({ serverId: server.id }, silentCallOptions)
				.catch(() => null);
			const matching = journal?.operations.find(
				(entry) => entry.requestId === startRequestId && entry.kind === 'start'
			);
			if (matching?.outcome === 'success' && current.status !== ServerStatus.ERROR) return;
			if (matching?.nextAction === 'retry_with_new_request_id') {
				startRequestId = '';
				permittedErrorRetry = true;
			} else if (matching?.holdsLock)
				throw new Error(matching.reason || 'The previous start needs to finish before retrying.');
		}
		if (current.status === ServerStatus.ERROR && !permittedErrorRetry) {
			throw new Error(
				'The server reports an error and the previous start has no retryable receipt. Open the server to check its status.'
			);
		}
		if (!startRequestId) {
			const attempt = beginLifecycleRequest(server.id, 'start');
			startRequestId = attempt.requestId;
			attempt.finish();
		}
		try {
			await rpcClient.server.startServer(
				{ id: server.id, requestId: startRequestId },
				silentCallOptions
			);
		} catch (error) {
			if (
				error instanceof ConnectError &&
				[
					Code.InvalidArgument,
					Code.PermissionDenied,
					Code.ResourceExhausted,
					Code.FailedPrecondition
				].includes(error.code)
			) {
				startRequestId = '';
			}
			throw error;
		}
	}

	async function applySetup() {
		const server = createdServer;
		if (!server) return;
		if (setupTasks.some((task) => task.status !== 'done')) {
			setupError = '';
			preparingContainer = true;
			try {
				await waitForPreparedServer(server);
			} catch (error) {
				setupError =
					error instanceof Error ? error.message : 'The container is still being prepared.';
				return;
			} finally {
				preparingContainer = false;
			}
		}
		for (const task of setupTasks) {
			if (task.status === 'done') continue;
			const retry = task.status === 'failed';
			task.status = 'running';
			try {
				await task.run(server.id, retry);
				task.status = 'done';
			} catch (error) {
				task.status = 'failed';
				task.error = error instanceof Error ? error.message : 'Setup request failed.';
			}
		}
		if (setupTasks.some((task) => task.status === 'failed')) return;
		startError = '';
		try {
			await startPreparedServer(server);
			deferredStart = false;
		} catch (error) {
			startError = error instanceof Error ? error.message : 'Could not start the server.';
		}
	}

	async function retrySetup() {
		if (loading) return;
		loading = true;
		try {
			await applySetup();
		} finally {
			loading = false;
		}
	}

	async function handleSubmit(e?: Event) {
		if (e) e.preventDefault();
		if (
			loading ||
			createdServer ||
			uploadingWizardDatapack ||
			archiveUploading ||
			loadingModpackVersions ||
			currentStep !== 4
		)
			return;

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
		if (!canProceed(1)) {
			currentStep = 1;
			return;
		}
		const unavailableModule = selectedModules.find((template) => moduleRestriction(template));
		if (unavailableModule) {
			selectAddonTab('Modules');
			toast.error(
				`${unavailableModule.name}: ${moduleRestriction(unavailableModule)} Remove it before creating the server.`
			);
			return;
		}
		if (selectedMods.length > 0 && (!canBrowseMods || modSelectionTarget !== modTarget)) {
			selectAddonTab('Mods');
			toast.error('Select compatible mods for the current runtime.');
			return;
		}

		if (archiveMode) {
			if (!archiveImportState.uploadId) {
				toast.error('Upload a server archive before creating.');
				currentStep = 0;
				return;
			}
			if (archiveUploading) {
				toast.error('Wait for the replacement archive upload to finish before creating.');
				currentStep = 0;
				return;
			}
			if (nodePlacementMode !== 'manual' || !selectedNodeId) {
				toast.error(
					'Choose a destination node for the archive so its storage is verified before creating.'
				);
				currentStep = 3;
				return;
			}
			if (archiveImportState.preparing) {
				toast.error('Wait for the archive preview to finish before creating.');
				currentStep = 0;
				return;
			}
			if (!archivePreviewReady) {
				toast.error(
					'Wait for a successful archive preview of this upload and destination before creating.'
				);
				currentStep = 0;
				return;
			}
			if (archivePreviewView?.isBlocked) {
				toast.error('This archive has blocking findings. Resolve them before creating.');
				currentStep = 0;
				return;
			}
			if (!archiveImportState.requestId) {
				toast.error('Archive submission is not ready. Re-upload the archive and try again.');
				currentStep = 0;
				return;
			}
		}

		const submission: ArchiveSubmission | null =
			archiveMode && archiveImportState.preview
				? {
						uploadId: archiveImportState.uploadId,
						nodeId: selectedNodeId,
						requestId: archiveImportState.requestId,
						preview: archiveImportState.preview
					}
				: null;
		if (archiveMode && !submission) {
			toast.error('Archive submission is not ready. Preview the upload again.');
			return;
		}
		archiveSubmission = submission;
		loading = true;
		// Archive submissions keep one request id across retries; a refusal does
		// not discard the completed upload. Ordinary creates use the short-lived
		// in-flight id as before.
		const attempt = submission
			? null
			: beginLifecycleRequest(formData.name.trim() || 'new-server', 'create');
		try {
			const tasks = buildSetupTasks();
			const hasAddons = tasks.length > 0;
			deferredStart = formData.startImmediately && hasAddons;
			const archivePreview = submission?.preview ?? null;
			const createRequest = create(CreateServerRequestSchema, {
				...formData,
				// The controller requires the verified archive runtime. Operator
				// memory and max-player values in formData remain their overrides.
				mcVersion: archivePreview
					? archivePreview.mcVersion
					: datapackGameVersion || formData.mcVersion,
				modLoader: archivePreview ? archivePreview.modLoader : formData.modLoader,
				loaderVersion: archivePreview ? archivePreview.loaderVersion : formData.loaderVersion,
				memory: formData.memory,
				maxPlayers: formData.maxPlayers,
				startImmediately: formData.startImmediately && !deferredStart,
				// Archive create binds exactly the node that the preview checked;
				// it never lets the controller resolve a different one.
				nodeId: submission
					? submission.nodeId
					: nodePlacementMode === 'manual'
						? selectedNodeId
						: '',
				placementStrategy: submission ? '' : nodePlacementMode === 'auto' ? placementStrategy : '',
				modpackId: submission ? '' : selectedModpack?.id || '',
				modpackVersionId: submission ? '' : selectedVersionId || '',
				port: useProxyMode ? 0 : formData.port,
				proxyHostname: useProxyMode && proxyEnabled ? formData.proxyHostname : '',
				proxyListenerId: useProxyMode && proxyEnabled ? formData.proxyListenerId : '',
				// An archive is provisioned by its manifest; a former blueprint,
				// manual image, detached mode or Docker override must not leak into
				// the import request even when the operator changed modes earlier.
				dockerImage: submission ? '' : formData.dockerImage,
				detached: submission ? false : formData.detached,
				dockerOverrides: submission ? undefined : formData.dockerOverrides,
				archiveUploadId: submission ? submission.uploadId : ''
			});
			if (submission) {
				// The archive session allocates one id per completed upload and keeps
				// it across retries; a refusal retains the upload for correction.
				createRequest.requestId = submission.requestId;
			} else {
				const fingerprint = toJsonString(CreateServerRequestSchema, createRequest);
				if (pendingCreate?.fingerprint !== fingerprint)
					pendingCreate = { fingerprint, requestId: attempt!.requestId };
				createRequest.requestId = pendingCreate.requestId;
			}

			const response = await rpcClient.server.createServer(
				createRequest,
				submission ? ARCHIVE_RPC_OPTIONS : undefined
			);
			if (!response.server?.id) throw new Error('The panel returned no server.');
			// Only the captured upload may release its submission identity; a
			// later session for a replacement archive keeps its own attempt.
			if (submission && archiveImportState.uploadId === submission.uploadId)
				archiveImport.succeeded();
			// The committed server may navigate immediately; the in-flight fence
			// applies only to the request, not to the post-create transition.
			archiveSubmission = null;
			createdServer = response.server;
			setupTasks = tasks;
			if (hasAddons) await applySetup();
			else void goto(resolve('/servers/[id]', { id: response.server.id }));
		} catch (error) {
			toast.error(
				submission
					? `Create from archive failed: ${error instanceof Error ? error.message : 'Unknown error'}. The uploaded archive was kept; you can retry with the same submission.`
					: `Failed to create server: ${error instanceof Error ? error.message : 'Unknown error'}`
			);
		} finally {
			attempt?.finish();
			archiveSubmission = null;
			loading = false;
		}
	}
</script>

<div class="bg-background font-sans text-foreground">
	<div class="space-y-6">
		<CarbonPageHeader
			title={createdServer ? createdServer.name : 'Create New Server'}
			description={createdServer
				? 'Server setup results'
				: 'Choose a Minecraft runtime, set its resources, and add optional content.'}
		>
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

		{#if createdServer}
			<section
				aria-label="Server setup results"
				class="space-y-4 border border-border bg-card p-5"
				aria-busy={loading}
			>
				<h2 class="text-base font-semibold">Server created</h2>
				{#if setupTasks.length > 0}
					<ul class="divide-y divide-border border-y border-border" aria-live="polite">
						{#each setupTasks as task (task)}
							<li
								class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 py-3 text-sm"
							>
								<span>{task.name}</span>
								<span
									class={task.status === 'failed'
										? 'text-destructive-text'
										: 'text-muted-foreground'}
								>
									{task.status === 'done'
										? 'Added'
										: task.status === 'running'
											? 'Adding…'
											: task.status === 'failed'
												? 'Failed'
												: 'Queued'}
								</span>
								{#if task.status === 'failed'}
									<p class="w-full text-xs break-words text-destructive-text">{task.error}</p>
								{/if}
							</li>
						{/each}
					</ul>
				{/if}
				{#if preparingContainer}
					<p role="status" class="text-sm text-muted-foreground">
						Waiting for the server container before adding content…
					</p>
				{:else if setupError}
					<p role="status" class="text-sm text-destructive-text">{setupError}</p>
				{:else if startError}
					<p role="status" class="text-sm text-destructive-text">{startError}</p>
				{:else if deferredStart && loading && setupTasks.every((task) => task.status === 'done')}
					<p role="status" class="text-sm text-muted-foreground">
						Waiting for the container before starting the server…
					</p>
				{:else if deferredStart && !loading}
					<p class="text-sm text-muted-foreground">
						Start is on hold while an add-on needs attention.
					</p>
				{/if}
				<div class="flex flex-wrap gap-2">
					{#if !loading}
						<CarbonButton href={resolve('/servers/[id]', { id: createdServer.id })}
							>Open server</CarbonButton
						>
						{#if setupTasks.some((task) => task.status === 'failed') || setupError || startError}
							<CarbonButton kind="secondary" onclick={retrySetup}>
								{startError ? 'Retry start' : setupError ? 'Retry setup' : 'Retry failed add-ons'}
							</CarbonButton>
						{/if}
					{/if}
				</div>
			</section>
		{/if}
		<!-- Carbon Progress Indicator / Steps (Requirement 3) -->
		<div
			hidden={Boolean(createdServer)}
			class="w-full rounded-none border border-border bg-card p-4"
		>
			<div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-5">
				{#each steps as step (step.id)}
					<button
						type="button"
						disabled={loading}
						aria-current={currentStep === step.id ? 'step' : undefined}
						onclick={() => goToStep(step.id)}
						class="focus-ring relative flex cursor-pointer items-center gap-3 rounded-none border p-3 text-left transition-colors select-none {currentStep ===
						step.id
							? 'border-primary bg-accent shadow-[inset_3px_0_0_var(--primary)]'
							: currentStep > step.id
								? 'border-border bg-background hover:bg-card'
								: 'border-border bg-background opacity-70 hover:opacity-100'}"
					>
						<!-- Indicator Badge -->
						<div
							class="flex h-7 w-7 shrink-0 items-center justify-center rounded-none font-mono text-xs font-bold {currentStep ===
							step.id
								? 'bg-primary text-primary-foreground'
								: currentStep > step.id
									? 'bg-success text-primary-foreground'
									: 'bg-secondary text-muted-foreground'}"
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
									? 'text-foreground'
									: currentStep > step.id
										? 'text-muted-foreground'
										: 'text-muted-foreground'}"
							>
								{step.title}
							</p>
							<p class="mt-0.5 font-sans text-xs break-words text-muted-foreground">
								{step.subtitle}
							</p>
						</div>
					</button>
				{/each}
			</div>
		</div>

		<!-- Wizard Form Container -->
		<form hidden={Boolean(createdServer)} inert={loading} onsubmit={handleSubmit} class="space-y-6">
			<!-- Step 0: Basic Information & Preset -->
			{#if currentStep === 0}
				<div class="space-y-6 rounded-none border border-border bg-card p-6">
					<div>
						<h2 class="text-lg font-light text-foreground">Step 1: Basic Information & Preset</h2>
						<p class="mt-1 font-sans text-xs text-muted-foreground">
							Provide server identification and select whether to build manual config or load from a
							curated modpack.
						</p>
					</div>

					<!-- Preset Selection (Carbon Tiles) -->
					<div class="space-y-2">
						<span
							id="config-method-label"
							class="block font-mono text-xs font-semibold tracking-wider text-muted-foreground uppercase"
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
								aria-checked={!archiveMode && selectedModpack === null}
								disabled={Boolean(archiveSubmission)}
								onclick={() => {
									leaveArchiveMode();
									if (selectedModpack || loadingModpackVersions) removeModpack();
								}}
								class="focus-ring relative cursor-pointer rounded-none border p-4 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-50 {!archiveMode &&
								selectedModpack === null
									? 'border-primary bg-accent shadow-[inset_3px_0_0_var(--primary)]'
									: 'border-border bg-background hover:bg-card'}"
							>
								<div class="flex items-center gap-2">
									<Settings class="h-4 w-4 text-accent-text" />
									<h4 class="text-sm font-semibold text-foreground">Manual Configuration</h4>
								</div>
								<p class="mt-1 text-xs text-muted-foreground">
									Start with clean Minecraft server settings and custom modloaders.
								</p>
							</button>

							<!-- Modpack Tile -->
							<button
								type="button"
								role="radio"
								aria-checked={!archiveMode && selectedModpack !== null}
								disabled={Boolean(archiveSubmission)}
								onclick={() => {
									leaveArchiveMode();
									showModpackDialog = true;
									void searchLibraryModpacks();
								}}
								class="focus-ring relative cursor-pointer rounded-none border p-4 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-50 {!archiveMode &&
								selectedModpack !== null
									? 'border-primary bg-accent shadow-[inset_3px_0_0_var(--primary)]'
									: 'border-border bg-background hover:bg-card'}"
							>
								<div class="flex items-center gap-2">
									<Package class="h-4 w-4 text-accent-text" />
									<h4 class="text-sm font-semibold text-foreground">
										{selectedModpack ? selectedModpack.name : 'From Modpack Preset'}
									</h4>
								</div>
								<p class="mt-1 text-xs text-muted-foreground">
									{selectedModpack
										? `${selectedModpack.indexer} • MC ${selectedModpack.mcVersion}`
										: 'Browse the Modpacks Library or choose a favorite'}
								</p>
							</button>

							<!-- Archive Tile (OSS-428) -->
							<button
								type="button"
								role="radio"
								aria-checked={archiveMode}
								disabled={Boolean(archiveSubmission)}
								onclick={chooseArchiveMode}
								class="focus-ring relative cursor-pointer rounded-none border p-4 text-left disabled:cursor-not-allowed disabled:opacity-50 {archiveMode
									? 'border-primary bg-accent shadow-[inset_3px_0_0_var(--primary)]'
									: 'border-border bg-background transition-colors hover:bg-card'}"
								data-testid="create-from-archive-tile"
							>
								<div class="flex items-center gap-2">
									<Archive class="h-4 w-4 text-accent-text" />
									<h4 class="text-sm font-semibold text-foreground">From Server Archive</h4>
								</div>
								<p
									class="mt-1 text-xs {archiveMode
										? 'text-accent-foreground'
										: 'text-muted-foreground'}"
								>
									Create a new server from a portable ZIP or tar.gz export.
								</p>
							</button>
						</div>

						{#if archiveMode}
							<div class="mt-3 space-y-4 rounded-none border border-primary bg-background p-4">
								<div class="flex flex-wrap items-start justify-between gap-3">
									<div>
										<h4 class="text-sm font-semibold text-foreground">
											Create from server archive
										</h4>
										<p class="mt-1 font-sans text-xs text-muted-foreground">
											Upload a ZIP or tar.gz export. Preview validates every checksum and reports
											runtime compatibility and destination capacity. A new name, node and free port
											are required. No running server is stopped by this flow.
										</p>
									</div>
									{#if archiveImportState.uploadId}
										<button
											type="button"
											onclick={removeArchive}
											disabled={Boolean(archiveSubmission)}
											class="shrink-0 cursor-pointer rounded-none bg-secondary px-3 py-1.5 font-mono text-xs text-foreground uppercase transition-colors hover:bg-destructive hover:text-primary-foreground disabled:cursor-not-allowed disabled:opacity-50"
										>
											Remove archive
										</button>
									{/if}
								</div>

								<div class="flex flex-wrap items-center gap-3">
									<input
										bind:this={archiveUploadInput}
										type="file"
										accept=".zip,.tar.gz,.tgz,application/zip,application/gzip"
										class="hidden"
										onchange={handleArchiveUpload}
										disabled={Boolean(archiveSubmission)}
										data-testid="archive-file-input"
									/>
									<CarbonButton
										kind="secondary"
										size="sm"
										disabled={archiveUploading || Boolean(archiveSubmission)}
										onclick={() => archiveUploadInput?.click()}
									>
										{#if archiveUploading}
											<Loader2 class="h-4 w-4 animate-spin" />
											Uploading… {archiveUploadPercent}%
										{:else}
											<Upload class="h-4 w-4" />
											{archiveImportState.uploadId ? 'Replace archive' : 'Upload archive'}
										{/if}
									</CarbonButton>
									{#if archiveImportState.fileName}
										<span class="min-w-0 truncate font-mono text-xs text-muted-foreground">
											{archiveImportState.fileName}
										</span>
									{/if}
								</div>

								{#if archiveImportState.preparing}
									<div
										class="flex items-center gap-2 border border-border bg-card p-3"
										role="status"
									>
										<CarbonInlineLoading
											description="Preparing archive preview — validating every file can take a few minutes."
										/>
									</div>
								{:else if archiveImportState.error}
									<div
										class="flex flex-wrap items-center justify-between gap-3 border-l-4 border-destructive bg-destructive/10 p-3"
										data-testid="archive-preview-error"
									>
										<p role="alert" class="text-xs text-destructive-text">
											{archiveImportState.error}
										</p>
										{#if archiveImportState.uploadId}
											<CarbonButton
												kind="secondary"
												size="sm"
												disabled={archiveUploading || Boolean(archiveSubmission)}
												onclick={retryArchivePreview}
											>
												Retry preview
											</CarbonButton>
										{/if}
									</div>
								{:else if archivePreviewView}
									<ArchivePreviewPanel
										view={archivePreviewView}
										nodeId={archiveNodeId}
										port={useProxyMode ? 0 : formData.port}
									/>
									<div class="flex flex-wrap items-center gap-3">
										<button
											type="button"
											onclick={applyArchiveDefaults}
											disabled={Boolean(archiveSubmission)}
											class="focus-ring cursor-pointer rounded-none border border-border bg-card px-3 py-1.5 font-mono text-xs text-accent-text uppercase transition-colors hover:bg-accent disabled:cursor-not-allowed disabled:opacity-50"
										>
											Apply runtime defaults
										</button>
										<span class="font-sans text-xs text-muted-foreground">
											Applied defaults remain editable; the controller revalidates the archive on
											create.
										</span>
									</div>
								{:else if archiveImportState.uploadId}
									<p class="font-sans text-xs text-muted-foreground">Preparing preview…</p>
								{/if}
							</div>
						{/if}

						{#if selectedModpack}
							<div
								class="mt-3 flex items-start justify-between gap-4 rounded-none border border-primary bg-background p-4"
							>
								<div class="flex min-w-0 items-start gap-3">
									{#if selectedModpack.logoUrl}
										<img
											src={selectedModpack.logoUrl}
											alt={selectedModpack.name}
											class="h-12 w-12 shrink-0 rounded-none border border-border object-cover"
										/>
									{/if}
									<div class="min-w-0">
										<div class="flex items-center gap-2">
											<h4 class="text-sm font-semibold text-foreground">{selectedModpack.name}</h4>
											<CarbonTag type="blue" size="sm">{selectedModpack.indexer}</CarbonTag>
										</div>
										<p class="mt-1 line-clamp-2 font-sans text-xs text-muted-foreground">
											{selectedModpack.summary}
										</p>
										{#if modpackVersions.length > 0 || loadingModpackVersions}
											<div class="mt-2 flex items-center gap-2">
												<label for="preset-version" class="font-mono text-xs text-muted-foreground"
													>Version</label
												>
												<select
													id="preset-version"
													value={selectedVersionId}
													onchange={(event) => selectModpackRelease(event.currentTarget.value)}
													disabled={loadingModpackVersions}
													class="h-8 rounded-none border border-border bg-card px-2 font-mono text-xs text-foreground focus:border-primary focus:outline-none disabled:opacity-50"
												>
													<option value="">
														{loadingModpackVersions ? 'Loading versions…' : 'Library default'}
													</option>
													{#each modpackVersions as version (version.id)}
														<option
															value={version.id}
															disabled={!modpackReleaseMinecraft.get(version.id)}
														>
															{version.displayName}
															{version.releaseType ? `(${version.releaseType})` : ''}
															· {modpackReleaseMinecraft.get(version.id) ||
																'Minecraft version unknown'}
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
									class="shrink-0 cursor-pointer rounded-none bg-secondary px-3 py-1.5 font-mono text-xs text-foreground uppercase transition-colors hover:bg-destructive hover:text-primary-foreground"
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
							class="block font-mono text-xs font-semibold tracking-wider text-muted-foreground uppercase"
						>
							Description <span class="text-xs font-normal text-muted-foreground lowercase"
								>(optional)</span
							>
						</label>
						<textarea
							id="description"
							placeholder="Primary survival server instance for cluster..."
							bind:value={formData.description}
							rows={3}
							class="focus-ring w-full resize-none rounded-none border border-border bg-background p-3 text-sm text-foreground transition-colors placeholder:text-muted-foreground focus:bg-accent"
						></textarea>
					</div>

					{#if catalogBlueprints.length > 0}
						<div class="space-y-2">
							<div class="flex items-center justify-between gap-2">
								<p
									class="font-mono text-xs font-semibold tracking-wider text-muted-foreground uppercase"
								>
									Catalog seed
								</p>
								{#if selectedBlueprintId}
									<button
										type="button"
										onclick={clearBlueprint}
										class="font-mono text-xs text-accent-text uppercase hover:underline"
									>
										Clear seed
									</button>
								{/if}
							</div>
							<p class="font-sans text-xs text-muted-foreground">
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
											? 'border-primary bg-background'
											: 'border-border bg-background'} {spec.qualified
											? 'hover:border-accent-text'
											: 'cursor-not-allowed opacity-80'}"
									>
										<div class="flex items-center justify-between gap-2">
											<span class="text-sm font-semibold text-foreground">{seed.name}</span>
											{#if spec.qualified}
												<CarbonTag type="green" size="sm">Java</CarbonTag>
											{:else}
												<CarbonTag type="yellow" size="sm">Unsupported</CarbonTag>
											{/if}
										</div>
										<p class="mt-1 font-sans text-xs text-muted-foreground">{seed.description}</p>
										{#if !spec.qualified}
											<p class="mt-2 font-sans text-xs text-warning-text">{spec.limitation}</p>
										{/if}
									</button>
								{/each}
							</div>
						</div>
					{/if}
				</div>

				<!-- Step 1: Game Engine & Version -->
			{:else if currentStep === 1}
				<div class="space-y-6 rounded-none border border-border bg-card p-6">
					<div>
						<h2 class="text-lg font-light text-foreground">Step 2: Engine & Version</h2>
						<p class="mt-1 font-sans text-xs text-muted-foreground">
							Select the Minecraft server release and runtime loader.
						</p>
					</div>

					<!-- Minecraft Version (Carbon Select) -->
					<div class="space-y-1.5">
						<label
							for="mcVersion"
							class="block font-mono text-xs font-semibold tracking-wider text-muted-foreground uppercase"
						>
							Minecraft Version
						</label>
						{#if archiveMode}
							<input
								id="mcVersion"
								readonly
								data-testid="archive-runtime-mc"
								value={archiveImportState.preview?.mcVersion || 'Verified from the archive preview'}
								class="h-10 w-full border border-dashed border-border bg-background px-4 text-sm text-muted-foreground"
							/>
							<p class="font-sans text-xs text-muted-foreground">
								Locked to the archive runtime. Re-upload a different archive to change it.
							</p>
						{:else if selectedModpack}
							<input
								id="mcVersion"
								readonly
								value={datapackGameVersion || 'Set by the selected modpack release'}
								class="h-10 w-full border border-border bg-background px-4 text-sm text-muted-foreground"
							/>
						{:else if loadingVersions}
							<div class="flex items-center gap-2 border border-border bg-background p-3">
								<CarbonInlineLoading description="Loading supported versions catalog..." />
							</div>
						{:else}
							<div class="relative">
								<select
									id="mcVersion"
									bind:value={formData.mcVersion}
									disabled={loading}
									class="focus-ring h-10 w-full cursor-pointer appearance-none rounded-none border border-border bg-background px-4 pr-10 text-sm text-foreground transition-colors focus:bg-accent"
								>
									{#each minecraftVersions as version (version)}
										<option value={version}>
											{version}
											{version === latestVersion ? '(Latest Official)' : ''}
										</option>
									{/each}
								</select>
								<div
									class="pointer-events-none absolute inset-y-0 right-0 flex items-center px-3 text-muted-foreground"
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
							class="block font-mono text-xs font-semibold tracking-wider text-muted-foreground uppercase"
						>
							Mod Loader / Server Software
						</label>
						{#if archiveMode}
							<input
								id="modLoader"
								readonly
								data-testid="archive-runtime-loader"
								value={archivePreviewView?.modLoader || 'Verified from the archive preview'}
								class="h-10 w-full border border-dashed border-border bg-background px-4 text-sm text-muted-foreground"
							/>
						{:else}
							<div class="relative">
								<select
									id="modLoader"
									value={formData.modLoader.toString()}
									onchange={(e) => {
										const val = Number(e.currentTarget.value);
										formData.modLoader = val as ModLoader;
									}}
									disabled={loading || !!selectedModpack}
									class="focus-ring h-10 w-full cursor-pointer appearance-none rounded-none border border-border bg-background px-4 pr-10 text-sm text-foreground transition-colors focus:bg-accent disabled:opacity-50"
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
									class="pointer-events-none absolute inset-y-0 right-0 flex items-center px-3 text-muted-foreground"
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

						{#if archiveMode}
							<p class="mt-1 font-sans text-xs text-muted-foreground">
								Archive provisioning uses the verified runtime and cannot mix in a different loader.
							</p>
						{:else if selectedModpack}
							<p class="mt-1 font-mono text-xs text-accent-text">
								Mod loader auto-determined by preset configuration.
							</p>
						{:else if formData.modLoader === ModLoader.VANILLA}
							<p class="mt-1 font-sans text-xs text-muted-foreground">
								Vanilla Minecraft server binary with no mod dependencies.
							</p>
						{:else}
							<p class="mt-1 font-sans text-xs text-success-text">
								Supports runtime mod jar injection and server extensions.
							</p>
						{/if}
					</div>

					{#if archiveMode}
						<div class="space-y-1.5">
							<label
								for="loaderVersion"
								class="block font-mono text-xs font-semibold tracking-wider text-muted-foreground uppercase"
							>
								Loader Version
							</label>
							<input
								id="loaderVersion"
								readonly
								data-testid="archive-runtime-loader-version"
								value={archiveImportState.preview?.loaderVersion || 'default'}
								class="h-10 w-full border border-dashed border-border bg-background px-4 font-mono text-sm text-muted-foreground"
							/>
							<p class="mt-1 font-sans text-xs text-muted-foreground">
								Verified from the archive; create submits this loader version.
							</p>
						</div>
					{:else if loaderTakesVersion && !selectedModpack}
						<!-- Loader Version (Carbon Select) -->
						<div class="space-y-1.5">
							<label
								for="loaderVersion"
								class="block font-mono text-xs font-semibold tracking-wider text-muted-foreground uppercase"
							>
								{formData.modLoader === ModLoader.NEOFORGE ? 'NeoForge' : 'Loader'} Version
							</label>
							<div class="relative">
								<select
									id="loaderVersion"
									value={formData.loaderVersion}
									onchange={(e) => (formData.loaderVersion = e.currentTarget.value)}
									disabled={loading || loadingLoaderVersions || !formData.mcVersion}
									class="h-10 w-full cursor-pointer appearance-none rounded-none border-b border-input bg-background px-4 pr-10 text-sm text-foreground focus:border-b-2 focus:border-primary focus:bg-card focus:outline-none disabled:opacity-50"
								>
									<option value="">Latest (recommended)</option>
									{#each loaderVersions as version (version)}
										{#if version !== 'latest'}
											<option value={version}>{version}</option>
										{/if}
									{/each}
								</select>
								<div
									class="pointer-events-none absolute inset-y-0 right-0 flex items-center px-3 text-muted-foreground"
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
							<p class="mt-1 font-sans text-xs text-muted-foreground">
								{loadingLoaderVersions
									? 'Looking up published builds…'
									: 'Pin an exact build, or leave it on Latest to follow the loader.'}
							</p>
						</div>
					{/if}
				</div>

				<!-- Step 2: Resources & Network -->
			{:else if currentStep === 2}
				<div class="space-y-6 rounded-none border border-border bg-card p-6">
					<div>
						<h2 class="text-lg font-light text-foreground">Step 3: Resources & Network</h2>
						<p class="mt-1 font-sans text-xs text-muted-foreground">
							Configure port routing, reverse proxy domain, and a memory request (not a hard cap
							until the engine reports it as enforced).
						</p>
					</div>

					<!-- Connection Method (Carbon Tiles) -->
					{#if proxyEnabled}
						<div class="space-y-2">
							<span
								id="ingress-method-label"
								class="block font-mono text-xs font-semibold tracking-wider text-muted-foreground uppercase"
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
										? 'border-primary bg-accent shadow-[inset_3px_0_0_var(--primary)]'
										: 'border-border bg-background hover:bg-card'}"
								>
									<h4 class="text-sm font-semibold text-foreground">Direct Port</h4>
									<p class="mt-1 text-xs text-muted-foreground">
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
										? 'border-primary bg-accent shadow-[inset_3px_0_0_var(--primary)]'
										: 'border-border bg-background hover:bg-card'}"
								>
									<h4 class="text-sm font-semibold text-foreground">SNI / Custom Hostname</h4>
									<p class="mt-1 text-xs text-muted-foreground">
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
										class="block font-mono text-xs font-semibold tracking-wider text-muted-foreground uppercase"
									>
										Proxy Listener
									</label>
									<div class="relative">
										<select
											id="proxy_listener"
											bind:value={formData.proxyListenerId}
											class="focus-ring h-10 w-full cursor-pointer appearance-none rounded-none border border-border bg-background px-4 pr-10 text-sm text-foreground transition-colors focus:bg-accent"
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
											class="pointer-events-none absolute inset-y-0 right-0 flex items-center px-3 text-muted-foreground"
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
									class="font-mono text-xs font-semibold tracking-wider text-muted-foreground uppercase"
								>
									Server Port
								</label>
								<button
									type="button"
									onclick={refreshAvailablePort}
									class="focus-ring cursor-pointer font-mono text-xs text-accent-text hover:underline"
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
								class="focus-ring h-10 w-full rounded-none border border-border bg-background px-4 font-mono text-sm text-foreground transition-colors focus:bg-accent {portError
									? '!border-destructive'
									: ''}"
							/>
							{#if portError}
								<p id="port-error" class="mt-1 font-mono text-xs text-destructive-text">
									{portError}
								</p>
							{/if}
						</div>
					{/if}

					<!-- Memory request and max players -->
					<div class="grid grid-cols-1 gap-4 pt-2 sm:grid-cols-2">
						<div class="space-y-1.5">
							<label
								for="memory"
								class="block font-mono text-xs font-semibold tracking-wider text-muted-foreground uppercase"
							>
								Memory request (MB)
							</label>
							<input
								id="memory"
								type="number"
								min="512"
								step="256"
								bind:value={formData.memory}
								class="focus-ring h-10 w-full rounded-none border border-border bg-background px-4 font-mono text-sm text-foreground transition-colors focus:bg-accent"
							/>
							<p class="font-mono text-xs text-muted-foreground">
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
								class="block font-mono text-xs font-semibold tracking-wider text-muted-foreground uppercase"
							>
								Max Players
							</label>
							<input
								id="max_players"
								type="number"
								min="1"
								max="1000"
								bind:value={formData.maxPlayers}
								class="focus-ring h-10 w-full rounded-none border border-border bg-background px-4 font-mono text-sm text-foreground transition-colors focus:bg-accent"
							/>
						</div>
					</div>

					<!-- Additional Ports Editor -->
					<div class="border-t border-border pt-2">
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
				<div class="space-y-6 rounded-none border border-border bg-card p-6">
					<div>
						<h2 class="text-lg font-light text-foreground">Step 4: Cluster Placement & Runtime</h2>
						<p class="mt-1 font-sans text-xs text-muted-foreground">
							Designate physical cluster host and configure container startup options.
						</p>
					</div>

					{#if archiveMode}
						<div
							class="space-y-2 rounded-none border border-border bg-background p-4"
							data-testid="archive-destination-confirmation"
						>
							<p
								class="font-mono text-xs font-semibold tracking-wider text-muted-foreground uppercase"
							>
								Archive destination
							</p>
							<div class="flex flex-wrap gap-x-6 gap-y-1 font-mono text-xs">
								<span class="inline-flex items-baseline gap-1.5">
									<span class="text-muted-foreground">Node</span>
									<span class="text-foreground" data-testid="archive-destination-node">
										{archiveNodeId || 'not selected'}
									</span>
								</span>
								<span class="inline-flex items-baseline gap-1.5">
									<span class="text-muted-foreground">Port</span>
									<span class="text-foreground">{useProxyMode ? 'proxy' : formData.port}</span>
								</span>
								{#if archivePreviewView}
									<span class="inline-flex items-baseline gap-1.5">
										<span class="text-muted-foreground">Required</span>
										<span class="text-foreground">{archivePreviewView.requiredBytes}</span>
									</span>
									<span class="inline-flex items-baseline gap-1.5">
										<span class="text-muted-foreground">Available</span>
										<span class="text-foreground">{archivePreviewView.availableBytes ?? '—'}</span>
									</span>
								{/if}
							</div>
							{#if archivePreviewView}
								<p
									class="font-sans text-xs {archivePreviewView.capacity === 'insufficient'
										? 'text-destructive-text'
										: 'text-muted-foreground'}"
								>
									{archivePreviewView.capacityText}
								</p>
								{#if archivePreviewView.isBlocked}
									<p class="font-sans text-xs text-destructive-text">
										This archive has blocking findings and cannot be created.
									</p>
								{/if}
							{:else}
								<p class="font-sans text-xs text-muted-foreground">
									Waiting for the archive preview to verify this destination's free space.
								</p>
							{/if}
						</div>
					{/if}

					<!-- Cluster Placement Mode (Carbon Tiles) -->
					<div class="space-y-3">
						<span
							id="node-placement-label"
							class="block font-mono text-xs font-semibold tracking-wider text-muted-foreground uppercase"
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
								aria-disabled={archiveMode}
								onclick={() => {
									if (!archiveMode) nodePlacementMode = 'auto';
								}}
								class="focus-ring relative cursor-pointer rounded-none border p-4 text-left transition-colors aria-disabled:cursor-not-allowed {nodePlacementMode ===
								'auto'
									? 'border-primary bg-accent shadow-[inset_3px_0_0_var(--primary)]'
									: 'border-border bg-background hover:bg-card'}"
							>
								<div class="flex items-center gap-2">
									<Activity class="h-4 w-4 text-accent-text" />
									<h4 class="text-sm font-semibold text-foreground">
										Auto-Placement (Load Balanced)
									</h4>
								</div>
								<p class="mt-1 text-xs {archiveMode ? 'text-foreground' : 'text-muted-foreground'}">
									{archiveMode
										? 'Unavailable for archives: preview and create must bind the same node so storage is verified.'
										: 'Cluster scheduler selects optimal worker node based on available memory and active workload.'}
								</p>
							</button>

							<button
								type="button"
								role="radio"
								aria-checked={nodePlacementMode === 'manual'}
								onclick={() => (nodePlacementMode = 'manual')}
								class="focus-ring relative cursor-pointer rounded-none border p-4 text-left transition-colors {nodePlacementMode ===
								'manual'
									? 'border-primary bg-accent shadow-[inset_3px_0_0_var(--primary)]'
									: 'border-border bg-background hover:bg-card'}"
							>
								<div class="flex items-center gap-2">
									<ServerIcon class="h-4 w-4 text-accent-text" />
									<h4 class="text-sm font-semibold text-foreground">Target Specific Node</h4>
								</div>
								<p class="mt-1 text-xs text-muted-foreground">
									Pin this Minecraft container instance to a designated Docker host machine.
								</p>
							</button>
						</div>

						{#if archiveMode && !archiveNodeId && !(archiveImportState.uploadId && destinationChoices.length === 0)}
							<div
								class="border border-border bg-background p-4 text-xs text-muted-foreground"
								data-testid="archive-destination-required"
							>
								Choose an enabled destination node. Archive preview and create need the same
								concrete node so its free space is verified before the import.
							</div>
						{/if}

						{#if nodePlacementMode === 'auto'}
							<div class="space-y-2 rounded-none border border-border bg-background p-4">
								<label
									for="placement_strategy"
									class="block font-mono text-xs text-muted-foreground uppercase"
								>
									Placement Strategy
								</label>
								<select
									id="placement_strategy"
									bind:value={placementStrategy}
									class="focus-ring h-10 w-full rounded-none border border-border bg-card px-3 font-mono text-xs text-foreground transition-colors focus:bg-accent"
								>
									<option value="least_memory">Least Memory Allocated (Recommended)</option>
									<option value="least_servers">Least Active Server Count</option>
									<option value="round_robin">Round Robin Distribution</option>
								</select>
							</div>
						{:else}
							<div class="space-y-2">
								{#if loadingNodes && destinationChoices.length === 0}
									<div
										class="flex items-center justify-center border border-border bg-background p-6"
									>
										<CarbonInlineLoading description="Scanning cluster topology..." />
									</div>
								{:else if archivePreflightWaiting}
									<div
										class="border border-border bg-background p-4 text-xs text-muted-foreground"
										data-testid="archive-destination-preflight"
									>
										Preparing the archive placement preflight so you can choose an enabled
										destination without node inventory access.
									</div>
								{:else if archiveMode ? destinationChoices.length === 0 : nodes.length === 0}
									{#if archiveMode && archiveImportState.uploadId}
										<div
											class="space-y-3 border border-border bg-background p-4"
											data-testid="archive-destination-empty"
										>
											<p class="text-xs text-muted-foreground">
												{#if archivePreflightSettled}
													The archive placement preflight found no enabled destination nodes. Enable
													a Docker node in Settings → Docker Nodes, then retry the preview.
												{:else if archiveImportState.error}
													The archive placement preflight could not list enabled destinations. Retry
													the preview after checking the controller.
												{:else}
													The archive placement preflight returned no enabled destinations. Retry
													the preview after enabling a Docker node in Settings → Docker Nodes.
												{/if}
											</p>
											<CarbonButton
												kind="secondary"
												size="sm"
												disabled={archiveUploading ||
													archiveImportState.preparing ||
													Boolean(archiveSubmission)}
												onclick={retryArchiveDestinations}
												data-testid="archive-destination-retry"
											>
												Retry preflight
											</CarbonButton>
										</div>
									{:else}
										<div
											class="border border-border bg-background p-4 text-xs text-muted-foreground"
										>
											No worker nodes available. Add and enable a Docker node in Settings → Docker
											Nodes before creating servers.
										</div>
									{/if}
								{:else if archiveMode}
									<div
										role="radiogroup"
										aria-label="Archive destination node"
										class="grid grid-cols-1 gap-3 sm:grid-cols-2"
									>
										{#each destinationChoices as destination (destination.id)}
											{@const node = nodes.find((candidate) => candidate.id === destination.id)}
											<button
												type="button"
												role="radio"
												aria-checked={selectedNodeId === destination.id}
												disabled={Boolean(archiveSubmission)}
												data-testid="archive-destination-option"
												onclick={() => {
													if (!archiveSubmission) selectedNodeId = destination.id;
												}}
												class="focus-ring relative cursor-pointer rounded-none border p-3.5 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-50 {selectedNodeId ===
												destination.id
													? 'border-primary bg-accent shadow-[inset_3px_0_0_var(--primary)]'
													: 'border-border bg-background hover:bg-card'}"
											>
												<div class="flex items-center justify-between gap-2">
													<span class="text-sm font-medium text-foreground">{destination.name}</span
													>
													{#if selectedNodeId === destination.id}
														<CarbonTag type="blue" size="sm">SELECTED</CarbonTag>
													{/if}
												</div>
												{#if node}
													<p class="mt-1 truncate font-mono text-xs text-muted-foreground">
														{node.host || node.advertisedIp || 'local socket'}
													</p>
													{#if Number(node.availableMemoryMb) > 0}
														<p class="mt-1 font-mono text-xs text-muted-foreground">
															{(Number(node.availableMemoryMb) / 1024).toFixed(1)} GB free
														</p>
													{/if}
												{:else}
													<p class="mt-1 font-mono text-xs text-muted-foreground">
														Enabled destination from the archive placement preflight
													</p>
												{/if}
											</button>
										{/each}
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
													? 'border-primary bg-accent shadow-[inset_3px_0_0_var(--primary)]'
													: 'border-border bg-background hover:bg-card'} {!node.enabled
													? 'cursor-not-allowed opacity-40'
													: ''}"
											>
												<div class="flex items-center justify-between">
													<div class="flex items-center gap-2">
														<span class="text-sm font-medium text-foreground">{node.name}</span>
														{#if node.isLocal}
															<span
																class="py-0.2 bg-secondary px-1.5 font-mono text-xs text-muted-foreground"
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
												<p class="mt-1 truncate font-mono text-xs text-muted-foreground">
													{node.host || node.advertisedIp || 'local socket'}
												</p>
												<div
													class="mt-1 flex items-center gap-1.5 font-mono text-xs text-muted-foreground"
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

					<!-- Archived runtime: container identity comes from the verified manifest. -->
					{#if archiveMode}
						<div class="border-t border-border pt-2" data-testid="archive-runtime-fields-locked">
							<div class="border border-border bg-background p-3">
								<p class="text-xs font-medium text-foreground">Managed runtime from the archive</p>
								<p class="mt-1 font-sans text-xs text-muted-foreground">
									Docker image, Detached Mode and Docker overrides are not used for an archive
									import. The verified archive runtime supplies the container configuration; memory
									and max players stay editable.
								</p>
							</div>
						</div>
					{:else}
						<!-- Advanced Docker Image -->
						<div class="space-y-1.5 border-t border-border pt-2">
							<label
								for="docker_image"
								class="block font-mono text-xs font-semibold tracking-wider text-muted-foreground uppercase"
							>
								Docker Image <span class="text-xs font-normal text-muted-foreground lowercase"
									>(Advanced)</span
								>
							</label>
							<div class="relative">
								<select
									id="docker_image"
									bind:value={formData.dockerImage}
									class="focus-ring h-10 w-full cursor-pointer appearance-none rounded-none border border-border bg-background px-4 pr-10 font-mono text-sm text-foreground transition-colors focus:bg-accent"
								>
									<option value="">Auto-select base image (Recommended)</option>
									{#each getUniqueDockerImages(dockerImages) as image (image.tag)}
										<option value={image.tag}>
											{getDockerImageDisplayName(image)}
										</option>
									{/each}
								</select>
								<div
									class="pointer-events-none absolute inset-y-0 right-0 flex items-center px-3 text-muted-foreground"
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

					<!-- Lifecycle Toggles -->
					<div class="space-y-3 border-t border-border pt-2">
						<h3
							class="font-mono text-xs font-semibold tracking-wider text-muted-foreground uppercase"
						>
							Lifecycle & Autostart
						</h3>
						<div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
							<!-- Start Immediately -->
							<label
								class="flex cursor-pointer items-center gap-3 rounded-none border border-border bg-background p-3 select-none hover:bg-card"
							>
								<input
									type="checkbox"
									bind:checked={formData.startImmediately}
									class="h-4 w-4 rounded-none accent-primary"
								/>
								<div>
									<span class="block text-xs font-medium text-foreground">Start Immediately</span>
									<span class="block font-sans text-xs text-muted-foreground"
										>Boot container upon creation</span
									>
								</div>
							</label>

							<!-- Detached Mode -->
							{#if !archiveMode}
								<label
									class="flex cursor-pointer items-center gap-3 rounded-none border border-border bg-background p-3 select-none hover:bg-card {useProxyMode
										? 'cursor-not-allowed opacity-50'
										: ''}"
								>
									<input
										type="checkbox"
										bind:checked={formData.detached}
										disabled={useProxyMode}
										class="h-4 w-4 rounded-none accent-primary"
									/>
									<div>
										<span class="block text-xs font-medium text-foreground">Detached Mode</span>
										<span class="block font-sans text-xs text-muted-foreground"
											>Persists past daemon stop</span
										>
									</div>
								</label>
							{/if}

							<!-- Auto Start -->
							<label
								class="flex cursor-pointer items-center gap-3 rounded-none border border-border bg-background p-3 select-none hover:bg-card {formData.detached &&
								!archiveMode
									? 'cursor-not-allowed opacity-50'
									: ''}"
							>
								<input
									type="checkbox"
									bind:checked={formData.autoStart}
									disabled={formData.detached && !archiveMode}
									class="h-4 w-4 rounded-none accent-primary"
								/>
								<div>
									<span class="block text-xs font-medium text-foreground">Auto Start</span>
									<span class="block font-sans text-xs text-muted-foreground"
										>Start when host daemon starts</span
									>
								</div>
							</label>

							<!-- Freeze on Idle -->
							<label
								class="flex cursor-pointer items-center gap-3 rounded-none border border-border bg-background p-3 select-none hover:bg-card"
							>
								<input
									type="checkbox"
									bind:checked={formData.autoHibernate}
									class="h-4 w-4 rounded-none accent-primary"
								/>
								<div>
									<span class="block text-xs font-medium text-foreground">Freeze on Idle</span>
									<span class="block font-sans text-xs text-muted-foreground"
										>Pause container when empty</span
									>
								</div>
							</label>

							<!-- Deep Sleep on Idle -->
							<label
								class="flex cursor-pointer items-center gap-3 rounded-none border border-border bg-background p-3 select-none hover:bg-card"
							>
								<input
									type="checkbox"
									bind:checked={formData.autoDeepSleep}
									class="h-4 w-4 rounded-none accent-primary"
								/>
								<div>
									<span class="block text-xs font-medium text-foreground">Deep Sleep on Idle</span>
									<span class="block font-sans text-xs text-muted-foreground"
										>Stop container when empty</span
									>
								</div>
							</label>
						</div>
					</div>

					<!-- Docker Overrides -->
					{#if !archiveMode}
						<div class="border-t border-border pt-2">
							<DockerOverridesEditor
								bind:overrides={formData.dockerOverrides}
								disabled={loading}
								onchange={(overrides) => (formData.dockerOverrides = overrides)}
							/>
						</div>
					{/if}
				</div>
			{:else if currentStep === 4}
				<div class="space-y-5">
					<div>
						<h2 class="text-lg font-light text-foreground">Step 5: Add-ons</h2>
						<p class="mt-1 text-xs text-muted-foreground">
							Add optional content and services now or manage them from the server page later.
						</p>
					</div>
					{#if archiveMode}
						<div
							class="border border-border bg-background p-4"
							data-testid="archive-addons-disabled"
						>
							<p class="text-sm font-semibold text-foreground">Standalone add-ons are disabled</p>
							<p class="mt-1 font-sans text-xs text-muted-foreground">
								The archive already contains its mods, plugins, datapacks and configuration. Archive
								creation uses the verified runtime, so separate add-on setup is skipped here. Manage
								individual add-ons from the server page after it is created.
							</p>
						</div>
					{:else}
						<nav aria-label="Add-on types" class="flex flex-wrap gap-2 border-b border-border pb-3">
							{#each addonTabs as tab (tab)}
								{@const count =
									tab === 'Mods'
										? selectedMods.length
										: tab === 'Modules'
											? selectedModules.length
											: selectedDatapacks.length + pendingDatapackUploads.length}
								<CarbonButton
									kind={addonTab === tab ? 'secondary' : 'tertiary'}
									size="sm"
									aria-pressed={addonTab === tab}
									onclick={() => selectAddonTab(tab)}
								>
									{tab}{count > 0 ? ` (${count})` : ''}
								</CarbonButton>
							{/each}
						</nav>

						{#if addonTab === 'Datapacks'}
							<div class="border border-border bg-card p-4">
								<div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
									<div>
										<p class="text-xs font-semibold tracking-wider text-foreground uppercase">
											Upload local datapack
										</p>
										<p class="mt-1 text-xs text-muted-foreground">
											Upload a <code class="font-mono">.zip</code> containing
											<code class="font-mono">pack.mcmeta</code>. The archive is queued and
											installed after the server is created.
										</p>
									</div>
									<div class="flex items-center gap-2">
										<input
											bind:this={datapackUploadInput}
											type="file"
											accept=".zip,application/zip"
											class="hidden"
											onchange={handleWizardDatapackUpload}
										/>
										<CarbonButton
											kind="secondary"
											size="sm"
											disabled={uploadingWizardDatapack}
											onclick={() => datapackUploadInput?.click()}
										>
											{#if uploadingWizardDatapack}
												<Loader2 class="h-4 w-4 animate-spin" />
												Uploading… {wizardDatapackUploadPercent}%
											{:else}
												<Upload class="h-4 w-4" />
												Upload Datapack
											{/if}
										</CarbonButton>
									</div>
								</div>
							</div>

							{#if selectedDatapacks.length > 0 || pendingDatapackUploads.length > 0}
								<div class="border border-border bg-card">
									<div
										class="border-b border-border px-4 py-2.5 text-xs font-semibold text-foreground uppercase"
									>
										Selected for install ({selectedDatapacks.length +
											pendingDatapackUploads.length})
									</div>
									<ul class="divide-y divide-border">
										{#each pendingDatapackUploads as dp (dp.sessionId)}
											<li
												class="flex items-center justify-between px-4 py-2.5 text-sm text-foreground"
											>
												<span class="flex min-w-0 items-center gap-2">
													<Upload class="h-4 w-4 shrink-0 text-muted-foreground" />
													<span class="truncate" title={dp.fileName}>{dp.name}</span>
													<span
														class="shrink-0 font-mono text-[10px] tracking-wider text-muted-foreground uppercase"
														>Local upload</span
													>
												</span>
												<button
													type="button"
													onclick={() => removePendingDatapackUpload(dp.sessionId)}
													aria-label={`Remove uploaded datapack ${dp.fileName}`}
													class="flex h-8 w-8 cursor-pointer items-center justify-center text-muted-foreground transition-colors hover:bg-accent hover:text-destructive-text"
												>
													<X class="h-4 w-4" />
												</button>
											</li>
										{/each}
										{#each selectedDatapacks as dp (dp.id)}
											<li
												class="flex items-center justify-between px-4 py-2.5 text-sm text-foreground"
											>
												<span class="flex min-w-0 items-center gap-2">
													<Package class="h-4 w-4 shrink-0 text-muted-foreground" />
													<span class="truncate">{dp.name}</span>
												</span>
												<button
													type="button"
													onclick={() => removeDatapack(dp.id)}
													aria-label={`Remove datapack ${dp.name}`}
													class="flex h-8 w-8 cursor-pointer items-center justify-center text-muted-foreground transition-colors hover:bg-accent hover:text-destructive-text"
												>
													<X class="h-4 w-4" />
												</button>
											</li>
										{/each}
									</ul>
								</div>
							{/if}

							{#if datapackGameVersion}
								<DatapackSearch
									gameVersion={datapackGameVersion}
									emptyHint="Search for datapacks, or skip this step and add them later."
								>
									{#snippet actions(d)}
										{@const added = selectedDatapacks.some((p) => p.id === d.id)}
										<button
											type="button"
											aria-pressed={added}
											onclick={() => toggleDatapack(d.id, d.name, d.indexer)}
											class="focus-ring flex h-9 cursor-pointer items-center gap-1.5 rounded-none px-3 font-sans text-xs font-medium transition-colors {added
												? 'bg-secondary text-muted-foreground hover:bg-secondary'
												: 'bg-primary text-primary-foreground hover:bg-[var(--button-primary-hover)]'}"
										>
											{#if added}<Check class="h-3.5 w-3.5" /> Added{:else}<Plus
													class="h-3.5 w-3.5"
												/> Add{/if}
										</button>
									{/snippet}
								</DatapackSearch>
							{:else}
								<p class="text-sm text-muted-foreground">
									This modpack release has no known Minecraft version. You can upload a ZIP or
									browse compatible datapacks from the server after creation.
								</p>
							{/if}
						{:else if addonTab === 'Mods'}
							<div class="space-y-4 border border-border bg-card p-4">
								{#if canBrowseMods}
									<p class="text-sm text-muted-foreground">
										Browse server-compatible mods for {loaderDisplayName(formData.modLoader)} on Minecraft
										{formData.mcVersion}. Required dependencies are checked when you select a
										release.
									</p>
									<CarbonButton kind="secondary" onclick={() => (showModBrowser = true)}
										>Browse & install</CarbonButton
									>
								{:else if selectedModpack}
									<p class="text-sm text-muted-foreground">
										The modpack provisions its own mods on first boot. Open the server’s Mods tab
										after provisioning to manage individual mods.
									</p>
								{:else if runtimeOverridden}
									<p class="text-sm text-muted-foreground">
										Remove the custom Docker game type, Minecraft version, entrypoint, or command to
										browse compatible mods during setup.
									</p>
									<CarbonButton kind="tertiary" onclick={() => goToStep(3)}
										>Review runtime settings</CarbonButton
									>
								{:else}
									<p class="text-sm text-muted-foreground">
										Choose Fabric, Forge, NeoForge, or Quilt and a Minecraft version to add mods
										during setup.
									</p>
									<CarbonButton kind="tertiary" onclick={() => goToStep(1)}
										>Choose game engine</CarbonButton
									>
								{/if}
								{#if selectedMods.length > 0}
									<!-- Same result-row idiom as the browser dialog and the studio:
										queued mods are cards with the Selected state tag. -->
									<div class="mt-4 grid grid-cols-1 gap-3 border-t border-border pt-4">
										{#each selectedMods as mod (mod.project)}
											<ModsCatalogResultCard
												result={{
													title: mod.name,
													description: '',
													iconUrl: '',
													author: '',
													downloads: 0,
													categories: [],
													platform: mod.selection.platform
												}}
												state="queued"
											>
												{#snippet badges()}
													<CarbonTag type="gray" size="sm" class="normal-case"
														>v{mod.versionLabel}</CarbonTag
													>
													{#if mod.files.length > 1}
														<CarbonTag type="gray" size="sm" class="normal-case"
															>{mod.files.length - 1} required dependencies</CarbonTag
														>
													{/if}
												{/snippet}
												{#snippet actions()}
													<CarbonButton
														kind="tertiary"
														size="sm"
														aria-label={`Remove ${mod.name}`}
														onclick={() =>
															(selectedMods = selectedMods.filter(
																(item) => item.project !== mod.project
															))}>Remove</CarbonButton
													>
												{/snippet}
											</ModsCatalogResultCard>
										{/each}
									</div>
								{/if}
							</div>
						{:else}
							<p class="text-sm text-muted-foreground">
								Selected modules are added as stopped services. You can start them from the server’s
								Modules tab. Set up BlueMap in World View after the world exists.
							</p>
							{#if loadingModules}
								<CarbonInlineLoading description="Loading module templates…" />
							{:else if modulesError}
								<p class="text-sm text-destructive-text" role="status">{modulesError}</p>
								<CarbonButton kind="secondary" onclick={loadModules}
									>Retry module templates</CarbonButton
								>
							{:else if moduleTemplates.length === 0}
								<p class="text-sm text-muted-foreground">
									This panel has no module templates available for setup.
								</p>
							{:else}
								<div class="grid gap-3 md:grid-cols-2">
									{#each moduleTemplates as template (template.id)}
										{@const selected = selectedModules.some((item) => item.id === template.id)}
										{@const restriction = moduleRestriction(template)}
										<div
											class="flex items-start justify-between gap-4 border border-border bg-card p-4"
										>
											<div class="min-w-0">
												<p class="text-sm font-semibold">{template.name}</p>
												<p class="mt-1 text-xs text-muted-foreground">{template.description}</p>
												{#if restriction}<p class="mt-2 text-xs text-muted-foreground">
														{restriction}
													</p>{/if}
											</div>
											<CarbonButton
												kind={selected ? 'secondary' : 'tertiary'}
												size="sm"
												aria-label={`${selected ? 'Remove' : 'Add'} ${template.name}`}
												aria-pressed={selected}
												disabled={Boolean(restriction) && !selected}
												onclick={() =>
													(selectedModules = selected
														? selectedModules.filter((item) => item.id !== template.id)
														: [...selectedModules, template])}
											>
												{selected ? 'Remove' : 'Add'}
											</CarbonButton>
										</div>
									{/each}
								</div>
							{/if}
						{/if}
					{/if}
				</div>
			{/if}

			{#if archiveMode && currentStep === steps.length - 1 && archiveCreateBlockedReason}
				<p
					role="status"
					data-testid="archive-create-blocked-help"
					class="border-l-4 border-primary bg-accent px-3 py-2 font-sans text-xs text-foreground"
				>
					{archiveCreateBlockedReason}
				</p>
			{/if}

			<!-- Carbon Wizard Navigation Bar -->
			<div class="flex items-center justify-between gap-3 border-t border-border pt-4">
				<CarbonButton
					href={resolve('/servers')}
					kind="tertiary"
					size="md"
					class="whitespace-nowrap"
					disabled={loading || Boolean(archiveSubmission)}
				>
					Cancel
				</CarbonButton>

				<div class="flex items-center gap-2">
					{#if currentStep > 0}
						<CarbonButton
							kind="secondary"
							size="md"
							class="gap-2 whitespace-nowrap"
							disabled={loading || Boolean(archiveSubmission)}
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
							disabled={loading || Boolean(archiveSubmission)}
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
							disabled={loading ||
								loadingVersions ||
								loadingModpackVersions ||
								uploadingWizardDatapack ||
								archiveUploading ||
								(archiveMode && !archiveCreateReady)}
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

{#if canBrowseMods && !createdServer}
	{#key modTarget}
		<ModBrowserDialog
			bind:open={showModBrowser}
			server={{
				id: '',
				modLoader: formData.modLoader,
				mcVersion: formData.mcVersion,
				modpackLoader: ModLoader.UNSPECIFIED
			}}
			onQueue={queueMods}
			queuedMods={selectedMods}
		/>
	{/key}
{/if}

<!-- Modpack Selection Modal (Sharp Carbon Modal) -->
<CarbonModal
	bind:open={showModpackDialog}
	title="Choose a modpack"
	description="Select a preset from the Modpacks Library or your favorites."
	size="2xl"
	hasFooter={false}
>
	<form
		class="mb-4 flex flex-wrap items-end gap-2"
		onsubmit={(event) => {
			event.preventDefault();
			void searchLibraryModpacks();
		}}
	>
		<div class="min-w-36 flex-1">
			<CarbonTextInput
				id="wizard-modpack-search"
				label="Search library"
				placeholder="Modpack name"
				bind:value={libraryModpackQuery}
			/>
		</div>
		<CarbonButton type="submit" disabled={loadingLibraryModpacks}>Search</CarbonButton>
		<CarbonButton
			kind="secondary"
			aria-pressed={showFavoriteModpacks}
			onclick={() => (showFavoriteModpacks = !showFavoriteModpacks)}
		>
			Favorites
		</CarbonButton>
	</form>
	<div class="max-h-[60vh] space-y-3 overflow-y-auto">
		{#if !showFavoriteModpacks && loadingLibraryModpacks}
			<CarbonInlineLoading description="Loading modpacks…" />
		{:else if !showFavoriteModpacks && libraryModpackError}
			<p class="text-sm text-destructive-text" role="status">{libraryModpackError}</p>
		{:else if (showFavoriteModpacks ? favoriteModpacks : libraryModpacks).length === 0}
			<div class="rounded-none border border-border bg-card p-8 text-center">
				<Package class="mx-auto mb-2 h-8 w-8 text-muted-foreground" />
				<p class="text-sm font-semibold text-foreground">
					{showFavoriteModpacks
						? 'No favorite modpacks yet'
						: 'No matching modpacks in the library'}
				</p>
				<p class="mt-1 text-xs text-muted-foreground">
					Use the <a href={resolve('/modpacks')} class="text-accent-text underline"
						>Modpacks Library</a
					> to sync providers or import a pack.
				</p>
			</div>
		{:else}
			{#each showFavoriteModpacks ? favoriteModpacks : libraryModpacks as modpack (modpack.id)}
				<button
					type="button"
					onclick={() => selectModpack(modpack)}
					class="flex w-full cursor-pointer items-start gap-3 rounded-none border border-border bg-card p-3.5 text-left transition-colors hover:bg-accent"
				>
					{#if modpack.logoUrl}
						<img
							src={modpack.logoUrl}
							alt={modpack.name}
							class="h-12 w-12 shrink-0 rounded-none border border-border object-cover"
						/>
					{/if}
					<div class="min-w-0 flex-1">
						<div class="flex items-center gap-2">
							<h4 class="text-sm font-semibold text-foreground">{modpack.name}</h4>
							<CarbonTag type="blue" size="sm">{modpack.indexer}</CarbonTag>
						</div>
						<p class="mt-1 line-clamp-2 text-xs text-muted-foreground">
							{modpack.summary}
						</p>
						<div class="mt-2 flex items-center gap-2 font-mono text-xs text-accent-text">
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
	{#if !showFavoriteModpacks && !loadingLibraryModpacks && !libraryModpackError && libraryTotal > libraryPageSize}
		<nav
			aria-label="Modpack pages"
			class="mt-4 flex flex-wrap items-center justify-between gap-2 border-t border-border pt-3"
		>
			<span class="text-xs text-muted-foreground"
				>{(libraryPage - 1) * libraryPageSize + 1}–{Math.min(
					libraryPage * libraryPageSize,
					libraryTotal
				)} of {libraryTotal}</span
			>
			<div class="flex gap-2">
				<CarbonButton
					kind="tertiary"
					size="sm"
					disabled={libraryPage === 1}
					onclick={() => searchLibraryModpacks(libraryPage - 1, libraryActiveQuery)}
					>Previous page</CarbonButton
				>
				<CarbonButton
					kind="tertiary"
					size="sm"
					disabled={libraryPage * libraryPageSize >= libraryTotal}
					onclick={() => searchLibraryModpacks(libraryPage + 1, libraryActiveQuery)}
					>Next page</CarbonButton
				>
			</div>
		</nav>
	{/if}
</CarbonModal>
