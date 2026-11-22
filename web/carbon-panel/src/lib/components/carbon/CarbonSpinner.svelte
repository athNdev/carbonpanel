<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<!--
Theme-consistent indeterminate loading spinner.

Replaces the ad-hoc "three-sided square" CSS border spinners
(`border-t-transparent` + `animate-spin`) with an SVG arc that matches the
IBM Carbon design language: sharp geometry, IBM Plex spacing, and motion
tokens from `lib/design-system/tokens.css`. The arc sweeps and rotates like
Carbon's own loading indicator, and it collapses to a static pulsing glyph
under `prefers-reduced-motion`.

Color: the arc draws with the INHERITED text color, so it stays visible on
any surface — inside a primary button it is white like the label, on the
page background a call site opts into the Carbon blue with `text-primary`.
A hardcoded `var(--primary)` here would vanish against primary buttons.
-->

<script lang="ts">
	import type { Snippet } from 'svelte';

	let {
		/** sm: inline in buttons/text. md: panel rows. lg: page-level waiting states. */
		size = 'md',
		/** Accessible label; defaults to a generic "Loading". */
		label = 'Loading',
		/** Optional trailing text, e.g. "Connecting to server daemon...". */
		description,
		children
	}: {
		size?: 'sm' | 'md' | 'lg';
		label?: string;
		description?: string;
		children?: Snippet;
	} = $props();

	const dimensions = { sm: 16, md: 24, lg: 40 } as const;
	const px = $derived(dimensions[size]);
</script>

<span class="carbon-spinner inline-flex items-center gap-3 align-middle" role="status">
	<svg
		width={px}
		height={px}
		viewBox="0 0 40 40"
		fill="none"
		aria-hidden="true"
		class="carbon-spinner-arc shrink-0"
	>
		<title>{label}</title>
		<circle cx="20" cy="20" r="16" stroke="currentColor" stroke-opacity="0.15" stroke-width="4" />
		<circle
			class="carbon-spinner-sweep"
			cx="20"
			cy="20"
			r="16"
			stroke="currentColor"
			stroke-width="4"
			stroke-linecap="butt"
			stroke-dasharray="72 29"
		/>
	</svg>
	{#if description}
		<span class="font-mono text-xs text-muted-foreground">{description}</span>
	{/if}
	{@render children?.()}
</span>

<style>
	/*
		Inherit the text color instead of hardcoding the Carbon blue: inside a
		primary button the arc must be the label color (white), and standalone
		call sites opt into blue with `text-primary` (see CarbonInlineLoading
		and the layout boot screen).
	*/
	.carbon-spinner {
		color: inherit;
	}

	.carbon-spinner-sweep {
		transform-origin: 50% 50%;
		animation: carbon-spinner-rotate 1s var(--ease-in-out-standard, cubic-bezier(0.65, 0, 0.35, 1))
			infinite;
	}

	@keyframes carbon-spinner-rotate {
		0% {
			transform: rotate(0deg);
		}
		50% {
			transform: rotate(450deg);
		}
		100% {
			transform: rotate(1080deg);
		}
	}

	@media (prefers-reduced-motion: reduce) {
		.carbon-spinner-sweep {
			animation: carbon-spinner-pulse 1.6s ease-in-out infinite;
		}

		@keyframes carbon-spinner-pulse {
			0%,
			100% {
				stroke-opacity: 1;
			}
			50% {
				stroke-opacity: 0.35;
			}
		}
	}
</style>
