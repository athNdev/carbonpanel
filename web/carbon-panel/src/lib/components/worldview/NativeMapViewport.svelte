<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import { untrack } from 'svelte';
	import {
		Crosshair,
		Copy,
		Flag,
		Landmark,
		MapPin,
		Minus,
		Plus,
		RefreshCw,
		X
	} from '@lucide/svelte';
	import CarbonAccordion from '$lib/components/carbon/CarbonAccordion.svelte';
	import CarbonAccordionItem from '$lib/components/carbon/CarbonAccordionItem.svelte';
	import CarbonButton from '$lib/components/carbon/CarbonButton.svelte';
	import CarbonErrorState from '$lib/components/carbon/CarbonErrorState.svelte';
	import WorldMapLoading from './WorldMapLoading.svelte';
	import CarbonSelect from '$lib/components/carbon/CarbonSelect.svelte';
	import CarbonTextInput from '$lib/components/carbon/CarbonTextInput.svelte';
	import {
		blockScale,
		CHUNKS_PER_TILE,
		clampBlock,
		loadNativeManifest,
		MAP_SPANS,
		NATIVE_FAILURE_MESSAGE,
		nativeDimensionLabel,
		parseNativeCoordinate,
		savedAgeLabel,
		TILE_BLOCKS,
		tileKey,
		visibleNativeTiles,
		type ManifestResult,
		type MapPoint,
		type MapSize,
		type NativeMapFailure,
		type TileResult
	} from '$lib/features/worldview/native-map';
	import { createNativeTileLoader } from '$lib/features/worldview/native-map-loader';
	import {
		projectNativeMapPoint,
		type NativeMapMarker,
		type NativeMarkerKind
	} from '$lib/features/worldview/native-map-markers';
	import { overlayKindLabel } from '$lib/features/worldview/overlays';

	let {
		proxyPath,
		title,
		preservePositionOnRenewal = false,
		onReopen,
		location = null,
		markers = [],
		markersUnavailable = false
	}: {
		proxyPath: string;
		title: string;
		preservePositionOnRenewal?: boolean;
		onReopen?: () => void;
		location?: MapPoint | null;
		markers?: readonly NativeMapMarker[];
		markersUnavailable?: boolean;
	} = $props();
	const id = $props.id();
	const markerIcons = { marker: MapPin, waypoint: Flag, poi: Landmark };
	const markerInset = 22;
	const mostImportantFailure: NativeMapFailure[] = [
		'access',
		'unsupported',
		'changed',
		'busy',
		'unavailable'
	];
	const initialZoom = 2;
	const maximumZoom = MAP_SPANS.length - 1;
	const formatTime = new Intl.DateTimeFormat(undefined, {
		dateStyle: 'medium',
		timeStyle: 'short'
	});

	let center = $state<MapPoint>({ x: 0, z: 0 });
	let size = $state<MapSize>({ width: 0, height: 0 });
	let zoom = $state(initialZoom);
	let spanChoice = $state('auto');
	let xInput = $state('');
	let zInput = $state('');
	let coordinateError = $state({ x: '', z: '' });
	let copyNote = $state('');
	let revision = $state(0);
	let source = $state.raw<{ path: string; revision: number; result: ManifestResult } | undefined>();
	let tiles = $state.raw<ReadonlyMap<string, TileResult>>(new Map());
	let loader = $state.raw<ReturnType<typeof createNativeTileLoader> | undefined>();
	let dragging = $state(false);
	let showMarkers = $state(true);
	let selectedMarkerId = $state('');
	let viewport = $state<HTMLDivElement>();
	let markerTrigger: HTMLButtonElement | undefined;
	// Live keeps complete tiles revalidating against the region file mtime;
	// off is the classic behavior (manual refresh only). Live is the default:
	// the map reads the server's live world directory.
	let live = $state(true);
	let now = $state(Date.now());
	let activeDimension = $state('overworld');
	let selectedMarker = $derived(markers.find((marker) => marker.id === selectedMarkerId));
	let positionedPath: string | undefined;
	let drag: { pointer: number; x: number; y: number; center: MapPoint } | undefined;

	let result = $derived(
		source?.path === proxyPath && source.revision === revision ? source.result : undefined
	);
	let spawn = $derived(result?.status === 'ready' ? result.manifest.spawn : null);
	let dimensions = $derived(
		result?.status === 'ready' ? result.manifest.dimensions : ['overworld']
	);
	let scale = $derived(blockScale(size, zoom));
	let visible = $derived(visibleNativeTiles(center, size, scale));
	let block = $derived({ x: Math.floor(center.x), z: Math.floor(center.z) });
	// Markers and overlays are Overworld observations; other dimensions have
	// no honest placement for them.
	let visiblePins = $derived(
		showMarkers && activeDimension === 'overworld'
			? markers
					.map((marker) => ({ marker, ...projectNativeMapPoint(marker, center, size, scale) }))
					.filter(
						({ left, top }) =>
							left >= markerInset &&
							left <= size.width - markerInset &&
							top >= markerInset &&
							top <= size.height - markerInset
					)
			: []
	);

	$effect(() => {
		const path = proxyPath;
		const generation = revision;
		const abort = new AbortController();
		source = undefined;
		loader = undefined;
		tiles = new Map();
		copyNote = '';
		drag = undefined;
		dragging = false;
		activeDimension = 'overworld';
		void loadNativeManifest(path, abort.signal)
			.catch((): ManifestResult => ({ status: 'unavailable' }))
			.then((manifest) => {
				if (abort.signal.aborted) return;
				source = { path, revision: generation, result: manifest };
				if (manifest.status !== 'ready') return;
				if (!positionedPath || (!preservePositionOnRenewal && positionedPath !== path)) {
					positionedPath = path;
					center = location ?? manifest.manifest.spawn ?? { x: 0, z: 0 };
					zoom = initialZoom;
					spanChoice = 'auto';
				}
			});
		return () => {
			abort.abort();
		};
	});

	// The tile loader is keyed on the session path and the active dimension;
	// switching dimensions keeps the camera and reloads terrain. Seeding
	// liveness is untracked so this effect never depends on `live`: toggling
	// the checkbox must only change the cadence through the dedicated effect
	// below, never dispose the loader or invalidate its tile cache.
	$effect(() => {
		const path = proxyPath;
		const generation = revision;
		const dimension = activeDimension;
		if (source?.path !== path || source.revision !== generation) return;
		if (source.result.status !== 'ready') return;
		const tileLoader = createNativeTileLoader(path, dimension, (results) => (tiles = results));
		loader = tileLoader;
		tiles = new Map();
		untrack(() => tileLoader.setLive(live));
		return () => {
			if (loader === tileLoader) loader = undefined;
			tileLoader.dispose();
		};
	});

	$effect(() => {
		loader?.setVisible(visible);
	});

	$effect(() => {
		loader?.setLive(live);
	});

	// The staleness age only needs minute granularity; tick while live so the
	// indicator keeps up with revalidations without its own timer pressure
	// when the map is closed or frozen.
	$effect(() => {
		if (!live) return;
		const timer = setInterval(() => (now = Date.now()), 30_000);
		return () => clearInterval(timer);
	});

	// A small spawn save stays legible. Open up one step when the first
	// viewport has continuous saved coverage. Manual zoom always takes priority.
	$effect(() => {
		if (spanChoice !== 'auto' || zoom !== initialZoom || !visible.length) return;
		if (
			visible.every((tile) => {
				const result = tiles.get(tileKey(tile));
				return result?.status === 'saved' && result.chunks === CHUNKS_PER_TILE;
			})
		)
			zoom = initialZoom - 1;
	});

	$effect(() => {
		if (location) moveTo(location.x, location.z);
	});

	let summary = $derived.by(() => {
		const states = visible.map((tile) => tiles.get(tileKey(tile)));
		const saved = states.filter((tile) => tile?.status === 'saved');
		const loading = states.length === 0 || states.some((tile) => !tile);
		const failure = mostImportantFailure.find((status) =>
			states.some((tile) => tile?.status === status)
		);
		const partial = saved.some((tile) => tile.chunks === null || tile.chunks < CHUNKS_PER_TILE);
		const provisional = saved.some((tile) => tile.provisionalChunks > 0);
		const savedAt = saved
			.map((tile) => tile.savedAt)
			.filter((time) => time !== null)
			.sort((a, b) => Date.parse(b) - Date.parse(a))[0];
		let message = 'All visible chunks have terrain.';
		if (loading) {
			message = 'Loading saved terrain…';
		} else if (failure) {
			message = NATIVE_FAILURE_MESSAGE[failure];
		} else if (saved.length === 0) {
			message = 'Explore this area in Minecraft. The map checks for new saves automatically.';
		} else if (partial || saved.length < states.length) {
			message =
				'Blank areas have no readable terrain yet. The map checks for new saves automatically.';
		}
		return {
			loading,
			failure,
			savedAt,
			message,
			provisional,
			empty: !loading && !failure && !saved.length
		};
	});

	function moveTo(x: number, z: number) {
		center = { x: clampBlock(x), z: clampBlock(z) };
		copyNote = '';
	}

	function changeZoom(direction: number) {
		zoom = Math.min(maximumZoom, Math.max(0, zoom + direction));
		spanChoice = String(MAP_SPANS[zoom]);
	}

	function setMapSpan(value: string) {
		spanChoice = value;
		zoom = value === 'auto' ? initialZoom : MAP_SPANS.findIndex((span) => String(span) === value);
	}

	function jumpToCoordinates(event: SubmitEvent) {
		event.preventDefault();
		const x = parseNativeCoordinate(xInput);
		const z = parseNativeCoordinate(zInput);
		const invalid = 'Enter a whole number from −30,000,000 to 29,999,999.';
		coordinateError = { x: x === null ? invalid : '', z: z === null ? invalid : '' };
		if (x === null || z === null) return;
		moveTo(x, z);
	}

	async function copyCoordinates() {
		try {
			await navigator.clipboard.writeText(block.x + ', ' + block.z);
			copyNote = 'Coordinates copied.';
		} catch {
			copyNote = 'Clipboard unavailable. Select the coordinates to copy them.';
		}
	}

	function keyPan(event: KeyboardEvent) {
		if (event.target !== event.currentTarget) return;
		if (event.ctrlKey || event.altKey || event.metaKey || (event.shiftKey && event.key !== '+'))
			return;
		const step = Math.max(4, Math.round(48 / scale));
		switch (event.key) {
			case 'ArrowLeft':
				moveTo(center.x - step, center.z);
				break;
			case 'ArrowRight':
				moveTo(center.x + step, center.z);
				break;
			case 'ArrowUp':
				moveTo(center.x, center.z - step);
				break;
			case 'ArrowDown':
				moveTo(center.x, center.z + step);
				break;
			case '+':
			case '=':
				changeZoom(1);
				break;
			case '-':
				changeZoom(-1);
				break;
			default:
				return;
		}
		event.preventDefault();
	}

	function startPan(event: PointerEvent) {
		if (drag || event.button !== 0) return;
		if (event.target instanceof Element && event.target.closest('button')) return;
		const element = event.currentTarget as HTMLElement;
		element.setPointerCapture(event.pointerId);
		drag = { pointer: event.pointerId, x: event.clientX, y: event.clientY, center: { ...center } };
		dragging = true;
	}

	function continuePan(event: PointerEvent) {
		if (drag?.pointer !== event.pointerId) return;
		moveTo(
			drag.center.x - (event.clientX - drag.x) / scale,
			drag.center.z - (event.clientY - drag.y) / scale
		);
	}

	function endPan(event: PointerEvent) {
		if (drag?.pointer !== event.pointerId) return;
		drag = undefined;
		dragging = false;
	}

	function retryMap() {
		const failure = result?.status === 'ready' ? summary.failure : result?.status;
		if (onReopen && (failure === 'access' || failure === 'changed')) {
			onReopen();
		} else if (result?.status === 'ready') {
			loader?.refresh();
		} else {
			revision += 1;
		}
	}

	function selectMarker(markerId: string, event: MouseEvent) {
		markerTrigger = event.currentTarget as HTMLButtonElement;
		selectedMarkerId = markerId;
	}

	function focusMarker(marker: NativeMapMarker, event: MouseEvent) {
		showMarkers = true;
		selectMarker(marker.id, event);
		moveTo(marker.x, marker.z);
		viewport?.focus();
	}

	function closeMarkerDetails() {
		selectedMarkerId = '';
		(markerTrigger?.isConnected ? markerTrigger : viewport)?.focus();
	}

	function markerName(marker: NativeMapMarker): string {
		return overlayKindLabel(marker.kind) + ': ' + marker.label;
	}

	function tilePixel(block: number, centerBlock: number, viewport: number): number {
		return Math.round(viewport / 2 + (block * TILE_BLOCKS - centerBlock) * scale);
	}
