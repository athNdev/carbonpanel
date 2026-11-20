<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import { Crosshair, Copy, Minus, Plus, RefreshCw } from '@lucide/svelte';
	import CarbonButton from '$lib/components/carbon/CarbonButton.svelte';
	import CarbonErrorState from '$lib/components/carbon/CarbonErrorState.svelte';
	import CarbonInlineLoading from '$lib/components/carbon/CarbonInlineLoading.svelte';
	import CarbonTextInput from '$lib/components/carbon/CarbonTextInput.svelte';
	import {
		blockScale,
		CHUNKS_PER_TILE,
		clampBlock,
		loadNativeManifest,
		NATIVE_FAILURE_MESSAGE,
		parseNativeCoordinate,
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

	let {
		proxyPath,
		title,
		preservePositionOnRenewal = false,
		onReopen
	}: {
		proxyPath: string;
		title: string;
		preservePositionOnRenewal?: boolean;
		onReopen?: () => void;
	} = $props();
	const id = $props.id();
	const mostImportantFailure: NativeMapFailure[] = [
		'access',
		'unsupported',
		'changed',
		'busy',
		'unavailable'
	];
	const initialZoom = 1;
	const maximumZoom = 3;
	const formatTime = new Intl.DateTimeFormat(undefined, {
		dateStyle: 'medium',
		timeStyle: 'short'
	});

	let center = $state<MapPoint>({ x: 0, z: 0 });
	let size = $state<MapSize>({ width: 0, height: 0 });
	let zoom = $state(initialZoom);
	let xInput = $state('');
	let zInput = $state('');
	let coordinateError = $state({ x: '', z: '' });
	let copyNote = $state('');
	let revision = $state(0);
	let source = $state.raw<{ path: string; revision: number; result: ManifestResult } | undefined>();
	let tiles = $state.raw<ReadonlyMap<string, TileResult>>(new Map());
	let loader = $state.raw<ReturnType<typeof createNativeTileLoader> | undefined>();
	let dragging = $state(false);
	let positionedPath: string | undefined;
	let drag: { pointer: number; x: number; y: number; center: MapPoint } | undefined;

	let result = $derived(
		source?.path === proxyPath && source.revision === revision ? source.result : undefined
	);
	let spawn = $derived(result?.status === 'ready' ? result.manifest.spawn : null);
	let scale = $derived(blockScale(size, zoom));
	let visible = $derived(visibleNativeTiles(center, size, scale));
	let block = $derived({ x: Math.floor(center.x), z: Math.floor(center.z) });

	$effect(() => {
		const path = proxyPath;
		const generation = revision;
		const abort = new AbortController();
		let tileLoader: typeof loader;
		source = undefined;
		loader = undefined;
		tiles = new Map();
		copyNote = '';
		drag = undefined;
		dragging = false;
		void loadNativeManifest(path, abort.signal)
			.catch((): ManifestResult => ({ status: 'unavailable' }))
			.then((manifest) => {
				if (abort.signal.aborted) return;
				source = { path, revision: generation, result: manifest };
				if (manifest.status !== 'ready') return;
				if (!positionedPath || (!preservePositionOnRenewal && positionedPath !== path)) {
					positionedPath = path;
					center = manifest.manifest.spawn ?? { x: 0, z: 0 };
					zoom = initialZoom;
				}
				tileLoader = createNativeTileLoader(path, (results) => (tiles = results));
				loader = tileLoader;
			});
		return () => {
			abort.abort();
			tileLoader?.dispose();
		};
	});

	$effect(() => {
		loader?.setVisible(visible);
	});

	let summary = $derived.by(() => {
		const states = visible.map((tile) => tiles.get(tileKey(tile)));
		const saved = states.filter((tile) => tile?.status === 'saved');
		const loading = states.length === 0 || states.some((tile) => !tile);
		const failure = mostImportantFailure.find((status) =>
			states.some((tile) => tile?.status === status)
		);
		const partial = saved.some((tile) => tile.chunks === null || tile.chunks < CHUNKS_PER_TILE);
		const savedAt = saved
			.map((tile) => tile.savedAt)
			.filter((time) => time !== null)
			.sort((a, b) => Date.parse(b) - Date.parse(a))[0];
		let message = 'Saved terrain available.';
		if (loading) {
			message = 'Loading saved terrain…';
		} else if (failure) {
			message = NATIVE_FAILURE_MESSAGE[failure];
		} else if (saved.length === 0) {
			message = spawn
				? 'No saved terrain here. Move or try the world spawn.'
				: 'No saved terrain here. Move to a different area.';
		} else if (partial || saved.length < states.length) {
			message = 'Only part of this area has saved terrain.';
		}
		return { loading, failure, savedAt, message };
	});

	function moveTo(x: number, z: number) {
		center = { x: clampBlock(x), z: clampBlock(z) };
		copyNote = '';
	}

	function changeZoom(direction: number) {
		zoom = Math.min(maximumZoom, Math.max(0, zoom + direction));
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

	function tilePixel(block: number, centerBlock: number, viewport: number): number {
		return Math.round(viewport / 2 + (block * TILE_BLOCKS - centerBlock) * scale);
	}
</script>

<section class="min-w-0 border border-border bg-card" aria-labelledby={id + '-title'}>
	<header class="flex flex-wrap items-start justify-between gap-3 border-b border-border px-4 py-3">
		<div class="min-w-0">
			<h2 id={id + '-title'} class="text-sm font-semibold text-foreground">{title}</h2>
			<p class="mt-1 text-xs text-muted-foreground">Overworld · Saved terrain</p>
		</div>
		{#if result?.status === 'ready'}
			<div role="group" aria-label="Map zoom and position" class="flex flex-wrap gap-1">
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
		{/if}
	</header>

	{#if !result}
		<div class="px-4 py-10" role="status">
			<CarbonInlineLoading description="Loading saved terrain…" />
		</div>
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
			</div>
		</div>
		<div class="space-y-3 border-t border-border px-4 py-3">
			<div class="flex min-w-0 flex-wrap items-center justify-between gap-2">
				<div class="min-w-0 text-xs text-muted-foreground">
					<p role="status">{summary.message}</p>
					{#if summary.savedAt}
						<p class="mt-1">
							Newest tile save <time datetime={summary.savedAt}
								>{formatTime.format(new Date(summary.savedAt))}</time
							>
						</p>
					{/if}
				</div>
				{#if summary.failure !== 'access' && summary.failure !== 'unsupported' && !summary.loading}
					<CarbonButton kind="tertiary" size="sm" class="gap-2" onclick={retryMap}>
						<RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
						<span>{summary.failure ? 'Retry terrain' : 'Refresh terrain'}</span>
					</CarbonButton>
				{/if}
			</div>

			<p id={id + '-controls'} class="text-xs text-muted-foreground">
				Drag or use the arrow keys to pan. Use + and − to zoom.
			</p>
			<div class="flex min-w-0 flex-wrap items-end justify-between gap-3">
				<form
					onsubmit={jumpToCoordinates}
					class="flex min-w-0 flex-wrap items-end gap-2"
					aria-label="Jump to coordinates"
				>
					<div class="grid min-w-0 grid-cols-2 gap-2">
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
