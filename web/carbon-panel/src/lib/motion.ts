// SPDX-License-Identifier: MPL-2.0

/**
 * Shared reduced-motion gate for CSS view-transitions and Svelte JS
 * transitions. The stylesheet can zero CSS animation duration; Svelte
 * `transition:`/`in:`/`out:` helpers must read this at call time.
 */
export function prefersReducedMotion(): boolean {
	if (typeof document === 'undefined' || typeof window === 'undefined') {
		return false;
	}
	return (
		window.matchMedia('(prefers-reduced-motion: reduce)').matches ||
		document.documentElement.getAttribute('data-carbon-motion') === 'reduced'
	);
}

/** Snap Svelte/CSS durations to zero when the user asked for reduced motion. */
export function motionDuration(ms: number): number {
	return prefersReducedMotion() ? 0 : ms;
}
