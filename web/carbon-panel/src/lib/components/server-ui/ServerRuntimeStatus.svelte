<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import {
		CircleHelp,
		CircleX,
		Moon,
		Package,
		Pause,
		Power,
		Square,
		TriangleAlert
	} from '@lucide/svelte';
	import {
		serverStatusLabel as getStatusDisplayName,
		serverStatusTone as getStatusTagType
	} from '$lib/features/servers/presentation';
	import { ServerStatus } from '$lib/proto/carbonpanel/v1/common_pb';
	import { exitFailureCopy } from '$lib/utils/resource-enforcement';
	import { motion } from '$lib/display-preferences.svelte';
	import { prefersReducedMotion } from 'svelte/motion';

	let {
		status,
		statusReason,
		proxied
	}: { status: ServerStatus; statusReason: string; proxied: boolean } = $props();

	const runtimeIcons = {
		[ServerStatus.STOPPED]: Square,
		[ServerStatus.DEEPSLEEP]: Moon,
		[ServerStatus.PAUSED]: Pause,
		[ServerStatus.UNHEALTHY]: TriangleAlert,
		[ServerStatus.ERROR]: CircleX,
		[ServerStatus.CREATING]: Package,
		[ServerStatus.STOPPING]: Power
	} as const;
	let RuntimeIcon = $derived(runtimeIcons[status as keyof typeof runtimeIcons] ?? CircleHelp);
	let ink = $derived(`var(--tag-${getStatusTagType(status)}-text)`);

	let previousStatus: ServerStatus | undefined;
	let shiftedTo = $state<ServerStatus>();
	$effect(() => {
		if ($motion === 'reduced' || prefersReducedMotion.current) shiftedTo = undefined;
		else if (previousStatus !== undefined && status !== previousStatus) shiftedTo = status;
		previousStatus = status;
	});

	let shiftDirection = $derived.by(() => {
		switch (shiftedTo) {
			case ServerStatus.ERROR:
			case ServerStatus.UNHEALTHY:
				return 'attention';
			case ServerStatus.STOPPING:
			case ServerStatus.STOPPED:
			case ServerStatus.PAUSED:
			case ServerStatus.DEEPSLEEP:
				return 'settle';
			default:
				return 'sweep';
		}
	});
</script>

<div
	data-testid="showcase-runtime-tile"
	class="flex min-h-48 flex-col justify-between gap-3 border border-border bg-card p-4 sm:p-5"
	style:--runtime-ink={ink}
