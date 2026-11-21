<!-- SPDX-License-Identifier: MPL-2.0 -->

<script module lang="ts">
	// A decorative cutaway. Each cell is one column, numbered from water level.
	const heights = [
		[3, 3, 2, 2, 0, 1],
		[3, 2, 2, 1, 0, 1],
		[2, 2, 1, 0, 0, 1],
		[2, 1, 1, 0, 1, 2],
		[1, 0, 0, 0, 1, 2],
		[1, 0, 1, 1, 2, 2]
	];
	const landscape = heights.flatMap((row, z) =>
		row.map((height, x) => ({
			x: (x - z) * 10,
			y: (x + z) * 5 - height * 3,
			depth: height * 3 + 7,
			height,
			tree: (x === 1 && z === 1) || (x === 0 && z === 2) || (x === 5 && z === 4),
			delay: -(6 - z) * 0.4
		}))
	);
</script>

<script lang="ts">
	let { label, compact = false }: { label: string; compact?: boolean } = $props();
</script>

<div class="map-loading" class:compact role="status">
	<svg class="landscape" viewBox="-70 -32 140 107" aria-hidden="true" focusable="false">
		{#each landscape as cell (cell)}
			<g transform={`translate(${cell.x} ${cell.y})`} class:water={cell.height === 0}>
				<path class="west" d={`M-10 5l10 5v${cell.depth}l-10-5z`} />
				<path class="east" d={`M0 10l10-5v${cell.depth}l-10 5z`} />
				<path class="surface" class:upland={cell.height >= 2} d="M0 0l10 5-10 5-10-5z" />
				{#if cell.height === 0}
					<path class="ripple" style:animation-delay={`${cell.delay}s`} d="m-4 5 4 2 4-2" />
				{/if}
				{#if cell.tree}
					<path class="trunk" d="M-1 5v-6h2v6l-1 1z" />
					<path class="pine" d="m0-12 5 12-5 3-5-3z" />
					<path class="pine-shadow" d="M0-12 5 0 0 3z" />
				{/if}
			</g>
		{/each}
	</svg>
	<p class="text-sm font-medium text-muted-foreground">{label}</p>
</div>

<style>
	.map-loading {
		display: grid;
		justify-items: center;
		align-content: center;
		gap: 0.5rem;
		min-height: 14rem;
		padding: 1rem;
		text-align: center;
	}

	.map-loading.compact {
		min-height: 0;
		padding: 0.25rem;
	}

	.landscape {
		--meadow: color-mix(in srgb, var(--success-text) 54%, var(--card));
		--stone: color-mix(in srgb, var(--muted-foreground) 52%, var(--card));
		width: min(12rem, 100%);
		height: auto;
		overflow: visible;
	}

	.compact .landscape {
		width: min(7.5rem, 100%);
	}

	.surface {
		fill: var(--meadow);
	}
	.upland {
		fill: color-mix(in srgb, var(--meadow) 84%, var(--foreground));
	}
	.west {
		fill: var(--stone);
	}
	.east {
		fill: color-mix(in srgb, var(--stone) 68%, var(--card));
	}
	.water .surface {
		fill: color-mix(in srgb, var(--accent-text) 78%, var(--card));
	}
	.pine {
		fill: color-mix(in srgb, var(--success-text) 70%, var(--card));
	}
	.pine-shadow,
	.trunk {
		fill: color-mix(in srgb, var(--meadow) 48%, var(--card));
	}

	.ripple {
		fill: none;
		stroke: var(--card);
		stroke-width: 1;
		opacity: 0.55;
	}

	@media (prefers-reduced-motion: no-preference) {
		:global(:root:not([data-carbon-motion='reduced'])) .ripple {
			animation: river-current 4.8s ease-in-out infinite;
		}
	}

	@keyframes river-current {
		0%,
		65%,
		100% {
			opacity: 0.18;
			transform: translateY(-1px);
		}
		32% {
			opacity: 0.85;
			transform: translateY(1px);
		}
	}
</style>
