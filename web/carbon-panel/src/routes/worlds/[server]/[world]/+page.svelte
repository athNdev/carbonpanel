<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { resolve } from '$app/paths';
	import { pushState, replaceState } from '$app/navigation';
	import { page } from '$app/state';
	import { create } from '@bufbuild/protobuf';
	import { ConnectError, Code } from '@connectrpc/connect';
	import { toast } from 'svelte-sonner';
	import {
		AlertTriangle,
		ArrowLeft,
		Ban,
		LoaderCircle,
		Lock,
		MapPin,
		RefreshCw,
		Users
	} from '@lucide/svelte';
	import {
		CarbonPageHeader,
		CarbonBreadcrumbs,
		CarbonTag,
		CarbonButton,
		CarbonSelect,
		CarbonTextInput,
		CarbonInlineLoading,
		CarbonEmptyState,
		CarbonErrorState,
		CarbonAccordion,
		CarbonAccordionItem,
		CarbonModal,
		CarbonConfirm,
		CarbonStructuredList
	} from '$lib/components/carbon';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { formatRpcError } from '$lib/api/rpc-error';
	import { authStore, checkPermission } from '$lib/stores/auth';
	import { createWorldViewStore } from '$lib/stores/worldview.svelte';
	import {
		ListWorldViewTargetsRequestSchema,
		GetWorldViewStatusRequestSchema,
		GetWorldViewSessionRequestSchema,
		ListCapabilitiesRequestSchema,
		ListMapOverlaysRequestSchema,
		SetMapOverlayRequestSchema,
		SetPlayerVisibilityRequestSchema,
		WorldRef_Kind,
		type Capability,
		type MapOverlay,
		type GetWorldViewSessionResponse,
		type WorldRef
	} from '$lib/proto/carbonpanel/v1/worldview_pb';
	// Teleport/world/region actions reuse the existing ServerService RPC.
	import { SendCommandRequestSchema as ServerSendCommandRequestSchema } from '$lib/proto/carbonpanel/v1/server_pb';
	import { decodeWorldKey } from '$lib/features/worldview/world-ref';
	import {
		deriveViewState,
		formatAge,
		canMountEmbed,
		liveOverlaysEnabled,
		VIEW_STATE_LABEL,
		VIEW_STATE_TONE
	} from '$lib/features/worldview/state';
	import {
		createCapabilityMatrix,
		rendererName,
		type CapabilityMatrix
	} from '$lib/features/worldview/capabilities';
	import { evaluateSession } from '$lib/features/worldview/session';
	import {
		filterOverlays,
		overlayDisplay,
		overlayKindLabel
	} from '$lib/features/worldview/overlays';
	import { adminActions, buildAdminRequest, type AdminAction } from '$lib/features/worldview/admin';
	import { prefersReducedMotion } from '$lib/motion';

	const store = createWorldViewStore({
		listTargets: async () =>
			(
				await rpcClient.worldView.listWorldViewTargets(
					create(ListWorldViewTargetsRequestSchema),
					silentCallOptions
				)
			).targets,
		status: async (serverId, worldRef) =>
			(
				await rpcClient.worldView.getWorldViewStatus(
					create(GetWorldViewStatusRequestSchema, { serverId, worldRef }),
					silentCallOptions
				)
			).target
	});

	let auth = $derived($authStore);
	let serverId = $derived(page.params.server ?? '');
	let worldKey = $derived(page.params.world ?? '');
	let worldRef = $derived<WorldRef | null>(decodeWorldKey(worldKey));
	let statusKey = $derived(`${serverId}:${worldKey}`);
	let snapshot = $derived($store);
	let statusTarget = $derived(snapshot.statuses[statusKey]);
	let view = $derived(deriveViewState(statusTarget));

	let canRead = $derived(checkPermission(auth.permissions, 'world_view', 'read', serverId));
	let canObserve = $derived(
		checkPermission(auth.permissions, 'world_view', 'observe_players', serverId)
	);
	let canUpdate = $derived(checkPermission(auth.permissions, 'world_view', 'update', serverId));
	let canCommand = $derived(checkPermission(auth.permissions, 'servers', 'command', serverId));
	let denied = $state(false);

	let session = $state<GetWorldViewSessionResponse | undefined>(undefined);
	let sessionError = $state<unknown>(null);
	let capabilities = $state<Capability[]>([]);
	let capabilitiesRenderer = $derived(statusTarget?.renderer?.type ?? statusTarget?.rendererType);
	let matrix = $derived<CapabilityMatrix>(
		createCapabilityMatrix(capabilities, capabilitiesRenderer)
	);
	let overlays = $state<MapOverlay[]>([]);
	let overlaysError = $state<unknown>(null);
	let playerVisible = $state(false);
	let visibilityNote = $state('');

	let controlsOpen = $state(true);
	let overlayControlsOpen = $state(false);
	let adminOpen = $state(false);

	// URL-backed selection state (deep-link intent, not a renderer camera).
	// Pan/zoom are renderer-owned and are not simulated here: no renderer API
	// for camera control has been verified, so this surface exposes no inert
	// pan/zoom controls. Back/forward restores the selection without a reload.
	let dimension = $derived(
		page.url.searchParams.get('dimension') || statusTarget?.dimension || 'overworld'
	);
	let x = $derived(parseNumber(page.url.searchParams.get('x')) ?? 0);
	let z = $derived(parseNumber(page.url.searchParams.get('z')) ?? 0);
	let layer = $derived(page.url.searchParams.get('layer') || 'all');

	function parseNumber(value: string | null): number | null {
		if (value === null || value.trim() === '') return null;
		const parsed = Number(value);
		return Number.isFinite(parsed) ? parsed : null;
	}

	function writeQuery(patch: Record<string, string | null>, mode: 'push' | 'replace' = 'push') {
		if (typeof window === 'undefined') return;
		const url = new URL(resolve(`/worlds/${serverId}/${worldKey}`), window.location.origin);
		for (const [key, value] of page.url.searchParams) url.searchParams.set(key, value);
		for (const [key, value] of Object.entries(patch)) {
			if (value === null || value === '') url.searchParams.delete(key);
			else url.searchParams.set(key, value);
		}
		url.searchParams.set('server', serverId);
		url.searchParams.set('world', worldKey);
		// The URL is built from resolve(); these are shallow query-only updates.
		// eslint-disable-next-line svelte/no-navigation-without-resolve
		if (mode === 'push') pushState(url, {});
		// eslint-disable-next-line svelte/no-navigation-without-resolve
		else replaceState(url, {});
	}

	function isPermissionDenied(error: unknown): boolean {
		return error instanceof ConnectError && error.code === Code.PermissionDenied;
	}

	function handleError(error: unknown) {
		if (isPermissionDenied(error)) denied = true;
	}

	async function loadSession() {
		if (!worldRef) return;
		try {
			const response = await rpcClient.worldView.getWorldViewSession(
				create(GetWorldViewSessionRequestSchema, { serverId, worldRef }),
				silentCallOptions
			);
			session = response;
			sessionError = null;
		} catch (error) {
			handleError(error);
			session = undefined;
			sessionError = error;
		}
	}

	async function loadCapabilities() {
		try {
			const response = await rpcClient.worldView.listCapabilities(
				create(ListCapabilitiesRequestSchema, { serverId }),
				silentCallOptions
			);
			capabilities = response.capabilities;
		} catch (error) {
			handleError(error);
			capabilities = [];
		}
	}

	async function loadOverlays() {
		if (!worldRef) return;
		try {
			const response = await rpcClient.worldView.listMapOverlays(
				create(ListMapOverlaysRequestSchema, { serverId, worldRef, dimension }),
				silentCallOptions
			);
			overlays = response.overlays;
			overlaysError = null;
		} catch (error) {
			handleError(error);
			overlaysError = error;
		}
	}

	async function retry() {
		denied = false;
		sessionError = null;
		overlaysError = null;
		if (!worldRef) return;
		await Promise.allSettled([
			store.loadStatus(statusKey, serverId, worldRef),
			loadCapabilities(),
			loadOverlays(),
			loadSession()
		]);
	}

	// Status polling is owned by this page and refcounted in the store.
	$effect(() => {
		if (!canRead || !worldRef) return;
		const close = store.watchStatus(statusKey, serverId, worldRef, 5_000);
		void store.loadStatus(statusKey, serverId, worldRef).catch(handleError);
		return close;
	});

	$effect(() => {
		if (!canRead || !worldRef) return;
		void loadCapabilities();
	});

	$effect(() => {
		if (!canRead || !worldRef) return;
		void loadOverlays();
	});

	$effect(() => {
		if (!canRead || !worldRef) return;
		if (view === 'ready' || view === 'stale') void loadSession();
		else session = undefined;
	});

	let embed = $derived(evaluateSession(canMountEmbed(view, session?.proxyPath), session));
	let visibleOverlays = $derived(
		liveOverlaysEnabled(view)
			? filterOverlays(overlays, dimension, playerVisible && canObserve)
			: []
	);
	let filteredOverlays = $derived(
		layer === 'all' ? visibleOverlays : visibleOverlays.filter((overlay) => overlay.kind === layer)
	);
	// Announce state transitions only. Freezing the age out of the live region
	// avoids repeating a changing staleness string on each 5-second poll.
	let announcement = $derived(
		!worldRef ? 'This world link is invalid.' : `Map state: ${VIEW_STATE_LABEL[view]}`
	);

	function handleMapKeydown(event: KeyboardEvent) {
		if (event.key === 'Escape') {
			overlayControlsOpen = false;
			adminOpen = false;
		}
	}

	// Opt-in player visibility is audited by the backend and hidden by default.
	async function togglePlayers() {
		if (!worldRef || !matrix.players(canObserve).supported) return;
		const next = !playerVisible;
		try {
			const response = await rpcClient.worldView.setPlayerVisibility(
				create(SetPlayerVisibilityRequestSchema, { serverId, worldRef, visible: next }),
				silentCallOptions
			);
			playerVisible = response.visible;
			visibilityNote = next
				? 'Player visibility was enabled for this session. The disclosure was recorded in the activity log.'
				: 'Player visibility was disabled. No player positions are requested now.';
			await loadOverlays();
		} catch (error) {
			handleError(error);
			toast.error(formatRpcError(error, 'Could not change player visibility.'));
		}
	}

	// Overlay add/edit is only available with the capability and update permission.
	let overlayModalOpen = $state(false);
	let editingOverlay = $state<MapOverlay | null>(null);
	let formLabel = $state('');
	let formX = $state('');
	let formZ = $state('');
	let formKind = $state('waypoint');

	function openOverlayForm(overlay: MapOverlay | null) {
		editingOverlay = overlay;
		formLabel = overlay?.label ?? '';
		formX = overlay ? String(overlay.x) : String(x);
		formZ = overlay ? String(overlay.z) : String(z);
		formKind = overlay?.kind ?? 'waypoint';
		overlayModalOpen = true;
	}

	async function saveOverlay() {
		if (!worldRef) return;
		const px = Number(formX);
		const pz = Number(formZ);
		if (!Number.isFinite(px) || !Number.isFinite(pz)) {
			toast.error('Enter numeric X and Z coordinates.');
			return;
		}
		try {
			await rpcClient.worldView.setMapOverlay(
				create(SetMapOverlayRequestSchema, {
					serverId,
					overlay: {
						id: editingOverlay?.id ?? crypto.randomUUID(),
						serverId,
						worldRef,
						dimension,
						kind: formKind,
						x: px,
						z: pz,
						label: formLabel,
						sensitive: editingOverlay?.sensitive ?? false
					}
				}),
				silentCallOptions
			);
			overlayModalOpen = false;
			await loadOverlays();
			toast.success(editingOverlay ? 'Overlay updated.' : 'Overlay added.');
		} catch (error) {
			handleError(error);
			toast.error(formatRpcError(error, 'Could not save the overlay.'));
		}
	}

	// Administration is separated from viewing and uses the existing command RPC.
	let adminActionList = $derived(adminActions(canCommand));
	let confirmAdminOpen = $state(false);
	let pendingAdmin = $state<AdminAction | null>(null);
	let adminBusy = $state(false);
	let adminError = $state('');

	function requestAdmin(action: AdminAction) {
		if (!action.allowed) return;
		pendingAdmin = action;
		adminError = '';
		confirmAdminOpen = true;
	}

	async function confirmAdmin() {
		const action = pendingAdmin;
		if (!action || !canCommand) return;
		const request = buildAdminRequest(action, serverId, { x, z });
		if (!request) {
			adminError = 'That command needs a valid X/Z position. Set map coordinates first.';
			return;
		}
		adminBusy = true;
		adminError = '';
		try {
			const response = await rpcClient.server.sendCommand(
				create(ServerSendCommandRequestSchema, { id: request.serverId, command: request.command }),
				silentCallOptions
			);
			if (!response.success) {
				adminError = response.error || response.output || 'The server rejected the command.';
				return;
			}
			confirmAdminOpen = false;
			toast.success(`${action.label} was sent to the server.`);
		} catch (error) {
			handleError(error);
			adminError = isPermissionDenied(error)
				? 'Permission denied: your account cannot run commands on this server. A map session cannot grant this.'
				: formatRpcError(error, 'The command could not be sent.');
		} finally {
			adminBusy = false;
		}
	}

	let worldChip = $derived.by(() => {
		if (!worldRef) return '';
		switch (worldRef.kind) {
			case WorldRef_Kind.LIBRARY_BINDING:
				return `Library binding ${worldRef.bindingId}`;
			case WorldRef_Kind.STATIC_IMPORT:
				return `Static snapshot ${worldRef.snapshotId}`;
			default:
				return 'Server-local world';
		}
	});
