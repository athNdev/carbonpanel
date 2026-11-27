<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { resolve } from '$app/paths';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { create } from '@bufbuild/protobuf';
	import { ConnectError, Code } from '@connectrpc/connect';
	import { toast } from 'svelte-sonner';
	import {
		AlertTriangle,
		ArrowLeft,
		Ban,
		Lock,
		Maximize,
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
		RendererState,
		WorldRef_Kind,
		type Capability,
		type MapOverlay,
		type GetWorldViewSessionResponse,
		type WorldRef
	} from '$lib/proto/carbonpanel/v1/worldview_pb';
	// Teleport/world/region actions reuse the existing ServerService RPC.
	import { SendCommandRequestSchema as ServerSendCommandRequestSchema } from '$lib/proto/carbonpanel/v1/server_pb';
	import { decodeWorldKey, encodeWorldKey } from '$lib/features/worldview/world-ref';
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
	import {
		adminActions,
		buildAdminRequest,
		type AdminAction,
		type AdminRequest
	} from '$lib/features/worldview/admin';
	import BlueMapInstall from '$lib/components/worldview/BlueMapInstall.svelte';
	import NativeMapViewport from '$lib/components/worldview/NativeMapViewport.svelte';
	import WorldMapLoading from '$lib/components/worldview/WorldMapLoading.svelte';
	import { parseNativeCoordinate, type MapPoint } from '$lib/features/worldview/native-map';
	import { loadNativePreview } from '$lib/features/worldview/native-map-preview';
	import { nativeMapMarkers } from '$lib/features/worldview/native-map-markers';

	let {
		serverId,
		worldKey,
		presentation = 'page',
		enabled = true,
		onexpand
	}: {
		serverId: string;
		worldKey?: string;
		presentation?: 'page' | 'preview' | 'dialog';
		enabled?: boolean;
		onexpand?: () => void;
	} = $props();

	const store = createWorldViewStore({
		listTargets: async () =>
			(
				await rpcClient.worldView.listWorldViewTargets(
					create(ListWorldViewTargetsRequestSchema),
					silentCallOptions
				)
			).targets,
		status: async (serverId, worldRef) => {
			const { target } = await rpcClient.worldView.getWorldViewStatus(
				create(GetWorldViewStatusRequestSchema, { serverId, worldRef }),
				silentCallOptions
			);
			if (target && target.serverId !== serverId)
				throw new Error('World View returned a different server.');
			return target;
		}
	});

	let auth = $derived($authStore);
	let preview = $derived(presentation === 'preview');
	let requestedRef = $derived(decodeWorldKey(worldKey) ?? undefined);
	let validLink = $derived(worldKey === undefined || Boolean(requestedRef));
	let statusKey = $derived((auth.user?.id ?? '') + ':' + serverId + ':' + (worldKey ?? 'active'));
	let snapshot = $derived($store);
	let statusTarget = $derived(snapshot.statuses[statusKey]);
	let effectiveWorldKey = $derived(worldKey ?? encodeWorldKey(statusTarget?.worldRef));
	let worldRef = $derived<WorldRef | null>(decodeWorldKey(effectiveWorldKey));
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
	const canManageModules = (resource: string, action: string) =>
		checkPermission(auth.permissions, resource, action, serverId);
	let modulesHref = $derived(resolve('/servers/[id]', { id: serverId }) + '?tab=modules');
	let blueMapSetupKey = $state('');
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
	let sessionExpired = $state(false);
	let sessionOwner = $state('');
	let scopeVersion = 0;
	let sessionAttempt = 0;
	let refreshAttempt = 0;
	let overlaysAttempt = 0;
	let capabilitiesAttempt = 0;
	let refreshWarning = $derived(
		Boolean(statusTarget && (statusError || (session && sessionError)))
	);
	let capabilitiesRenderer = $derived(statusTarget?.renderer?.type ?? statusTarget?.rendererType);
	let capabilitiesScope = $derived(
		statusTarget && effectiveWorldKey
			? JSON.stringify([
					statusKey,
					effectiveWorldKey,
					capabilitiesRenderer,
					statusTarget.renderer?.version
				])
			: ''
	);
	let capabilityResult = $state<{ key: string; items: Capability[]; error?: unknown }>();
	let capabilities = $derived(
		capabilityResult?.key === capabilitiesScope ? capabilityResult.items : []
	);
	let capabilitiesError = $derived(
		capabilityResult?.key === capabilitiesScope ? capabilityResult.error : undefined
	);
	let sessionMode = $derived(
		view === 'saved' ? 'native' : rendererControlsAvailable ? 'embedded' : null
	);
	let sessionScope = $derived(
		sessionMode && effectiveWorldKey
			? statusKey + ':' + effectiveWorldKey + ':' + sessionMode + ':' + capabilitiesRenderer
			: ''
	);
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
	let overlayDimension = $derived(view === 'saved' ? 'overworld' : dimension);
	let nativeMarkers = $derived(nativeMapMarkers(overlays));
	let x = $derived(parseNumber(page.url.searchParams.get('x')));
	let z = $derived(parseNumber(page.url.searchParams.get('z')));
	let layer = $derived(page.url.searchParams.get('layer') || 'all');
	let nativeLocation = $derived.by(() => {
		const x = parseNativeCoordinate(page.url.searchParams.get('x') ?? '');
		const z = parseNativeCoordinate(page.url.searchParams.get('z') ?? '');
		return x === null || z === null ? null : { x, z };
	});

	function parseNumber(value: string | null): number | null {
		if (value === null || value.trim() === '') return null;
		const parsed = Number(value);
		return Number.isFinite(parsed) ? parsed : null;
	}

	function writeQuery(patch: Record<string, string | null>, mode: 'push' | 'replace' = 'push') {
		if (typeof window === 'undefined') return;
		const url = new URL(page.url.href);
		for (const [key, value] of Object.entries(patch)) {
			if (value === null || value === '') url.searchParams.delete(key);
			else url.searchParams.set(key, value);
		}
		// Keep the current layout and any map selection; only the query changes.
		// eslint-disable-next-line svelte/no-navigation-without-resolve
		void goto(url, { replaceState: mode === 'replace', reset: false });
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
		const ref = effectiveWorldKey;
		const version = scopeVersion;
		return () =>
			key === statusKey && ref === effectiveWorldKey && version === scopeVersion && canRead;
	}

	function clearSession() {
		sessionAttempt++;
		sessionOwner = '';
		session = undefined;
		sessionExpired = false;
	}

	function handleError(error: unknown) {
		if (!isReadDenied(error)) return;
		deniedByRead = true;
		clearSession();
	}

	async function loadSession() {
		if (!worldRef || !sessionScope) return;
		const current = scopeGuard();
		const key = sessionScope;
		const native = sessionMode === 'native';
		const attempt = ++sessionAttempt;
		try {
			const response = await rpcClient.worldView.getWorldViewSession(
				create(GetWorldViewSessionRequestSchema, { serverId, worldRef }),
				{ ...silentCallOptions, timeoutMs: 10_000 }
			);
			if (!current() || key !== sessionScope || attempt !== sessionAttempt || denied) return;
			if (
				native !== (response.state === RendererState.NATIVE_AVAILABLE) ||
				(!native &&
					response.state !== RendererState.READY &&
					response.state !== RendererState.STALE)
			) {
				sessionError = new Error('The map renderer changed. Reconnecting…');
				void store.loadStatus(statusKey, serverId, requestedRef).catch(() => {});
				return;
			}
			if (response.proxyPath) authStore.refreshBrowserSessionCookie();
			sessionOwner = key;
			session = response;
			sessionExpired = false;
			sessionError = null;
		} catch (error) {
			if (!current() || key !== sessionScope || attempt !== sessionAttempt) return;
			handleError(error);
			sessionError = error;
		}
	}

	async function loadCapabilities() {
		const key = capabilitiesScope;
		if (!key) return;
		const current = scopeGuard();
		const attempt = ++capabilitiesAttempt;
		const stillCurrent = () =>
			current() && key === capabilitiesScope && attempt === capabilitiesAttempt;
		try {
			const response = await rpcClient.worldView.listCapabilities(
				create(ListCapabilitiesRequestSchema, { serverId }),
				silentCallOptions
			);
			if (stillCurrent()) capabilityResult = { key, items: response.capabilities };
		} catch (error) {
			if (!stillCurrent()) return;
			handleError(error);
			capabilityResult = { key, items: isReadDenied(error) ? [] : capabilities, error };
		}
	}

	async function loadOverlays() {
		if (!worldRef) return;
		const current = scopeGuard();
		const attempt = ++overlaysAttempt;
		try {
			const response = await rpcClient.worldView.listMapOverlays(
				create(ListMapOverlaysRequestSchema, { serverId, worldRef, dimension: overlayDimension }),
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
		if (!validLink || !canRead) return;
		const wasAvailable = rendererControlsAvailable;
		const current = scopeGuard();
		const attempt = ++refreshAttempt;
		const [status] = await Promise.allSettled([
			store.loadStatus(statusKey, serverId, requestedRef),
			...(!preview && worldRef ? [loadCapabilities(), loadOverlays()] : [])
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
		if (!enabled || !canRead || !validLink || auth.isLoading) return;
		const key = statusKey;
		const close = store.watchStatus(key, serverId, requestedRef, preview ? 15_000 : 5_000);
		void store.loadStatus(key, serverId, requestedRef).catch(() => {});
		return () => {
			close();
			scopeVersion++;
			store.reset();
			deniedByRead = false;
			clearSession();
			sessionError = null;
			capabilityResult = undefined;
			overlays = [];
			overlaysError = null;
			playerVisible = false;
			visibilityNote = '';
		};
	});

	$effect(() => {
		if (preview || !canRead || !capabilitiesScope) return;
		void loadCapabilities();
	});

	$effect(() => {
		if (preview || !canRead || !worldRef) return;
		void loadOverlays();
		return () => {
			overlaysAttempt++;
			overlays = [];
		};
	});

	$effect(() => {
		if (!enabled || !canRead || !worldRef || denied || !sessionScope) {
			clearSession();
			return;
		}
		void loadSession();
	});

	$effect(() => {
		if (!enabled || !canRead || denied || !sessionScope || !sessionError) return;
		if (sessionOwner === sessionScope && session?.proxyPath) return;
		const retrySession = setTimeout(() => void loadSession(), 5_000);
		return () => clearTimeout(retrySession);
	});

	$effect(() => {
		if (!canRead || denied || sessionOwner !== sessionScope || !session?.proxyPath) return;
		const expiresAt = timestampToMs(session.expiresAt);
		if (expiresAt === null) return;

		// Retry transient renewal failures, but stop serving the old session at its deadline.
		let canceled = false;
		const expire = setTimeout(
			() => {
				sessionExpired = true;
			},
			Math.max(0, expiresAt - Date.now())
		);
		async function renew() {
			await loadSession();
			if (!canceled) refresh = setTimeout(() => void renew(), 5_000);
		}
		let refresh = setTimeout(() => void renew(), Math.max(1_000, expiresAt - Date.now() - 20_000));
		return () => {
			canceled = true;
			clearTimeout(refresh);
			clearTimeout(expire);
		};
	});

	let embed = $derived(
		sessionExpired
			? { src: null, reason: 'The map session expired. Reconnecting…' }
			: evaluateSession(
					!denied &&
						sessionOwner === sessionScope &&
						(view === 'saved' || canMountEmbed(view, session?.proxyPath)),
					session
				)
	);
	let previewTile = $state<{ path: string; scope: string; src: string }>();
	let previewFetching = $state(false);
	$effect(() => {
		const path = embed.src;
		const scope = sessionScope;
		if (!preview || view !== 'saved' || !path) return;
		const controller = new AbortController();
		const { signal } = controller;
		previewFetching = true;
		void (async () => {
			const tile = await loadNativePreview(path, signal);
			if (signal.aborted) return;
			if (tile.status === 'access') {
				handleError(new ConnectError('World map access was revoked.', Code.PermissionDenied));
			} else if (tile.status === 'saved') previewTile = { path, scope, src: tile.src };
		})()
			.catch(() => {})
			.finally(() => {
				if (!signal.aborted) previewFetching = false;
			});
		return () => {
			controller.abort();
			previewFetching = false;
		};
	});
	let previewImage = $derived(
		view === 'saved' &&
			embed.src &&
			previewTile?.path === embed.src &&
			previewTile.scope === sessionScope
			? previewTile.src
			: undefined
	);
	let previewFrame = $derived(view !== 'saved' ? embed.src : null);
	let mapLoadingLabel = $derived.by(() => {
		if (auth.isLoading) return 'Checking map access…';
		if (!enabled || denied || !canRead || statusUnavailable) return '';
		if (view === 'loading')
			return worldKey === undefined ? 'Finding the active world…' : 'Checking the map renderer…';
		if (view === 'installing') return 'Starting the map renderer…';
		if (view === 'rendering') return 'Rendering terrain…';
		if (previewFetching) return 'Reading saved terrain…';
		if (sessionMode && !session && !sessionError)
			return view === 'saved' ? 'Opening saved terrain…' : 'Connecting to the map…';
		return '';
	});
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
		!validLink
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
		const next = !playerVisible;
		if (!worldRef || !canObserve || (next && !matrix.players(canObserve).supported)) return;
		const current = scopeGuard();
		try {
			const response = await rpcClient.worldView.setPlayerVisibility(
				create(SetPlayerVisibilityRequestSchema, { serverId, worldRef, visible: next }),
				silentCallOptions
			);
			if (!current()) return;
			playerVisible = response.visible;
			visibilityNote = next
				? 'Player visibility was enabled for this session. The disclosure was recorded in the activity log.'
				: 'Player visibility was disabled. No player positions are requested now.';
			await loadOverlays();
		} catch (error) {
			// Action-scoped denial must not revoke an otherwise readable world view.
			if (current()) toast.error(formatRpcError(error, 'Could not change player visibility.'));
		}
	}

	// Overlay add/edit is only available with the capability and update permission.
	let overlayModalOpen = $state(false);
	let editingOverlay = $state<MapOverlay | null>(null);
	let formLabel = $state('');
	let formX = $state('');
	let formZ = $state('');
	let formKind = $state('waypoint');
	let overlayDraftDimension = '';
	let overlayScope: (() => boolean) | undefined;

	function openOverlayForm(overlay: MapOverlay | null) {
		overlayScope = scopeGuard();
		overlayDraftDimension = overlay?.dimension || overlayDimension;
		editingOverlay = overlay;
		formLabel = overlay?.label ?? '';
		formX = String(overlay?.x ?? x ?? '');
		formZ = String(overlay?.z ?? z ?? '');
		formKind = overlay?.kind ?? 'waypoint';
		overlayModalOpen = true;
	}

	async function saveOverlay() {
		const current = overlayScope;
		if (!worldRef || !current?.() || !matrix.overlayEditing(canUpdate).supported) return;
		const px = parseNumber(formX);
		const pz = parseNumber(formZ);
		if (px === null || pz === null) {
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
						dimension: overlayDraftDimension,
						kind: formKind,
						x: px,
						z: pz,
						label: formLabel,
						sensitive: editingOverlay?.sensitive ?? false
					}
				}),
				silentCallOptions
			);
			if (!current()) return;
			overlayModalOpen = false;
			await loadOverlays();
			toast.success(editingOverlay ? 'Overlay updated.' : 'Overlay added.');
		} catch (error) {
			// Action-scoped denial must not revoke an otherwise readable world view.
			if (current()) toast.error(formatRpcError(error, 'Could not save the overlay.'));
		}
	}

	// Administration is separated from viewing and uses the existing command RPC.
	let adminActionList = $derived(adminActions(canCommand));
	let confirmAdminOpen = $state(false);
	let pendingAdmin = $state<{
		action: AdminAction;
		request: AdminRequest;
		position: MapPoint | null;
	} | null>(null);
	let adminScope: (() => boolean) | undefined;
	let adminBusy = $state(false);
	let adminError = $state('');

	function requestAdmin(action: AdminAction) {
		if (!action.allowed) return;
		const position = x === null || z === null ? null : { x, z };
		const request = buildAdminRequest(action, serverId, position);
		if (!request) {
			adminError = 'Set both map coordinates before teleporting.';
			return;
		}
		adminScope = scopeGuard();
		pendingAdmin = { action, request, position: action.id === 'teleport' ? position : null };
		adminError = '';
		confirmAdminOpen = true;
	}

	async function confirmAdmin() {
		const current = adminScope;
		if (!pendingAdmin || !canCommand || !current?.()) return;
		const { action, request } = pendingAdmin;
		adminBusy = true;
		adminError = '';
		try {
			const response = await rpcClient.server.sendCommand(
				create(ServerSendCommandRequestSchema, { id: request.serverId, command: request.command }),
				silentCallOptions
			);
			if (!current()) return;
			if (!response.success) {
				adminError = response.error || response.output || 'The server rejected the command.';
				return;
			}
			confirmAdminOpen = false;
			toast.success(`${action.label} was sent to the server.`);
		} catch (error) {
			if (!current()) return;
			// servers/command denial is separate from world_view/read; keep the map usable.
			adminError = isPermissionDenied(error)
				? 'Permission denied: your account cannot run commands on this server. A map session cannot grant this.'
				: formatRpcError(error, 'The command could not be sent.');
		} finally {
			adminBusy = false;
		}
	}

	$effect(() => {
		if (!effectiveWorldKey) return;
		return () => {
			overlayModalOpen = false;
			confirmAdminOpen = false;
			pendingAdmin = null;
			adminError = '';
			playerVisible = false;
			visibilityNote = '';
		};
	});

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