</script>

{#snippet kindIcon(kind: NativeMarkerKind)}
	{@const Icon = markerIcons[kind]}
	<Icon class="h-4 w-4 shrink-0" aria-hidden="true" />
{/snippet}

<section class="min-w-0 border border-border bg-card" aria-labelledby={id + '-title'}>
	<header class="flex flex-wrap items-start justify-between gap-3 border-b border-border px-4 py-3">
		<div class="min-w-0">
			<h2 id={id + '-title'} class="text-sm font-semibold text-foreground">{title}</h2>
			<p class="mt-1 text-xs text-muted-foreground">{nativeDimensionLabel(activeDimension)}</p>
		</div>
		{#if result?.status === 'ready'}
			<div role="group" aria-label="Map zoom and position" class="flex flex-wrap items-end gap-1">
				{#if dimensions.length > 1}
					<CarbonSelect
						label="Dimension"
						value={activeDimension}
						onchange={(event) => (activeDimension = event.currentTarget.value)}
					>
						{#each dimensions as dimension (dimension)}
							<option value={dimension}>{nativeDimensionLabel(dimension)}</option>
						{/each}
					</CarbonSelect>
				{/if}
				<CarbonSelect
					label="Map span"
					value={spanChoice}
					onchange={(event) => setMapSpan(event.currentTarget.value)}
				>
					<option value="auto"
						>{spanChoice === 'auto' ? `Auto (${MAP_SPANS[zoom]} blocks)` : 'Auto'}</option
					>
					{#each MAP_SPANS as span (span)}
						<option value={String(span)}>{span.toLocaleString()} blocks</option>
					{/each}
				</CarbonSelect>
				<CarbonButton
					kind="secondary"
					size="md"
					iconOnly
					aria-label="Zoom out"
					disabled={zoom === 0}
					onclick={() => changeZoom(-1)}
				>
					<Minus class="h-4 w-4" aria-hidden="true" />
				</CarbonButton>
				<CarbonButton
					kind="secondary"
					size="md"
					iconOnly
					aria-label="Zoom in"
					disabled={zoom === maximumZoom}
					onclick={() => changeZoom(1)}
				>
					<Plus class="h-4 w-4" aria-hidden="true" />
				</CarbonButton>
				{#if spawn}
					<CarbonButton
						kind="tertiary"
						size="md"
						class="gap-2"
						onclick={() => spawn && moveTo(spawn.x, spawn.z)}
					>
						<Crosshair class="h-4 w-4" aria-hidden="true" />
						<span>World spawn</span>
					</CarbonButton>
				{/if}
			</div>
			<form
				onsubmit={jumpToCoordinates}
				class="flex min-w-0 basis-full flex-wrap items-end gap-2"
				aria-label="Jump to coordinates"
			>
				<div class="grid max-w-[17rem] min-w-0 flex-[1_1_12rem] grid-cols-2 gap-2">
					<CarbonTextInput
						label="X coordinate"
						bind:value={xInput}
						error={coordinateError.x}
						maxlength={9}
					/>
					<CarbonTextInput
						label="Z coordinate"
						bind:value={zInput}
						error={coordinateError.z}
						maxlength={9}
					/>
				</div>
				<CarbonButton type="submit" kind="secondary" size="md">Jump</CarbonButton>
			</form>
		{/if}
	</header>

	{#if !result}
		<WorldMapLoading label="Loading saved terrain…" />
	{:else if result.status !== 'ready'}
		<CarbonErrorState
			class="border-0"
			title="Map unavailable"
			message={NATIVE_FAILURE_MESSAGE[result.status]}
			onretry={result.status === 'access' || result.status === 'unsupported' ? undefined : retryMap}
		/>
	{:else}
		<div
			role="application"
			tabindex="0"
			aria-label={title}
			aria-describedby={id + '-controls'}
			class="map-viewport focus-ring relative isolate w-full touch-none overflow-hidden bg-background select-none"
			class:dragging
			bind:this={viewport}
			bind:clientWidth={size.width}
			bind:clientHeight={size.height}
			onkeydown={keyPan}
			onpointerdown={startPan}
			onpointermove={continuePan}
			onpointerup={endPan}
			onpointercancel={endPan}
			onlostpointercapture={endPan}
		>
			{#each visible as tile (tileKey(tile))}
				{@const saved = tiles.get(tileKey(tile))}
				{@const left = tilePixel(tile.x, center.x, size.width)}
				{@const top = tilePixel(tile.z, center.z, size.height)}
				<div
					class="tile pointer-events-none absolute overflow-hidden"
					style:left={left + 'px'}
					style:top={top + 'px'}
					style:width={tilePixel(tile.x + 1, center.x, size.width) - left + 'px'}
					style:height={tilePixel(tile.z + 1, center.z, size.height) - top + 'px'}
					aria-hidden="true"
				>
					{#if saved?.status === 'saved'}
						<img src={saved.src} alt="" class="size-full" draggable="false" />
					{/if}
				</div>
			{/each}
			<div class="pointer-events-none absolute inset-0 grid place-items-center" aria-hidden="true">
				<Crosshair class="map-cursor h-5 w-5" strokeWidth={2} />
				{#if summary.empty}
					<div
						class="absolute top-1/2 mt-6 max-w-[85%] border border-border bg-card px-3 py-2 text-center text-sm text-foreground"
					>
						No terrain available at these coordinates
					</div>
				{/if}
			</div>
			{#each visiblePins as { marker, left, top } (marker.id)}
				<button
					type="button"
					class="focus-ring absolute z-10 grid size-10 -translate-x-1/2 -translate-y-1/2 cursor-pointer place-items-center border border-border bg-card text-foreground shadow-sm hover:bg-accent aria-pressed:border-primary aria-pressed:bg-primary aria-pressed:text-primary-foreground"
					style:left={left + 'px'}
					style:top={top + 'px'}
					aria-label={markerName(marker)}
					aria-pressed={selectedMarkerId === marker.id}
					title={markerName(marker)}
					onclick={(event) => selectMarker(selectedMarkerId === marker.id ? '' : marker.id, event)}
				>
					{@render kindIcon(marker.kind)}
				</button>
			{/each}
		</div>
		{#if selectedMarker && showMarkers}
			<div
				class="flex flex-wrap items-start justify-between gap-2 border-t border-border bg-background px-4 py-3"
			>
				<div role="status" class="flex min-w-0 items-start gap-2">
					{@render kindIcon(selectedMarker.kind)}
					<div class="min-w-0">
						<h3 class="text-sm font-semibold break-words text-foreground">
							{selectedMarker.label}
						</h3>
						<p class="mt-1 text-xs break-words text-muted-foreground">
							{overlayKindLabel(selectedMarker.kind)} ·
							<code>X: {selectedMarker.x} · Z: {selectedMarker.z}</code>
						</p>
					</div>
				</div>
				<CarbonButton
					kind="ghost"
					size="sm"
					iconOnly
					aria-label="Close marker details"
					onclick={closeMarkerDetails}
				>
					<X class="h-4 w-4" aria-hidden="true" />
				</CarbonButton>
			</div>
		{/if}
		<div class="space-y-3 border-t border-border px-4 py-3">
			<div class="flex min-w-0 flex-wrap items-center justify-between gap-2">
				<div class="min-w-0 text-xs text-muted-foreground">
					<p role="status">{summary.message}</p>
					{#if summary.empty}
						<p class="mt-1">
							If Chunky is installed, you can also
							<a
								class="focus-ring text-accent-text underline"
								href="https://github.com/pop4959/Chunky/wiki/Pregeneration"
								target="_blank"
								rel="noreferrer">pregenerate this area from the server console</a
							>.
						</p>
					{/if}
					{#if summary.provisional}
						<p class="mt-1">Faded terrain shows saved heights from chunks still generating.</p>
					{/if}
					{#if markers.length && activeDimension !== 'overworld'}
						<p class="mt-1">Markers are only placed on the Overworld map.</p>
					{/if}
					{#if summary.savedAt}
						<p class="mt-1">
							Latest chunk save in this view <time datetime={summary.savedAt}
								>{formatTime.format(new Date(summary.savedAt))}</time
							>{#if savedAgeLabel(summary.savedAt, now)}
								· saved {savedAgeLabel(summary.savedAt, now)}
							{/if}
						</p>
					{/if}
				</div>
				<div class="flex flex-wrap items-center gap-3">
					<label class="flex cursor-pointer items-center gap-2 text-xs text-foreground">
						<input
							type="checkbox"
							bind:checked={live}
							class="focus-ring size-4 cursor-pointer"
							data-testid="native-map-live"
						/>
						Live updates
					</label>
					{#if summary.failure !== 'access' && summary.failure !== 'unsupported' && !summary.loading}
						<CarbonButton kind="tertiary" size="sm" class="gap-2" onclick={retryMap}>
							<RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
							<span>{summary.failure ? 'Retry terrain' : 'Refresh terrain'}</span>
						</CarbonButton>
					{/if}
				</div>
			</div>

			<p id={id + '-controls'} class="text-xs text-muted-foreground">
				Drag or use the arrow keys to pan. Use + and − to zoom.
			</p>
			<div class="flex min-w-0 flex-wrap items-end justify-between gap-3">
				<div class="flex min-w-0 flex-wrap items-center gap-2">
					<code class="text-xs text-foreground" data-testid="native-map-coordinates"
						>X: {block.x} · Z: {block.z}</code
					>
					<CarbonButton kind="tertiary" size="md" class="gap-2" onclick={copyCoordinates}>
						<Copy class="h-4 w-4" aria-hidden="true" />
						<span>Copy coordinates</span>
					</CarbonButton>
				</div>
			</div>
			{#if copyNote}
				<p role="status" class="text-xs text-muted-foreground">{copyNote}</p>
			{/if}
		</div>
		{#if markersUnavailable}
			<p role="status" class="border-t border-border px-4 py-3 text-xs text-muted-foreground">
				{markers.length
					? 'Could not refresh map markers. Showing the last known markers.'
					: 'Map markers are unavailable.'}
			</p>
		{/if}
		{#if markers.length}
			<CarbonAccordion>
				<CarbonAccordionItem title={'Map markers (' + markers.length + ')'}>
					<label class="mb-3 flex items-center gap-2 text-xs text-foreground">
						<input
							type="checkbox"
							bind:checked={showMarkers}
							class="focus-ring size-4 cursor-pointer"
						/>
						Show markers on map
					</label>
					<ul
						class="max-h-56 divide-y divide-border overflow-auto"
						aria-label="Map marker locations"
					>
						{#each markers as marker (marker.id)}
							<li>
								<button
									type="button"
									class="focus-ring flex w-full min-w-0 cursor-pointer items-start gap-2 px-2 py-2 text-left text-sm hover:bg-accent"
									aria-label={'Center map on ' + markerName(marker)}
									onclick={(event) => focusMarker(marker, event)}
								>
									{@render kindIcon(marker.kind)}
									<span class="min-w-0 break-words">
										<span class="block text-foreground">{marker.label}</span>
										<span class="block text-xs text-muted-foreground">
											{overlayKindLabel(marker.kind)} · <code>X: {marker.x} · Z: {marker.z}</code>
										</span>
									</span>
								</button>
							</li>
						{/each}
					</ul>
				</CarbonAccordionItem>
			</CarbonAccordion>
		{/if}
	{/if}
</section>

<style>
	.map-viewport {
		block-size: clamp(17rem, 50vh, 37rem);
		cursor: grab;
	}
	.map-viewport.dragging {
		cursor: grabbing;
	}
	.map-viewport:focus-visible {
		outline-offset: -3px;
	}
	.tile {
		box-shadow: inset -1px -1px var(--border);
	}
	.tile img {
		image-rendering: pixelated;
	}
	.map-viewport :global(.map-cursor) {
		color: var(--foreground);
		filter: drop-shadow(0 0 2px var(--background));
	}
</style>
