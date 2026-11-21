<!-- SPDX-License-Identifier: MPL-2.0 -->

<script lang="ts">
	let { label, compact = false }: { label: string; compact?: boolean } = $props();
	const elevations = [0, 1, 2, 1, 0, 1, 2, 2, 1, 0, 0, 1, 0, 1, 2];
</script>

<div class="map-loading" class:compact role="status">
	<div class="tiles" aria-hidden="true">
		{#each elevations as elevation, index (index)}
			<span
				class="tile"
				style:--tile-tint={`${10 + 13 * elevation}%`}
				style:--tile-delay={`${((index % 5) + Math.floor(index / 5)) * 0.17}s`}
			></span>
		{/each}
	</div>
	<p class="text-sm font-medium text-muted-foreground">{label}</p>
</div>

<style>
	.map-loading {
		display: grid;
		justify-items: center;
		align-content: center;
		gap: 1rem;
		min-height: 14rem;
		padding: 1rem;
		text-align: center;
	}

	.map-loading.compact {
		min-height: 0;
		padding: 0.25rem;
		gap: 0.75rem;
	}

	.tiles {
		--tile-size: 1.25rem;
		display: grid;
		grid-template-columns: repeat(5, var(--tile-size));
		gap: 0.1875rem;
	}

	.compact .tiles {
		--tile-size: 0.875rem;
	}

	.tile {
		height: var(--tile-size);
		border: 1px solid color-mix(in srgb, var(--accent-text) 16%, var(--border));
		background: color-mix(in srgb, var(--accent-text) var(--tile-tint), var(--card));
		animation: tile-sweep 3.6s var(--ease-in-out-standard) var(--tile-delay) infinite;
	}

	@keyframes tile-sweep {
		0%,
		38%,
		100% {
			opacity: 0.65;
			transform: translateY(0);
		}
		18% {
			opacity: 1;
			transform: translateY(-0.125rem);
		}
	}

	@media (prefers-reduced-motion: reduce) {
		.tile {
			animation: none;
		}
	}
</style>