{#if preview}
	<section
		class="relative isolate flex min-h-64 flex-col overflow-hidden border border-border bg-card lg:min-h-72"
		aria-label="World View preview"
		data-testid="server-world-map-card"
	>
		{#if previewImage}
			<img
				src={previewImage}
				alt=""
				class="absolute inset-0 h-full w-full object-cover [image-rendering:pixelated]"
			/>
		{:else if previewFrame}
			<!-- Crop the renderer's toolbar from the passive preview. The dialog uses its full viewport. -->
			<div inert aria-hidden="true" class="pointer-events-none absolute -inset-16">
				<iframe
					src={previewFrame}
					title="World renderer preview"
					tabindex="-1"
					class="h-full w-full border-0"
					sandbox="allow-scripts allow-same-origin allow-forms"
					loading="lazy"
				></iframe>
			</div>
		{/if}
		{#if previewImage || previewFrame}
			<div
				class="pointer-events-none absolute inset-0 bg-linear-to-b from-black/15 via-transparent to-black/20"
			></div>
		{/if}
		<div
			class="relative flex flex-1 flex-col justify-between gap-4 p-4 sm:p-5"
			class:text-white={Boolean(previewImage || previewFrame)}
		>
			<div class="flex items-start justify-between gap-2">
				<h2
					class={[
						'text-sm font-semibold',
						(previewImage || previewFrame) && 'bg-black/70 px-2 py-1 text-white'
					]}
				>
					World View
				</h2>
				{#if canRead && !denied}
					<div class="flex gap-1.5">
						<CarbonButton
							kind="secondary"
							size="sm"
							iconOnly
							aria-label="Refresh map preview"
							title="Refresh map preview"
							onclick={() => void retry()}
						>
							<RefreshCw class="h-4 w-4" aria-hidden="true" />
						</CarbonButton>
						<CarbonButton
							kind="secondary"
							size="sm"
							class="gap-2"
							aria-label="Expand world map"
							title="Expand world map"
							onclick={onexpand}
						>
							<Maximize class="h-4 w-4" aria-hidden="true" />
							<span>Expand</span>
						</CarbonButton>
					</div>
				{/if}
			</div>
			{#if !previewImage && !previewFrame && mapLoadingLabel}
				<WorldMapLoading label={mapLoadingLabel} compact />
			{:else if !previewImage && !previewFrame}
				<div class="flex flex-1 items-center gap-2 text-sm text-muted-foreground" role="status">
					<MapPin class="h-4 w-4 shrink-0" aria-hidden="true" />
					{#if !canRead || denied}
						World View access required
					{:else if view === 'onboarding'}
						Expand to set up a map
					{:else if view === 'saved'}
						Expand to explore saved terrain
					{:else}
						Preview unavailable
					{/if}
				</div>
			{/if}
			{#if canRead && !denied && statusTarget}
				{@const connecting = view === 'ready' && Boolean(mapLoadingLabel)}
				{@const lastKnown = refreshWarning && Boolean(previewImage || previewFrame)}
				<div class="flex flex-wrap items-end justify-between gap-2">
					{#if statusTarget.name}<p
							class={[
								'min-w-0 text-base font-semibold break-words',
								(previewImage || previewFrame) && 'bg-black/70 px-2 py-1 text-white'
							]}
						>
							{statusTarget.name}
						</p>{/if}
					{#if view !== 'saved' || lastKnown}
						<CarbonTag
							type={lastKnown ? 'yellow' : connecting ? 'blue' : VIEW_STATE_TONE[view]}
							size="sm"
						>
							{lastKnown ? 'LAST KNOWN' : connecting ? 'CONNECTING' : VIEW_STATE_LABEL[view]}
						</CarbonTag>
					{/if}
				</div>
			{/if}
		</div>
	</section>
{:else}
	<div
		class="min-w-0 flex-1 space-y-4 bg-background font-sans text-foreground"
		data-testid="showcase-world-map-content"
	>
		{#if presentation === 'page'}
			<CarbonBreadcrumbs
				items={[{ label: 'World View', href: worldListHref }, { label: worldTitle }]}
			/>
		{/if}

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
				{#if presentation === 'page'}
					<CarbonButton kind="tertiary" size="sm" href={worldListHref} class="gap-2">
						<ArrowLeft class="h-3.5 w-3.5" />
						<span>{canBrowseAll ? 'All worlds' : 'Server maps'}</span>
					</CarbonButton>
				{/if}
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
			<div class="border border-border bg-card">
				<WorldMapLoading label={mapLoadingLabel} />
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
		{:else if !validLink}
			<CarbonErrorState
				title="Invalid world link"
				message="This URL does not carry a recognisable world reference. Return to the world picker and open the world again."
			/>
		{:else if !worldRef && !statusUnavailable}
			{#if statusTarget}
				<CarbonErrorState
					title="Active world unavailable"
					message="The server did not report a world that can be opened."
					onretry={retry}
				/>
			{:else}
				<div class="border border-border bg-card"><WorldMapLoading label={mapLoadingLabel} /></div>
			{/if}
		{:else}
			<div
				class={[
					'grid min-w-0 gap-4',
					rendererControlsAvailable && view !== 'saved' && 'lg:grid-cols-[minmax(0,1fr)_20rem]'
				]}
			>
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
					{:else if view === 'loading' || view === 'installing' || view === 'rendering'}
						<div class="border border-border bg-card">
							<WorldMapLoading label={mapLoadingLabel} />
						</div>
					{:else if view === 'onboarding'}
						<div class="space-y-3 border border-border bg-card p-4 sm:p-6">
							<h2 class="text-base font-semibold">Set up a map</h2>
							<p class="max-w-xl text-sm text-muted-foreground">
								{statusTarget?.freshness?.source === 'worldview.discovered.bluemap'
									? statusTarget.freshness.note
									: 'Connect a renderer to see this world in Carbon Panel.'}
							</p>
							{#if statusTarget?.freshness?.source !== 'worldview.discovered.bluemap'}
								<BlueMapInstall
									{serverId}
									target={statusTarget}
									can={canManageModules}
									oncomplete={() => void retry()}
								/>
							{:else if canManageModules('modules', 'read')}
								<CarbonButton kind="secondary" size="sm" href={modulesHref}
									>Open Modules</CarbonButton
								>
							{/if}
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
								{statusTarget?.error ||
									rendererName(capabilitiesRenderer) + ' has no verified map adapter.'}
							</p>
						</div>
					{:else if view === 'stopped'}
						<div class="space-y-3 border border-border bg-card p-8">
							<div class="flex items-center gap-2">
								<AlertTriangle class="h-5 w-5 text-warning" />
								<h2 class="text-base font-semibold">Renderer stopped</h2>
							</div>
							<p class="text-sm text-muted-foreground">
								{statusTarget?.freshness?.note || 'The renderer has stopped serving tiles.'}
							</p>
							{#if statusTarget?.freshness?.source === 'worldview.discovered.bluemap' && canManageModules('modules', 'read')}
								<CarbonButton kind="secondary" size="sm" href={modulesHref}
									>Open Modules</CarbonButton
								>
							{/if}
						</div>
					{:else if view === 'saved'}
						<div class="space-y-2 border border-border bg-card px-4 py-3 text-sm">
							<p class="text-muted-foreground">
								Refresh after Minecraft saves to see new builds. This map does not track moving
								players.
							</p>
							{#if statusTarget?.error}
								<p class="text-muted-foreground">{statusTarget.error}</p>
								{#if canManageModules('modules', 'read')}
									<CarbonButton kind="tertiary" size="sm" href={modulesHref}
										>Open Modules</CarbonButton
									>
								{/if}
							{:else if canManageModules('modules', 'create') && canManageModules('modules', 'start')}
								{#key statusKey}
									<details
										ontoggle={(event) => {
											if (event.currentTarget.open) blueMapSetupKey = statusKey;
										}}
									>
										<summary class="focus-ring w-fit cursor-pointer text-accent-text"
											>Set up a 3D map with BlueMap</summary
										>
										{#if blueMapSetupKey === statusKey}
											<div class="mt-3 space-y-3">
												<p class="text-muted-foreground">
													The managed BlueMap module renders textured buildings and terrain from
													saved chunks. Player positions require a separate live feed.
												</p>
												<BlueMapInstall
													{serverId}
													target={statusTarget}
													can={canManageModules}
													oncomplete={() => void retry()}
												/>
											</div>
										{/if}
									</details>
								{/key}
							{/if}
						</div>
						{#if embed.src}
							{#key statusKey}
								<NativeMapViewport
									proxyPath={embed.src}
									title="World terrain"
									preservePositionOnRenewal
									location={nativeLocation}
									markers={nativeMarkers}
									markersUnavailable={Boolean(overlaysError)}
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
							<div class="border border-border bg-card">
								<WorldMapLoading label={mapLoadingLabel} />
							</div>
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
						{:else if mapLoadingLabel}
							<div class="border border-border bg-card">
								<WorldMapLoading label={mapLoadingLabel} />
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

				<aside
					class="min-w-0 space-y-3"
					class:hidden={!rendererControlsAvailable && !canCommand && !capabilitiesError}
				>
					{#if capabilitiesError}
						<p class="border-l-4 border-warning bg-warning/10 p-2 text-xs" role="alert">
							Could not check map controls. Use Refresh to retry.
						</p>
					{/if}
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
												<span
													class={mode.supported ? 'text-success-text' : 'text-muted-foreground'}
												>
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

						{#if view !== 'saved'}
							<CarbonAccordionItem title="Overlays" bind:open={overlayControlsOpen}>
								<div class="space-y-3">
									{#if !playerVisible && canObserve && matrix.players(true).supported}
										<CarbonButton kind="tertiary" size="sm" onclick={togglePlayers}>
											<Users class="h-3.5 w-3.5" />
											<span>Show player markers</span>
										</CarbonButton>
									{:else if playerVisible && canObserve}
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
						{/if}

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
								{#if adminError}
									<p
										class="border-l-2 border-destructive bg-destructive/10 p-3 text-sm text-foreground"
										role="alert"
									>
										{adminError}
									</p>
								{/if}
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
		title={pendingAdmin?.action.label ?? 'Run command'}
		message={(pendingAdmin?.action.consequence ?? '') +
			(pendingAdmin?.position
				? ` X ${pendingAdmin.position.x}, Z ${pendingAdmin.position.z}.`
				: '')}
		confirmLabel="Run command"
		danger={pendingAdmin?.action.id === 'pause-autosave'}
		confirming={adminBusy}
		onconfirm={confirmAdmin}
		onclose={() => {
			adminError = '';
		}}
	/>
{/if}