>
	<h2 class="text-sm font-semibold">Runtime status</h2>
	<div class="runtime-instrument" aria-hidden="true">
		<div class="runtime-tracks"></div>
		{#key shiftedTo}
			{#if shiftedTo !== undefined}
				<div
					class="runtime-shift"
					data-shift={shiftDirection}
					onanimationend={() => (shiftedTo = undefined)}
				></div>
			{/if}
		{/key}
		{#if [ServerStatus.RUNNING, ServerStatus.STARTING, ServerStatus.RESTARTING].includes(status)}
			<svg
				class="runtime-drive"
				class:winding={status !== ServerStatus.RUNNING}
				viewBox="0 0 90 64"
				aria-hidden="true"
			>
				<path
					d="M7 55h75M21 54l9-21h4l8 21m13 0 6-20h4l7 20"
					fill="none"
					stroke="currentColor"
					stroke-opacity=".35"
					stroke-width="1.5"
				/>
				<g transform="translate(32 31)">
					<g class="drive-wheel">
						<polygon
							points="16.4,-4.4 16.8,-2.6 20.0,-1.9 20.0,1.9 16.8,2.6 16.4,4.4 15.8,6.2 18.3,8.4 16.4,11.6 13.3,10.6 12.0,12.0 10.6,13.3 11.6,16.4 8.4,18.3 6.2,15.8 4.4,16.4 2.6,16.8 1.9,20.0 -1.9,20.0 -2.6,16.8 -4.4,16.4 -6.2,15.8 -8.4,18.3 -11.6,16.4 -10.6,13.3 -12.0,12.0 -13.3,10.6 -16.4,11.6 -18.3,8.4 -15.8,6.2 -16.4,4.4 -16.8,2.6 -20.0,1.9 -20.0,-1.9 -16.8,-2.6 -16.4,-4.4 -15.8,-6.2 -18.3,-8.4 -16.4,-11.6 -13.3,-10.6 -12.0,-12.0 -10.6,-13.3 -11.6,-16.4 -8.4,-18.3 -6.2,-15.8 -4.4,-16.4 -2.6,-16.8 -1.9,-20.0 1.9,-20.0 2.6,-16.8 4.4,-16.4 6.2,-15.8 8.4,-18.3 11.6,-16.4 10.6,-13.3 12.0,-12.0 13.3,-10.6 16.4,-11.6 18.3,-8.4 15.8,-6.2"
						/>
						<path d="M0-12v6m10.4 12-5.2-3m-15.6 3 5.2-3" />
						<circle r="3.5" />
					</g>
				</g>
				<g transform="translate(63 31) rotate(22.5)">
					<g class="drive-wheel pinion">
						<polygon
							points="10.0,-4.1 10.5,-2.4 13.8,-2.0 13.8,2.0 10.5,2.4 10.0,4.1 9.2,5.7 11.1,8.3 8.3,11.1 5.7,9.2 4.1,10.0 2.4,10.5 2.0,13.8 -2.0,13.8 -2.4,10.5 -4.1,10.0 -5.7,9.2 -8.3,11.1 -11.1,8.3 -9.2,5.7 -10.0,4.1 -10.5,2.4 -13.8,2.0 -13.8,-2.0 -10.5,-2.4 -10.0,-4.1 -9.2,-5.7 -11.1,-8.3 -8.3,-11.1 -5.7,-9.2 -4.1,-10.0 -2.4,-10.5 -2.0,-13.8 2.0,-13.8 2.4,-10.5 4.1,-10.0 5.7,-9.2 8.3,-11.1 11.1,-8.3 9.2,-5.7"
						/>
						<path d="M0-7v3m6.1 7-2.6-1.5m-9.6 1.5 2.6-1.5" />
						<circle r="2.25" />
					</g>
				</g>
			</svg>
		{:else}
			<span class="runtime-emblem" aria-hidden="true">
				<RuntimeIcon size={24} strokeWidth={1.5} />
			</span>
		{/if}
	</div>
	<div class="space-y-1 border-t border-border pt-2.5" aria-live="polite" aria-atomic="true">
		<p class="text-xl leading-tight font-medium first-letter:uppercase">
			{getStatusDisplayName(status).toLowerCase()}
		</p>
		<p class="text-xs leading-relaxed text-muted-foreground">
			{#if status === ServerStatus.RUNNING}
				Server container is running
			{:else if status === ServerStatus.STOPPED}
				Container offline
			{:else if status === ServerStatus.CREATING}
				Provisioning container & checking image
			{:else if status === ServerStatus.STARTING}
				Starting server (modpack downloads may take several minutes)
			{:else if status === ServerStatus.RESTARTING}
				Restarting server container
			{:else if status === ServerStatus.DEEPSLEEP}
				{proxied
					? 'Deep sleeping (container stopped, proxy listening for login wake)'
					: 'Deep sleeping (container stopped, direct port listening for login wake)'}
			{:else if status === ServerStatus.PAUSED}
				Paused via cgroup freezer (instant resume available)
			{:else if status === ServerStatus.UNHEALTHY}
				Server running but failing health checks
			{:else if status === ServerStatus.STOPPING}
				Gracefully shutting down
			{:else if status === ServerStatus.ERROR}
				{exitFailureCopy(statusReason)}
			{:else}
				Daemon status: {getStatusDisplayName(status)}
			{/if}
		</p>
	</div>
</div>

<style>
	.runtime-instrument {
		position: relative;
		display: grid;
		min-height: 6.5rem;
		flex: 1;
		place-items: center;
		isolation: isolate;
		overflow: hidden;
		border-block: 1px solid var(--border);
		background: color-mix(in srgb, var(--card) 72%, var(--background));
		color: var(--runtime-ink);
	}
	.runtime-tracks {
		position: absolute;
		inset: 0;
		background:
			linear-gradient(transparent calc(50% - 1px), var(--border) 50%, transparent calc(50% + 1px)),
			radial-gradient(
					circle,
					color-mix(in srgb, currentColor 30%, transparent) 1px,
					transparent 1px
				)
				center / 12px 12px;
		mask-image: linear-gradient(to right, transparent, black 25% 75%, transparent);
	}
	.runtime-tracks::before,
	.runtime-tracks::after {
		position: absolute;
		inset-inline: 12%;
		height: 5px;
		background: repeating-linear-gradient(
			90deg,
			color-mix(in srgb, currentColor 28%, transparent) 0 1px,
			transparent 1px 8px
		);
		content: '';
	}
	.runtime-tracks::before {
		inset-block-start: 5px;
	}
	.runtime-tracks::after {
		inset-block-end: 5px;
	}
	.runtime-shift {
		position: absolute;
		inset: 0;
		opacity: 0;
	}
	.runtime-shift[data-shift='sweep'] {
		background: linear-gradient(
			90deg,
			transparent 5%,
			color-mix(in srgb, currentColor 5%, transparent) 37%,
			color-mix(in srgb, currentColor 20%, transparent) 53% 54%,
			color-mix(in srgb, currentColor 10%, transparent) 54% 57%,
			transparent 57% 61%,
			color-mix(in srgb, currentColor 9%, transparent) 61% 62%,
			transparent 95%
		);
		animation: instrument-sweep 1.25s var(--ease-out-quart);
	}
	.runtime-shift[data-shift='settle'] {
		background: repeating-linear-gradient(
			180deg,
			transparent 0 8px,
			color-mix(in srgb, currentColor 16%, transparent) 8px 10px
		);
		animation: instrument-settle 900ms var(--ease-out-quart);
	}
	.runtime-shift[data-shift='attention'] {
		border-inline: 2px solid var(--runtime-ink);
		background: color-mix(in srgb, currentColor 18%, transparent);
		animation: instrument-attention 950ms var(--ease-out-quart);
	}
	@keyframes instrument-sweep {
		from {
			transform: translateX(-80%);
		}
		24% {
			opacity: 1;
		}
		to {
			transform: translateX(80%);
		}
	}
	@keyframes instrument-settle {
		from {
			opacity: 1;
		}
		to {
			transform: translateY(30%);
		}
	}
	@keyframes instrument-attention {
		from {
			transform: scaleX(0.45);
		}
		32% {
			opacity: 0.9;
		}
		to {
			transform: scaleX(1);
		}
	}
	.runtime-emblem {
		display: grid;
		flex: none;
		width: 3.5rem;
		aspect-ratio: 1;
		z-index: 1;
		place-items: center;
		border: 1px solid currentColor;
		background-color: color-mix(in srgb, currentColor 7%, var(--card));
	}
	.runtime-drive {
		flex: none;
		width: min(9rem, 60%);
		z-index: 1;
		height: auto;
	}
	.drive-wheel {
		fill: var(--card);
		stroke: currentColor;
		stroke-width: 1.4;
		stroke-linejoin: round;
		animation: drive-turn 12s linear infinite;
	}
	.drive-wheel polygon {
		fill: color-mix(in srgb, currentColor 10%, var(--card));
	}
	.pinion {
		animation-duration: 8s;
		animation-direction: reverse;
	}
	.winding .drive-wheel {
		animation: drive-engage 3s cubic-bezier(0.35, 0, 0.3, 1) infinite;
	}
	.winding .pinion {
		animation-name: pinion-engage;
	}
	@keyframes drive-turn {
		to {
			transform: rotate(360deg);
		}
	}
	@keyframes drive-engage {
		0%,
		25% {
			transform: rotate(0);
		}
		85%,
		100% {
			transform: rotate(240deg);
		}
	}
	@keyframes pinion-engage {
		0%,
		25% {
			transform: rotate(0);
		}
		85%,
		100% {
			transform: rotate(-360deg);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.runtime-drive .drive-wheel,
		.runtime-shift[data-shift] {
			animation: none;
		}
	}
	:global([data-carbon-motion='reduced']) .runtime-drive .drive-wheel,
	:global([data-carbon-motion='reduced']) .runtime-shift[data-shift] {
		animation: none;
	}
</style>
