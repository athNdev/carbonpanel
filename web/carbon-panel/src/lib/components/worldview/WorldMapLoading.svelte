<!-- SPDX-License-Identifier: MPL-2.0 -->

<script lang="ts">
	let { label, compact = false }: { label: string; compact?: boolean } = $props();
	const bearings = Array.from({ length: 32 }, (_, index) => index * 11.25);
</script>

<div class="map-loading" class:compact role="status">
	<svg class="compass" viewBox="-52 -52 104 104" aria-hidden="true" focusable="false">
		<circle r="45" fill="none" stroke="var(--border)" />
		{#each bearings as angle, index (angle)}
			<path
				transform={`rotate(${angle})`}
				d={index % 8 === 0 ? 'M0 -41v7' : index % 4 === 0 ? 'M0 -41v5' : 'M0 -41v2'}
				stroke="currentColor"
			/>
		{/each}
		<path d="M-3 -48 0 -52 3 -48Z" fill="var(--accent-text)" />
		<g class="needle">
			<path d="M0 -30 5 0 0 3 -5 0Z" fill="var(--accent-text)" />
			<path d="m0 30-5-30 5-3 5 3Z" fill="currentColor" />
			<circle r="3" fill="var(--card)" stroke="currentColor" />
		</g>
	</svg>
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
	.compact {
		min-height: 0;
		gap: 0.625rem;
		padding: 0.25rem;
	}
	.compass {
		display: block;
		width: min(6.5rem, 100%);
		height: auto;
		color: var(--muted-foreground);
	}
	.compact .compass {
		width: min(4rem, 100%);
	}
	.needle {
		transform: rotate(24deg);
	}
	@media (prefers-reduced-motion: no-preference) {
		:global(:root:not([data-carbon-motion='reduced'])) .needle {
			animation: find-bearing 5s ease-in-out infinite;
		}
	}
	@keyframes find-bearing {
		0%,
		100% {
			transform: rotate(24deg);
		}
		34% {
			transform: rotate(-42deg);
		}
		62% {
			transform: rotate(32deg);
		}
		80% {
			transform: rotate(20deg);
		}
	}
</style>
