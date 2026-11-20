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
		timestampToMs,
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
	import BlueMapInstall from '$lib/components/worldview/BlueMapInstall.svelte';
	import NativeMapViewport from '$lib/components/worldview/NativeMapViewport.svelte';
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
	let statusKey = $derived((auth.user?.id ?? '') + ':' + serverId + ':' + worldKey);
	let snapshot = $derived($store);
	let statusTarget = $derived(snapshot.statuses[statusKey]);
	let view = $derived(deriveViewState(statusTarget));
	let statusError = $derived(snapshot.statusErrors[statusKey]);
	let statusUnavailable = $derived(
		!statusTarget && (Boolean(statusError) || snapshot.statusLoading[statusKey] === false)
	);
	let rendererControlsAvailable = $derived(
		view === 'ready' || view === 'stale' || view === 'saved'
	);

	let canRead = $derived(checkPermission(auth.permissions, 'world_view', 'read', serverId));
	let canObserve = $derived(
		checkPermission(auth.permissions, 'world_view', 'observe_players', serverId)
	);
	let canUpdate = $derived(checkPermission(auth.permissions, 'world_view', 'update', serverId));
	let canCommand = $derived(checkPermission(auth.permissions, 'servers', 'command', serverId));
	let deniedByRead = $state(false);
	let denied = $derived(deniedByRead || isReadDenied(statusError));
	let canBrowseAll = $derived(checkPermission(auth.permissions, 'world_view', 'read', '*'));
	let worldListHref = $derived(
		canBrowseAll
			? resolve('/worlds')
			: resolve('/worlds') + '?' + new URLSearchParams({ server: serverId })
	);
	let worldTitle = $derived(
		canRead && !denied ? statusTarget?.name || worldRef?.levelName || 'World map' : 'World map'
	);

	let session = $state<GetWorldViewSessionResponse | undefined>(undefined);
	let sessionError = $state<unknown>(null);
	let sessionOwner = $state('');
	let scopeVersion = 0;
	let sessionAttempt = 0;
	let refreshAttempt = 0;
	let overlaysAttempt = 0;
	let refreshWarning = $derived(
		Boolean(statusTarget && (statusError || (session && sessionError)))
	);
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
		const url = new URL(
			resolve('/worlds/[server]/[world]', { server: serverId, world: worldKey }),
			window.location.origin
		);
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

	function isReadDenied(error: unknown): boolean {
		return (
			isPermissionDenied(error) ||
			(error instanceof ConnectError && error.code === Code.Unauthenticated)
		);
	}

	function scopeGuard() {
		const key = statusKey;
		const version = scopeVersion;
		return () => key === statusKey && version === scopeVersion && canRead;
	}

	function clearSession() {
		sessionAttempt++;
		sessionOwner = '';
		session = undefined;
	}

	function handleError(error: unknown) {
		if (!isReadDenied(error)) return;
		deniedByRead = true;
		clearSession();
	}

	async function loadSession() {
		if (!worldRef) return;
		const current = scopeGuard();
		const key = statusKey;
		const attempt = ++sessionAttempt;
		try {
			const response = await rpcClient.worldView.getWorldViewSession(
				create(GetWorldViewSessionRequestSchema, { serverId, worldRef }),
				silentCallOptions
			);
			if (!current() || attempt !== sessionAttempt || denied) return;
			sessionOwner = key;
			session = response;
			sessionError = null;
		} catch (error) {
			if (!current() || attempt !== sessionAttempt) return;
			handleError(error);
			const expiry = timestampToMs(session?.expiresAt);
			if (expiry !== null && expiry <= Date.now()) clearSession();
			sessionError = error;
		}
	}

	async function loadCapabilities() {
		const current = scopeGuard();
		try {
			const response = await rpcClient.worldView.listCapabilities(
				create(ListCapabilitiesRequestSchema, { serverId }),
				silentCallOptions
			);
			if (current()) capabilities = response.capabilities;
		} catch (error) {
			if (!current()) return;
			handleError(error);
			capabilities = [];
		}
	}

	async function loadOverlays() {
		if (!worldRef) return;
		const current = scopeGuard();
		const attempt = ++overlaysAttempt;
		try {
			const response = await rpcClient.worldView.listMapOverlays(
				create(ListMapOverlaysRequestSchema, { serverId, worldRef, dimension }),
				silentCallOptions
			);
			if (!current() || attempt !== overlaysAttempt) return;
			overlays = response.overlays;
			overlaysError = null;
		} catch (error) {
			if (!current() || attempt !== overlaysAttempt) return;
			handleError(error);
			overlaysError = error;
		}
	}

	async function retry() {
		if (!worldRef || !canRead) return;
		const wasAvailable = rendererControlsAvailable;
		const current = scopeGuard();
		const attempt = ++refreshAttempt;
		const [status] = await Promise.allSettled([
			store.loadStatus(statusKey, serverId, worldRef),
			loadCapabilities(),
			loadOverlays()
		]);
		if (
			current() &&
			attempt === refreshAttempt &&
			!denied &&
			status.status === 'fulfilled' &&
			wasAvailable &&
			rendererControlsAvailable
		)
			await loadSession();
	}

	// Each page scope owns its polling and rendered state. Store reset makes
	// an outstanding request from a departed route unable to write status.
	$effect(() => {
		if (!canRead || !worldRef || auth.isLoading) return;
		const key = statusKey;
		const close = store.watchStatus(key, serverId, worldRef, 5_000);
		void store.loadStatus(key, serverId, worldRef).catch(() => {});
		return () => {
			close();
			scopeVersion++;
			store.reset();
			deniedByRead = false;
			clearSession();
			sessionError = null;
			capabilities = [];
			overlays = [];
			overlaysError = null;
			playerVisible = false;
			visibilityNote = '';
		};
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
		if (!canRead || !worldRef || denied || !rendererControlsAvailable) {
			clearSession();
			return;
		}
		void loadSession();
	});

	$effect(() => {
		if (!canRead || denied || view !== 'saved' || sessionOwner !== statusKey) return;
		const expiresAt = timestampToMs(session?.expiresAt);
		if (expiresAt === null) return;
		const refresh = setTimeout(
			() => void loadSession(),
			Math.max(1_000, expiresAt - Date.now() - 20_000)
		);
		return () => clearTimeout(refresh);
	});

	let embed = $derived(
		evaluateSession(
			!denied &&
				sessionOwner === statusKey &&
				(view === 'saved' || canMountEmbed(view, session?.proxyPath)),
			session
		)
	);
	let visibleOverlays = $derived(
		!statusError && liveOverlaysEnabled(view)
			? filterOverlays(overlays, dimension, playerVisible && canObserve)
			: []
	);
	let filteredOverlays = $derived(
		layer === 'all' ? visibleOverlays : visibleOverlays.filter((overlay) => overlay.kind === layer)
	);
	// Announce state transitions only. Freezing the age out of the live region
	// avoids repeating a changing staleness string on each 5-second poll.
	let announcement = $derived(
		!worldRef
			? 'This world link is invalid.'
			: statusUnavailable
				? 'Map status unavailable.'
				: refreshWarning
					? 'Map status refresh failed. Showing the last known state.'
					: 'Map state: ' + VIEW_STATE_LABEL[view]
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
			// Action-scoped denial must not revoke an otherwise readable world view.
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
			// Action-scoped denial must not revoke an otherwise readable world view.
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
			// servers/command denial is separate from world_view/read; keep the map usable.
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

<div
	class="h-full flex-1 space-y-4 bg-background font-sans text-foreground"
	data-testid="showcase-world-map-content"
>
	<CarbonBreadcrumbs
		items={[{ label: 'World View', href: worldListHref }, { label: worldTitle }]}
	/>

	<CarbonPageHeader title={worldTitle}>
		{#snippet tag()}
			{#if canRead && !denied && worldRef}
				{#if statusUnavailable}
					<CarbonTag type="red" size="sm">STATUS UNAVAILABLE</CarbonTag>
				{:else if refreshWarning}
					<CarbonTag type="yellow" size="sm">LAST KNOWN</CarbonTag>
				{:else}
					<CarbonTag type={VIEW_STATE_TONE[view]} size="sm">{VIEW_STATE_LABEL[view]}</CarbonTag>
				{/if}
			{/if}
		{/snippet}
		{#snippet actions()}
			<CarbonButton kind="tertiary" size="sm" href={worldListHref} class="gap-2">
				<ArrowLeft class="h-3.5 w-3.5" />
				<span>{canBrowseAll ? 'All worlds' : 'Server maps'}</span>
			</CarbonButton>
			{#if canRead && !denied && worldRef && !statusUnavailable && view !== 'unknown'}
				<CarbonButton kind="secondary" size="sm" class="gap-2" onclick={retry}>
					<RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
					<span>Refresh</span>
				</CarbonButton>
			{/if}
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
		<div class={['grid min-w-0 gap-4', view !== 'saved' && 'lg:grid-cols-[minmax(0,1fr)_20rem]']}>
			<section class="min-w-0 space-y-3" aria-label="Map surface">
				<div class="flex flex-wrap items-center gap-2 border border-border bg-card px-3 py-2">
					<span class="mono-label">WORLD</span>
					<span class="text-xs break-all text-muted-foreground">{worldChip}</span>
				</div>

				{#if refreshWarning}
					<p
						role="alert"
						class="border-l-4 border-warning bg-warning/10 px-3 py-2 text-xs text-foreground"
					>
						Could not refresh the map. Showing the last known state.
					</p>
				{/if}

				{#if statusUnavailable || view === 'unknown'}
					<CarbonErrorState
						title="Map status unavailable"
						message={statusError
							? formatRpcError(statusError, 'Could not check the map status.')
							: view === 'unknown'
								? 'Carbon Panel received an unrecognized renderer state.'
								: 'Carbon Panel received no map status for this world.'}
						onretry={retry}
					/>
				{:else if view === 'loading'}
					<div class="border border-border bg-card p-8" role="status">
						<CarbonInlineLoading description="Checking renderer state…" />
					</div>
				{:else if view === 'onboarding'}
					<div class="space-y-3 border border-border bg-card p-4 sm:p-6">
						<h2 class="text-base font-semibold">Set up a map</h2>
						<p class="max-w-xl text-sm text-muted-foreground">
							Connect a renderer to see this world in Carbon Panel.
						</p>
						<BlueMapInstall
							{serverId}
							target={statusTarget}
							can={(resource, action) =>
								checkPermission(auth.permissions, resource, action, serverId)}
							oncomplete={() => void retry()}
						/>
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
							Live tiles and overlays are unavailable. Last observation:
							{formatAge(statusTarget?.freshness?.observedAt ?? statusTarget?.lastRender)}.
						</p>
					</div>
				{:else if view === 'saved'}
					{#if embed.src}
						{#key statusKey}
							<NativeMapViewport
								proxyPath={embed.src}
								title="World terrain"
								preservePositionOnRenewal
								onReopen={() => void loadSession()}
							/>
						{/key}
					{:else if sessionError || session}
						<CarbonErrorState
							title="Map unavailable"
							message={sessionError
								? formatRpcError(sessionError, 'Could not open the map.')
								: embed.reason}
							onretry={retry}
						/>
					{:else}
						<CarbonInlineLoading description="Opening saved terrain" />
					{/if}
				{:else if view === 'stale' || view === 'ready'}
					{#if view === 'stale'}
						<div
							class="flex flex-wrap items-center gap-2 border border-warning/60 bg-warning/15 px-3 py-2 text-xs"
						>
							<AlertTriangle class="h-4 w-4 shrink-0 text-warning" />
							<span class="font-semibold text-warning">LAST-KNOWN TILES</span>
							<span class="text-foreground">
								Last observation: {formatAge(
									statusTarget?.freshness?.observedAt ?? statusTarget?.lastRender
								)}. Live overlays are disabled.
							</span>
						</div>
					{/if}
					{#if embed.src}
						<div
							class="focus-ring relative min-h-[24rem] border border-border bg-card"
							role="region"
							aria-label="World map, {refreshWarning
								? 'last known'
								: VIEW_STATE_LABEL[
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
								{sessionError
									? formatRpcError(sessionError, 'Could not start a map session.')
									: embed.reason || 'No safe map path was returned.'}
							</p>
							<CarbonButton kind="secondary" size="sm" onclick={retry}>Retry</CarbonButton>
						</div>
					{/if}
				{/if}
			</section>

			<aside class="min-w-0 space-y-3" class:hidden={view === 'saved'}>
				{#if rendererControlsAvailable}
					<p class="text-xs text-muted-foreground">
						Use the controls inside the map to pan, zoom, or search for coordinates.
					</p>
				{/if}

				<CarbonAccordion>
					{#if rendererControlsAvailable}
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
									<p class="text-xs text-muted-foreground">
										Telemetry is available in the renderer.
									</p>
								{/if}
							</div>
						</CarbonAccordionItem>
					{/if}

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