</script>

<div class="h-full flex-1 space-y-4 bg-background font-sans text-foreground">
	<CarbonBreadcrumbs
		items={[
			{ label: 'Worlds', href: resolve('/worlds') },
			{ label: statusTarget?.name || worldRef?.levelName || 'World map' }
		]}
	/>

	<CarbonPageHeader
		title={statusTarget?.name || worldRef?.levelName || 'World map'}
		description="Observation-only map view. Administrative actions are kept separate below."
	>
		{#snippet tag()}
			<CarbonTag type={VIEW_STATE_TONE[view]} size="sm">{VIEW_STATE_LABEL[view]}</CarbonTag>
		{/snippet}
		{#snippet actions()}
			<CarbonButton kind="tertiary" size="sm" href={resolve('/worlds')}>
				<ArrowLeft class="h-3.5 w-3.5" />
				<span>All worlds</span>
			</CarbonButton>
			<CarbonButton kind="secondary" size="sm" onclick={retry}>
				<RefreshCw class="h-3.5 w-3.5" />
				<span>Retry</span>
			</CarbonButton>
		{/snippet}
	</CarbonPageHeader>

	<div class="sr-only" role="status" aria-live="polite">{announcement}</div>

	{#if auth.isLoading}
		<div class="border border-border bg-card p-6" role="status">
			<CarbonInlineLoading description="Checking world-view access…" />
		</div>
	{:else if !canRead || denied}
		<CarbonEmptyState
			title="No world-view access"
			description="Your account needs world_view/read on this server. No map frame is loaded for this session. Ask an administrator for access."
		>
			{#snippet icon()}
				<Lock class="h-6 w-6" />
			{/snippet}
		</CarbonEmptyState>
	{:else if !worldRef}
		<CarbonErrorState
			title="Invalid world link"
			message="This URL does not carry a recognisable world reference. Return to the world picker and open the world again."
		/>
	{:else}
		<div class="grid min-w-0 gap-4 lg:grid-cols-[minmax(0,1fr)_20rem]">
			<section class="min-w-0 space-y-3" aria-label="Map surface">
				<div class="flex flex-wrap items-center gap-2 border border-border bg-card px-3 py-2">
					<span class="mono-label">WORLD</span>
					<span class="text-xs break-all text-muted-foreground">{worldChip}</span>
					{#if worldRef.kind === WorldRef_Kind.SERVER_LOCAL}
						<span class="text-xs text-muted-foreground">
							Import to World Library is not available in this build.
						</span>
					{:else if worldRef.kind === WorldRef_Kind.LIBRARY_BINDING}
						<span class="text-xs text-muted-foreground">
							Library navigation is not available in this build.
						</span>
					{/if}
				</div>

				{#if view === 'loading'}
					<div class="border border-border bg-card p-8" role="status">
						<CarbonInlineLoading description="Checking renderer state…" />
					</div>
				{:else if view === 'onboarding'}
					<div class="space-y-3 border border-border bg-card p-8">
						<h2 class="text-base font-semibold">No map renderer installed</h2>
						<p class="max-w-xl text-sm text-muted-foreground">
							This world has no renderer yet. BlueMap is the recommended built-in renderer. Install
							a renderer module for this server to see a map here.
						</p>
						<div class="flex flex-wrap gap-2">
							<CarbonButton kind="primary" size="sm" href={resolve('/modules')}>
								Open modules
							</CarbonButton>
						</div>
						<p class="text-xs text-muted-foreground">
							Setup documentation (docs/world-viewing.md) is not linked in this build yet.
						</p>
					</div>
				{:else if view === 'installing'}
					<div class="space-y-3 border border-border bg-card p-8" role="status">
						<div class="flex items-center gap-2">
							<LoaderCircle
								class="h-5 w-5 text-accent-text {prefersReducedMotion() ? '' : 'animate-spin'}"
							/>
							<h2 class="text-base font-semibold">Installing renderer</h2>
						</div>
						<p class="text-sm text-muted-foreground">
							The renderer module is being created and started. No map is mounted until it reports
							ready tiles.
						</p>
					</div>
				{:else if view === 'rendering'}
					<div class="space-y-3 border border-border bg-card p-8" role="status">
						<div class="flex items-center gap-2">
							<LoaderCircle
								class="h-5 w-5 text-chart-4 {prefersReducedMotion() ? '' : 'animate-spin'}"
							/>
							<h2 class="text-base font-semibold">Rendering map</h2>
						</div>
						<p class="text-sm text-muted-foreground">
							The initial render is in progress. The backend does not report a percentage, so none
							is shown here.
						</p>
					</div>
				{:else if view === 'error'}
					<CarbonErrorState
						title="Renderer error"
						message={statusTarget?.error ||
							(sessionError
								? formatRpcError(sessionError, 'The renderer is unavailable.')
								: 'The renderer reported an error. Retry to check again.')}
						retryLabel="Retry"
						onretry={retry}
					/>
				{:else if view === 'unsupported'}
					<div class="space-y-3 border border-border bg-card p-8">
						<div class="flex items-center gap-2">
							<Ban class="h-5 w-5 text-destructive-text" />
							<h2 class="text-base font-semibold">Unsupported renderer</h2>
						</div>
						<p class="text-sm text-muted-foreground">
							{rendererName(capabilitiesRenderer)} has no verified adapter capability here. No substitute
							map is shown.
						</p>
					</div>
				{:else if view === 'stopped'}
					<div class="space-y-3 border border-border bg-card p-8">
						<div class="flex items-center gap-2">
							<AlertTriangle class="h-5 w-5 text-warning" />
							<h2 class="text-base font-semibold">Renderer stopped</h2>
						</div>
						<p class="text-sm text-muted-foreground">
							Live tiles are unavailable. Last observed
							{formatAge(statusTarget?.freshness?.observedAt ?? statusTarget?.lastRender)}. No live
							overlay is active.
						</p>
					</div>
				{:else if view === 'stale' || view === 'ready'}
					{#if view === 'stale'}
						<div
							class="flex flex-wrap items-center gap-2 border border-warning/60 bg-warning/15 px-3 py-2 text-xs"
						>
							<AlertTriangle class="h-4 w-4 shrink-0 text-warning" />
							<span class="font-semibold text-warning">LAST-KNOWN TILES</span>
							<span class="text-foreground">
								Observed {formatAge(
									statusTarget?.freshness?.observedAt ?? statusTarget?.lastRender
								)}. Live overlays are disabled.
							</span>
						</div>
					{/if}
					{#if embed.src}
						<div
							class="focus-ring relative min-h-[24rem] border border-border bg-card"
							role="region"
							aria-label="World map, {VIEW_STATE_LABEL[
								view
							].toLowerCase()}. Map navigation is provided by the renderer; tab into the map to use its controls."
							tabindex="0"
							onkeydown={handleMapKeydown}
						>
							<iframe
								src={embed.src}
								title="World map for {statusTarget?.name || worldRef?.levelName || 'this world'}"
								tabindex="0"
								class="h-[36rem] w-full border-0 bg-background sm:h-[44rem]"
								loading="lazy"
								sandbox="allow-scripts allow-same-origin allow-forms allow-popups"
							></iframe>
						</div>
					{:else}
						<div class="space-y-3 border border-border bg-card p-8">
							<h2 class="text-base font-semibold">Map not available</h2>
							<p class="text-sm text-muted-foreground">
								{embed.reason || 'No safe map path was returned.'}
							</p>
							<CarbonButton kind="secondary" size="sm" onclick={retry}>Retry</CarbonButton>
						</div>
					{/if}
				{/if}
			</section>

			<aside class="min-w-0 space-y-3">
				<p class="text-xs text-muted-foreground">
					Pan, zoom and coordinate search are provided by the renderer's own map controls.
					Coordinates in the link are intent for player/admin actions, not a simulated camera.
				</p>

				<CarbonAccordion>
					<CarbonAccordionItem title="View controls" bind:open={controlsOpen}>
						<div class="space-y-3">
							<CarbonSelect
								label="Overlay dimension"
								value={dimension}
								disabled={!matrix.dimensions().supported}
								onchange={(event) =>
									writeQuery({ dimension: (event.currentTarget as HTMLSelectElement).value })}
								helperText={matrix.dimensions().supported
									? 'Filters panel-owned overlays only.'
									: matrix.dimensions().reason}
							>
								<option value="overworld">Overworld</option>
								<option value="nether">Nether</option>
								<option value="end">End</option>
							</CarbonSelect>
							<div class="space-y-1">
								<span class="text-xs text-foreground">View mode</span>
								{#each matrix.viewModes() as mode (mode.id)}
									<div class="flex items-center justify-between gap-2 text-xs">
										<span class={mode.supported ? 'text-foreground' : 'text-muted-foreground'}
											>{mode.label}</span
										>
										<span class={mode.supported ? 'text-success-text' : 'text-muted-foreground'}>
											{mode.supported ? 'Available' : mode.reason}
										</span>
									</div>
								{/each}
							</div>
							{#if matrix.timeWeather().supported}
								<p class="text-xs text-muted-foreground">
									Time and weather controls are available in the renderer.
								</p>
							{/if}
							{#if matrix.telemetry().supported}
								<p class="text-xs text-muted-foreground">Telemetry is available in the renderer.</p>
							{/if}
						</div>
					</CarbonAccordionItem>

					<CarbonAccordionItem title="Overlays" bind:open={overlayControlsOpen}>
						<div class="space-y-3">
							{#if !playerVisible && canObserve && matrix.players(true).supported}
								<CarbonButton kind="tertiary" size="sm" onclick={togglePlayers}>
									<Users class="h-3.5 w-3.5" />
									<span>Show player markers</span>
								</CarbonButton>
							{:else if playerVisible}
								<CarbonButton kind="tertiary" size="sm" onclick={togglePlayers}>
									<span>Hide player markers</span>
								</CarbonButton>
							{:else}
								<p class="text-xs text-muted-foreground">{matrix.players(canObserve).reason}</p>
							{/if}
							{#if visibilityNote}
								<p class="text-xs text-success-text">{visibilityNote}</p>
							{/if}

							<CarbonSelect
								label="Overlay layer"
								value={layer}
								onchange={(event) =>
									writeQuery({ layer: (event.currentTarget as HTMLSelectElement).value })}
							>
								<option value="all">All overlays</option>
								<option value="marker">Markers</option>
								<option value="poi">Points of interest</option>
								<option value="waypoint">Waypoints</option>
								<option value="region">Regions</option>
							</CarbonSelect>

							{#if matrix.overlayEditing(canUpdate).supported}
								<CarbonButton kind="secondary" size="sm" onclick={() => openOverlayForm(null)}>
									<MapPin class="h-3.5 w-3.5" />
									<span>Add overlay</span>
								</CarbonButton>
							{:else}
								<p class="text-xs text-muted-foreground">
									{matrix.overlayEditing(canUpdate).reason}
								</p>
							{/if}

							{#if overlaysError}
								<p class="text-xs text-destructive-text">
									{formatRpcError(overlaysError, 'Overlays could not be loaded.')}
								</p>
							{:else if filteredOverlays.length === 0}
								<p class="text-xs text-muted-foreground">
									No overlays in this layer for the current state.
								</p>
							{:else}
								<CarbonStructuredList>
									{#each filteredOverlays as overlay (overlay.id)}
										<div
											class="flex flex-wrap items-center justify-between gap-2 px-4 py-2 text-xs"
										>
											<div class="min-w-0">
												<p class="truncate font-medium">{overlayDisplay(overlay).label}</p>
												<p class="text-muted-foreground">
													{overlayKindLabel(overlay.kind)}{overlayDisplay(overlay).coordinates
														? ` · ${overlayDisplay(overlay).coordinates}`
														: ''}
												</p>
											</div>
											{#if matrix.overlayEditing(canUpdate).supported}
												<CarbonButton
													kind="tertiary"
													size="sm"
													onclick={() => openOverlayForm(overlay)}
												>
													Edit
												</CarbonButton>
											{/if}
										</div>
									{/each}
								</CarbonStructuredList>
							{/if}
							<p class="text-xs text-muted-foreground">
								Player profile links are not available in this build; marker labels stay opaque.
							</p>
						</div>
					</CarbonAccordionItem>

					<CarbonAccordionItem
						title="Administration"
						subtitle="Separate permissions"
						bind:open={adminOpen}
					>
						<div class="space-y-3">
							<p class="text-xs text-muted-foreground">
								These actions run console commands on the server. They use servers/command and are
								never authorised by this map session.
							</p>
							{#each adminActionList as action (action.id)}
								<div class="space-y-1 border-t border-border pt-2">
									<CarbonButton
										kind={action.id === 'pause-autosave' ? 'danger' : 'secondary'}
										size="sm"
										disabled={!action.allowed}
										onclick={() => requestAdmin(action)}
									>
										{action.label}
									</CarbonButton>
									{#if !action.allowed}
										<p class="text-xs text-destructive-text">{action.deniedReason}</p>
									{/if}
								</div>
							{/each}
						</div>
					</CarbonAccordionItem>
				</CarbonAccordion>
			</aside>
		</div>
	{/if}
</div>

<CarbonModal
	bind:open={overlayModalOpen}
	title={editingOverlay ? 'Edit overlay' : 'Add overlay'}
	size="sm"
>
	<div class="space-y-3">
		<CarbonTextInput label="Label" bind:value={formLabel} placeholder="Waypoint name" />
		<CarbonTextInput label="X" bind:value={formX} inputmode="decimal" />
		<CarbonTextInput label="Z" bind:value={formZ} inputmode="decimal" />
		<CarbonSelect label="Kind" bind:value={formKind}>
			<option value="marker">Marker</option>
			<option value="poi">Point of interest</option>
			<option value="waypoint">Waypoint</option>
			<option value="region">Region</option>
		</CarbonSelect>
		<div class="flex justify-end gap-2 pt-2">
			<CarbonButton kind="secondary" size="sm" onclick={() => (overlayModalOpen = false)}>
				Cancel
			</CarbonButton>
			<CarbonButton kind="primary" size="sm" onclick={saveOverlay}>Save overlay</CarbonButton>
		</div>
	</div>
</CarbonModal>

<CarbonConfirm
	bind:open={confirmAdminOpen}
	title={pendingAdmin?.label ?? 'Run command'}
	message={pendingAdmin?.consequence ?? ''}
	confirmLabel="Run command"
	danger={pendingAdmin?.id === 'pause-autosave'}
	confirming={adminBusy}
	onconfirm={confirmAdmin}
	onclose={() => {
		adminError = '';
	}}
/>
{#if adminError}
	<div class="mt-3 border border-destructive bg-destructive/20 p-3 text-xs" role="alert">
		{adminError}
	</div>
{/if}
